package ha

import (
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/lieyanc/FireGateway/internal/config"
)

// Protocol 2 adds replicated accounts to snapshots and actors to mutations.
const Protocol = 2

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
	Frozen           bool   `json:"frozen"`
	Takeover         bool   `json:"takeover"`
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
func NewPeerClient(cfg config.ClusterConfig, node string) (*PeerClient, error) {
	h, err := secureHTTP(cfg.PeerCAFile, PeerTimeout)
	if err != nil {
		return nil, err
	}
	return &PeerClient{cfg: cfg, node: node, http: h}, nil
}

func (p *PeerClient) Close() { p.http.CloseIdleConnections() }

func topology(cfg config.ClusterConfig, node string) string {
	b, _ := json.Marshal(struct {
		InitialWriter, Router string
		Nodes                 map[string]string
		Redirects             []string
	}{cfg.InitialWriter, cfg.RouterURL, map[string]string{node: cfg.Address, cfg.PeerID: cfg.PeerAddress}, cfg.Redirects})
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
	address := strings.TrimRight(p.cfg.PeerURL, "/") + "/internal/ha/v1"
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, address, bytes.NewReader(b))
	if err != nil {
		return PeerResponse{}, err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Authorization", "Bearer "+p.cfg.PeerToken)
	res, err := p.http.Do(hr)
	if err != nil {
		var cert *tls.CertificateVerificationError
		var ue *url.Error
		if errors.As(err, &cert) {
			return PeerResponse{}, &PeerFault{"peer TLS verification failed"}
		}
		if errors.As(err, &ue) {
			return PeerResponse{}, fmt.Errorf("peer connection failed: %w", ue.Err)
		}
		return PeerResponse{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 256))
		if text := strings.TrimSpace(string(msg)); text != "" {
			return PeerResponse{}, &PeerFault{fmt.Sprintf("peer HTTP status %d: %s", res.StatusCode, text)}
		}
		return PeerResponse{}, &PeerFault{fmt.Sprintf("peer HTTP status %d", res.StatusCode)}
	}
	var out PeerResponse
	if err = json.NewDecoder(io.LimitReader(res.Body, peerBodyLimit)).Decode(&out); err != nil {
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
func (c *Controller) PeerHandler(mutate func(context.Context, Mutation) Reply) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if r.Method != http.MethodPost || subtle.ConstantTimeCompare([]byte(auth), []byte(c.cfg.PeerToken)) != 1 {
			http.Error(w, "node authentication required", http.StatusUnauthorized)
			return
		}
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
		case "mutate":
			if req.Mutation == nil || c.replica.state().Writer != c.node {
				err = ErrFrozen
			} else {
				reply := mutate(r.Context(), *req.Mutation)
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
		}
		_ = json.NewEncoder(w).Encode(out)
	})
}
