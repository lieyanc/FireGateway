package ha

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/lieyanc/FireGateway/internal/config"
)

type Ingress struct {
	Address     string
	Fingerprint string
	Values      map[string]map[string]any
}
type Upstream interface {
	Read(context.Context) (Ingress, error)
	Switch(context.Context, Ingress, string) error
}

// Client uses only existing rpcd UCI methods. It stores no cluster data on
// OpenWrt, and executes no commands or scripts there.
type Client struct {
	cfg     config.ClusterConfig
	http    *http.Client
	mu      sync.Mutex
	session string
}

func NewClient(cfg config.ClusterConfig) (*Client, error) {
	h, err := secureHTTP(cfg.CAFile, RouterTimeout)
	if err != nil {
		return nil, err
	}
	return &Client{cfg: cfg, http: h}, nil
}
func (c *Client) rpc(ctx context.Context, session, object, method string, args any, out any) (int, error) {
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "call", "params": []any{session, object, method, args}})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.RouterURL, bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return 0, errors.New("router API unreachable or TLS verification failed")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return 0, fmt.Errorf("router HTTP status %d", res.StatusCode)
	}
	var reply struct {
		Result []json.RawMessage `json:"result"`
		Error  json.RawMessage   `json:"error"`
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&reply); err != nil {
		return 0, err
	}
	if len(reply.Result) == 0 {
		return 0, errors.New("invalid router ubus response")
	}
	var code int
	if err = json.Unmarshal(reply.Result[0], &code); err != nil {
		return 0, err
	}
	if code != 0 {
		return code, fmt.Errorf("router %s.%s returned ubus status %d", object, method, code)
	}
	// Native UCI mutations normally return [0], with no response body.
	if out == nil {
		return 0, nil
	}
	if len(reply.Result) < 2 {
		return 0, errors.New("router returned no data")
	}
	return 0, json.Unmarshal(reply.Result[1], out)
}
func (c *Client) login(ctx context.Context) (string, error) {
	var out struct {
		Session string `json:"ubus_rpc_session"`
	}
	_, err := c.rpc(ctx, "00000000000000000000000000000000", "session", "login", map[string]any{"username": c.cfg.Username, "password": c.cfg.Password, "timeout": 300}, &out)
	if err != nil {
		return "", err
	}
	if out.Session == "" {
		return "", errors.New("router authentication failed")
	}
	return out.Session, nil
}
func (c *Client) read(ctx context.Context, session string) (Ingress, int, error) {
	var out struct {
		Values map[string]map[string]any `json:"values"`
	}
	code, err := c.rpc(ctx, session, "uci", "get", map[string]any{"config": "firewall"}, &out)
	if err != nil {
		return Ingress{}, code, err
	}
	in := Ingress{Values: map[string]map[string]any{}}
	for _, name := range c.cfg.Redirects {
		v, ok := out.Values[name]
		if !ok || v[".type"] != "redirect" || v[".anonymous"] == true || v["target"] != "DNAT" || v["enabled"] == "0" {
			return in, 0, fmt.Errorf("firewall.%s must be an enabled named DNAT redirect", name)
		}
		address, ok := v["dest_ip"].(string)
		if !ok || (address != c.cfg.Address && address != c.cfg.PeerAddress) {
			return in, 0, fmt.Errorf("firewall.%s has an unmanaged destination", name)
		}
		if in.Address != "" && in.Address != address {
			return in, 0, errors.New("managed redirects have mixed destinations; resolve partial or external changes")
		}
		in.Address = address
		in.Values[name] = v
	}
	if in.Address == "" {
		return in, 0, errors.New("no managed redirects")
	}
	b, _ := json.Marshal(in.Values)
	in.Fingerprint = digest(b)
	return in, 0, nil
}
func (c *Client) Read(ctx context.Context) (Ingress, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		if c.session == "" {
			var err error
			c.session, err = c.login(ctx)
			if err != nil {
				return Ingress{}, err
			}
		}
		in, code, err := c.read(ctx, c.session)
		if code == 6 {
			c.session = ""
			continue
		}
		return in, err
	}
	return Ingress{}, errors.New("router access denied; existing UCI read permission is required")
}
func (c *Client) Switch(ctx context.Context, expected Ingress, address string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if address != c.cfg.Address {
		return errors.New("can only switch ingress to this node")
	}
	// A new session isolates our staged changes from LuCI and previous failed
	// calls. Never reauthenticate mid-transaction and lose the staging context.
	session, err := c.login(ctx)
	if err != nil {
		return err
	}
	original, _, err := c.read(ctx, session)
	if err != nil {
		return err
	}
	if original.Fingerprint != expected.Fingerprint {
		return errors.New("upstream rules changed before switching")
	}
	// A separate read session sees committed configuration, not our staged set.
	observer, err := c.login(ctx)
	if err != nil {
		return err
	}
	var changes struct {
		Changes map[string]json.RawMessage `json:"changes"`
	}
	if _, err = c.rpc(ctx, session, "uci", "changes", map[string]any{}, &changes); err != nil {
		return err
	}
	for _, change := range changes.Changes {
		if string(change) != "[]" && string(change) != "{}" && string(change) != "null" {
			return errors.New("router session has pending changes")
		}
	}
	applied := false
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2e9)
		defer cancel()
		if !applied {
			_, _ = c.rpc(cleanup, session, "uci", "revert", map[string]any{"config": "firewall"}, nil)
		}
		// Do not destroy a rollback session after an uncertain apply/confirm;
		// OpenWrt's native rollback timeout remains authoritative for that attempt.
	}()
	for _, name := range c.cfg.Redirects {
		if _, err = c.rpc(ctx, session, "uci", "set", map[string]any{"config": "firewall", "section": name, "values": map[string]string{"dest_ip": address}}, nil); err != nil {
			return err
		}
	}
	current, _, err := c.read(ctx, observer)
	if err != nil {
		return err
	}
	if current.Fingerprint != original.Fingerprint {
		return errors.New("upstream rules changed while staging")
	}
	if _, err = c.rpc(ctx, session, "uci", "apply", map[string]any{"rollback": true, "timeout": 30}, nil); err != nil {
		return fmt.Errorf("upstream apply unconfirmed: %w", err)
	}
	applied = true
	actual, _, err := c.read(ctx, observer)
	if err != nil {
		return err
	}
	desired := clone(original.Values)
	for _, name := range c.cfg.Redirects {
		desired[name]["dest_ip"] = address
	}
	b, _ := json.Marshal(desired)
	if actual.Fingerprint != digest(b) {
		return errors.New("upstream configuration verification failed; native rollback remains pending")
	}
	if _, err = c.rpc(ctx, session, "uci", "confirm", map[string]any{}, nil); err != nil {
		return fmt.Errorf("upstream confirmation uncertain: %w", err)
	}
	c.session = observer
	return nil
}
