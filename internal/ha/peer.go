package ha

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/lieyanc/FireGateway/internal/config"
)

// Protocol 4 makes the primary the only writer and router owner: the peer
// reports whether it serves, and a backup can ask to become primary
// ("yield") or ask the primary to take the router back ("claim").
const Protocol = 4

const PeerTimeout = 8 * time.Second
const RouterTimeout = 8 * time.Second
const peerBodyLimit = 2 << 20

type PeerState struct {
	NodeID           string `json:"nodeId"`
	PairID           string `json:"pairId"`
	Paired           bool   `json:"paired"`
	Writer           string `json:"writer"`
	Epoch            int64  `json:"epoch"`
	Revision         int64  `json:"revision"`
	Checksum         string `json:"checksum"`
	PreparedChecksum string `json:"preparedChecksum"`
	Serving          bool   `json:"serving"`
	Degraded         bool   `json:"degraded"`
	PendingID        string `json:"pendingId,omitempty"`
	Transferring     bool   `json:"transferring"`
	// Usage is the node's own traffic count per tenant for quota checks.
	Usage map[string]config.TenantUsage `json:"usage,omitempty"`
}
type Mutation struct {
	ID       string          `json:"id"`
	Expected string          `json:"expected"`
	Method   string          `json:"method"`
	Path     string          `json:"path"`
	Body     json.RawMessage `json:"body,omitempty"`
	// Actor identifies the authenticated caller on the originating node: a
	// user id, or one of the reserved "@" actors of the api package.
	Actor string `json:"actor,omitempty"`
}
type PeerRequest struct {
	Protocol    int          `json:"protocol"`
	ClusterID   string       `json:"clusterId"`
	NodeID      string       `json:"nodeId"`
	Receiver    string       `json:"receiver"`
	Topology    string       `json:"topology"`
	Method      string       `json:"method"`
	PairID      string       `json:"pairId,omitempty"`
	ID          string       `json:"id,omitempty"`
	Checksum    string       `json:"checksum,omitempty"`
	Epoch       int64        `json:"epoch,omitempty"`
	Transaction *Transaction `json:"transaction,omitempty"`
	Handoff     *Handoff     `json:"handoff,omitempty"`
	Mutation    *Mutation    `json:"mutation,omitempty"`
}
type PeerResponse struct {
	Protocol int             `json:"protocol"`
	NodeID   string          `json:"nodeId"`
	Error    string          `json:"error,omitempty"`
	Code     string          `json:"code,omitempty"`
	State    *PeerState      `json:"state,omitempty"`
	Snapshot *config.RuleSet `json:"snapshot,omitempty"`
	Reply    *Reply          `json:"reply,omitempty"`
}
type PeerAPI interface {
	Call(context.Context, string, PeerRequest) (PeerResponse, error)
}

// Protocol/auth/config failures do not prove a machine has gone offline.
type PeerFault struct{ Message string }

func (e *PeerFault) Error() string { return e.Message }

type PeerClient struct {
	cfg  config.ClusterConfig
	node string
	http *http.Client
}

func secureHTTP(ca string, timeout time.Duration) (*http.Client, error) {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if ca != "" {
		b, err := os.ReadFile(ca)
		if err != nil {
			return nil, err
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(b) {
			return nil, errors.New("CA file has no certificates")
		}
		t.TLSClientConfig.RootCAs = pool
	}
	return &http.Client{Transport: t, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

// NewPeerClient dials the peer's private node port with this node's
// certificate.
func NewPeerClient(cfg config.ClusterConfig, node string) (*PeerClient, error) {
	_, tlsCfg, err := peerTLS(cfg.PeerToken, cfg.ID, node, cfg.Address, cfg.PeerID)
	if err != nil {
		return nil, err
	}
	t := &http.Transport{
		TLSClientConfig:     tlsCfg,
		ForceAttemptHTTP2:   false,
		MaxIdleConns:        2,
		IdleConnTimeout:     time.Minute,
		TLSHandshakeTimeout: 3 * time.Second,
		DialContext:         (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 15 * time.Second}).DialContext,
	}
	return &PeerClient{cfg: cfg, node: node, http: &http.Client{Transport: t, Timeout: PeerTimeout}}, nil
}

func (p *PeerClient) Close() { p.http.CloseIdleConnections() }

func topology(cfg config.ClusterConfig, node string) string {
	b, _ := json.Marshal(struct {
		InitialWriter, Router string
		Nodes                 map[string]string
		Redirects             []string
		PeerPort              int
	}{cfg.InitialWriter, cfg.RouterURL, map[string]string{node: cfg.Address, cfg.PeerID: cfg.PeerAddress}, cfg.Redirects, cfg.PeerPort})
	return digest(b)
}
func (p *PeerClient) Call(ctx context.Context, method string, req PeerRequest) (PeerResponse, error) {
	req.Protocol = Protocol
	req.ClusterID = p.cfg.ID
	req.NodeID = p.node
	req.Receiver = p.cfg.PeerID
	req.Topology = topology(p.cfg, p.node)
	req.Method = method
	b, err := json.Marshal(req)
	if err != nil {
		return PeerResponse{}, err
	}
	address := "https://" + peerEndpoint(p.cfg.PeerAddress, p.cfg.PeerPort) + "/v1"
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, address, bytes.NewReader(b))
	if err != nil {
		return PeerResponse{}, err
	}
	hr.Header.Set("Content-Type", "application/json")
	res, err := p.http.Do(hr)
	if err != nil {
		return PeerResponse{}, peerFailure(err)
	}
	defer res.Body.Close()
	// Past the handshake only the peer itself can answer, so any malformed
	// reply is a configuration or version fault, never a sign of an outage.
	if res.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 256))
		if text := strings.TrimSpace(string(msg)); text != "" {
			return PeerResponse{}, &PeerFault{fmt.Sprintf("peer HTTP status %d: %s", res.StatusCode, text)}
		}
		return PeerResponse{}, &PeerFault{fmt.Sprintf("peer HTTP status %d", res.StatusCode)}
	}
	var out PeerResponse
	if err = json.NewDecoder(io.LimitReader(res.Body, peerBodyLimit)).Decode(&out); err != nil {
		if ctx.Err() != nil {
			return out, fmt.Errorf("peer connection failed: %w", err)
		}
		return out, &PeerFault{"invalid peer response"}
	}
	if out.Protocol != Protocol || out.NodeID != p.cfg.PeerID {
		return out, &PeerFault{fmt.Sprintf("peer identity or protocol mismatch (peer protocol %d, want %d); run the same release on both nodes", out.Protocol, Protocol)}
	}
	if out.Error != "" {
		switch out.Code {
		case "conflict":
			return out, config.ErrRevisionConflict
		case "unavailable":
			return out, ErrFrozen
		default:
			return out, &PeerFault{out.Error}
		}
	}
	return out, nil
}

