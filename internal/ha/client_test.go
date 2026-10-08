package ha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/lieyanc/FireGateway/internal/config"
)

type rpcdFake struct {
	mu       sync.Mutex
	values   map[string]map[string]any
	sessions map[string]map[string]string
	serial   int
	expired  bool
	deny     string
	calls    []string
}

func (f *rpcdFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var req struct {
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Method != "call" || len(req.Params) != 4 {
		http.Error(w, "invalid", 400)
		return
	}
	var sid, obj, method string
	_ = json.Unmarshal(req.Params[0], &sid)
	_ = json.Unmarshal(req.Params[1], &obj)
	_ = json.Unmarshal(req.Params[2], &method)
	var args map[string]any
	_ = json.Unmarshal(req.Params[3], &args)
	f.calls = append(f.calls, obj+"."+method)
	var result = []any{0}
	switch {
	case obj == "session" && method == "login":
		f.serial++
		sid = fmt.Sprintf("s%d", f.serial)
		f.sessions[sid] = map[string]string{}
		result = append(result, map[string]string{"ubus_rpc_session": sid})
	case obj != "uci":
		result = []any{3}
	case f.expired:
		f.expired = false
		result = []any{6}
	case f.sessions[sid] == nil:
		result = []any{6}
	case method == f.deny:
		result = []any{7}
	case method == "get":
		values := clone(f.values)
		for name, ip := range f.sessions[sid] {
			values[name]["dest_ip"] = ip
		}
		result = append(result, map[string]any{"values": values})
	case method == "changes":
		result = append(result, map[string]any{"changes": map[string]any{}})
	case method == "set":
		vals, ok := args["values"].(map[string]any)
		if !ok || len(vals) != 1 || args["config"] != "firewall" {
			result = []any{2}
			break
		}
		ip, ok := vals["dest_ip"].(string)
		if !ok {
			result = []any{2}
			break
		}
		f.sessions[sid][args["section"].(string)] = ip
	case method == "apply":
		if args["rollback"] != true || args["timeout"] != float64(30) {
			result = []any{2}
			break
		}
		for name, ip := range f.sessions[sid] {
			f.values[name]["dest_ip"] = ip
		}
		f.sessions[sid] = map[string]string{}
	case method == "revert":
		f.sessions[sid] = map[string]string{}
	case method == "confirm":
	default:
		result = []any{3}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
}
func routerClient(t *testing.T) (*Client, *rpcdFake) {
	t.Helper()
	f := &rpcdFake{sessions: map[string]map[string]string{}, values: map[string]map[string]any{
		"dmz":       {".type": "redirect", "target": "DNAT", "dest_ip": "192.168.1.10", "src": "wan", "dest": "lan", "proto": "tcp udp"},
		"unrelated": {".type": "redirect", "target": "DNAT", "dest_ip": "192.168.1.99", "src_dport": "22"}}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c, err := NewClient(config.ClusterConfig{RouterURL: srv.URL, Username: "test", Password: "test-only", Address: "192.168.1.11", PeerAddress: "192.168.1.10", Redirects: []string{"dmz"}})
	if err != nil {
		t.Fatal(err)
	}
	return c, f
}
func TestNativeOpenWrtSwitchPreservesOtherRules(t *testing.T) {
	c, f := routerClient(t)
	ctx := context.Background()
	f.expired = true
	in, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if f.serial != 2 {
		t.Fatal("expired read session was not refreshed")
	}
	if err = c.Switch(ctx, in, c.cfg.Address); err != nil {
		t.Fatal(err)
	}
	if f.values["dmz"]["dest_ip"] != c.cfg.Address || f.values["unrelated"]["dest_ip"] != "192.168.1.99" || f.values["dmz"]["src"] != "wan" {
		t.Fatal("switch changed unrelated configuration")
	}
	if f.serial != 3 {
		t.Fatalf("switch should reuse the read session and open one staging session, got %d logins", f.serial)
	}
	if _, err = c.Read(ctx); err != nil || f.serial != 3 {
		t.Fatal("polling opened a new router session")
	}
	allowed := map[string]bool{"session.login": true, "uci.get": true, "uci.set": true, "uci.changes": true, "uci.apply": true, "uci.confirm": true, "uci.revert": true}
	for _, method := range f.calls {
		if !allowed[method] {
			t.Fatalf("non-native method: %s", method)
		}
	}
}
func TestNativeSwitchRejectsConcurrentAndUnconfirmedChanges(t *testing.T) {
	c, f := routerClient(t)
	ctx := context.Background()
	in, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.values["dmz"]["src"] = "changed"
	if err = c.Switch(ctx, in, c.cfg.Address); err == nil {
		t.Fatal("concurrent config edit overwritten")
	}
	if f.values["dmz"]["dest_ip"] == c.cfg.Address {
		t.Fatal("destination changed despite conflict")
	}
	in, err = c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.deny = "apply"
	if err = c.Switch(ctx, in, c.cfg.Address); err == nil || errors.Is(err, ErrSwitchUncertain) {
		t.Fatalf("a refused apply must be a definite failure: %v", err)
	}
	if f.values["dmz"]["dest_ip"] == c.cfg.Address {
		t.Fatal("staging changed committed config")
	}
	f.deny = "confirm"
	if err = c.Switch(ctx, in, c.cfg.Address); !errors.Is(err, ErrSwitchUncertain) {
		t.Fatalf("a failed confirm must leave the outcome to OpenWrt's rollback: %v", err)
	}
}
func TestUnexpectedDestinationCannotBeClaimed(t *testing.T) {
	c, f := routerClient(t)
	f.values["dmz"]["dest_ip"] = "192.168.1.55"
	if _, err := c.Read(context.Background()); err == nil {
		t.Fatal("unmanaged destination accepted")
	}
	f.values["dmz"]["dest_ip"] = c.cfg.PeerAddress
	f.values["dmz"][".anonymous"] = true
	if _, err := c.Read(context.Background()); err == nil {
		t.Fatal("anonymous redirect accepted")
	}
}