// SetMutator installs the writer-side executor for edits forwarded by the
// peer. Until it is set, forwarded edits are refused as read-only.
func (c *Controller) SetMutator(fn func(context.Context, Mutation) Reply) { c.mutator.Store(&fn) }

// Listen serves the node link on this node's LAN address. Call it before Run.
func (c *Controller) Listen() error {
	server, _, err := peerTLS(c.cfg.PeerToken, c.cfg.ID, c.node, c.cfg.Address, c.cfg.PeerID)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", peerEndpoint(c.cfg.Address, c.cfg.PeerPort))
	if err != nil {
		return fmt.Errorf("node link: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle("POST /v1", c.peerHandler())
	srv := &http.Server{Handler: mux, TLSConfig: server, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: PeerTimeout, WriteTimeout: 2 * PeerTimeout, IdleTimeout: time.Minute, ErrorLog: log.New(io.Discard, "", 0)}
	c.server = srv
	go func() { _ = srv.Serve(tls.NewListener(ln, server)) }()
	return nil
}

func (c *Controller) peerHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		var req PeerRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, peerBodyLimit))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			http.Error(w, "invalid node request", 400)
			return
		}
		var extra any
		if dec.Decode(&extra) != io.EOF {
			http.Error(w, "invalid trailing JSON", 400)
			return
		}
		if req.Protocol != Protocol {
			http.Error(w, fmt.Sprintf("peer protocol %d is not supported (want %d); run the same release on both nodes", req.Protocol, Protocol), 400)
			return
		}
		if req.ClusterID != c.cfg.ID || req.NodeID != c.cfg.PeerID || req.Receiver != c.node || req.Topology != topology(c.cfg, c.node) {
			http.Error(w, "node identity or pair topology mismatch", 403)
			return
		}
		out := PeerResponse{Protocol: Protocol, NodeID: c.node}
		var err error
		switch req.Method {
		case "heartbeat":
			st := c.peerState()
			out.State = &st
		case "snapshot":
			st := c.peerState()
			snap := c.replica.state().Committed
			out.State = &st
			out.Snapshot = &snap
		case "prepare":
			if req.Transaction == nil {
				err = errors.New("transaction required")
			} else {
				err = c.replica.Prepare(*req.Transaction)
			}
		case "commit":
			err = c.replica.CommitPeer(req.PairID, req.ID)
		case "handoff-prepare", "handoff-commit":
			if req.Handoff == nil {
				err = errors.New("handoff required")
			} else {
				err = c.replica.ReceiveHandoff(req.PairID, *req.Handoff, req.Method == "handoff-commit")
			}
		case "resume":
			err = c.replica.Resume(req.PairID, req.Checksum, req.Epoch)
		case "yield":
			err = c.yield(r.Context(), req)
		case "claim":
			err = c.claimRequest(req)
		case "mutate":
			mutate := c.mutator.Load()
			if req.Mutation == nil || mutate == nil || c.replica.state().Writer != c.node {
				err = ErrFrozen
			} else {
				reply := (*mutate)(r.Context(), *req.Mutation)
				out.Reply = &reply
			}
		default:
			err = errors.New("unknown node method")
		}
		if err != nil {
			out.Error = err.Error()
			out.Code = "invalid"
			if errors.Is(err, config.ErrRevisionConflict) {
				out.Code = "conflict"
			}
			if errors.Is(err, config.ErrClusterUnavailable) {
				out.Code = "unavailable"
			}
		} else if req.Method != "heartbeat" && req.Method != "snapshot" && req.Method != "prepare" {
			// Apply a decided change now instead of at the next poll.
			c.wake()
		}
		_ = json.NewEncoder(w).Encode(out)
	})
}
