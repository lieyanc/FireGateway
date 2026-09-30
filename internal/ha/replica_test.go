package ha

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lieyanc/FireGateway/internal/config"
	"github.com/lieyanc/FireGateway/internal/events"
	"github.com/lieyanc/FireGateway/internal/gateway"
)

type wire struct {
	mu        sync.Mutex
	target    *Controller
	offline   bool
	loseAfter string
	fault     bool
}

func (w *wire) Call(ctx context.Context, method string, req PeerRequest) (PeerResponse, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fault {
		return PeerResponse{}, &PeerFault{"invalid credentials"}
	}
	if w.offline {
		return PeerResponse{}, context.DeadlineExceeded
	}
	c := w.target
	out := PeerResponse{Protocol: 1, NodeID: c.node}
	var err error
	switch method {
	case "heartbeat", "snapshot":
		st := c.peerState()
		out.State = &st
		if method == "snapshot" {
			snap := c.replica.state().Committed
			out.Snapshot = &snap
		}
	case "prepare":
		err = c.replica.Prepare(*req.Transaction)
	case "commit":
		err = c.replica.CommitPeer(req.PairID, req.ID)
	case "handoff-prepare", "handoff-commit":
		err = c.replica.ReceiveHandoff(req.PairID, *req.Handoff, method == "handoff-commit")
	case "resume":
		err = c.replica.Resume(req.PairID, req.Checksum, req.Epoch)
	default:
		return out, errors.New("unexpected method " + method)
	}
	if w.loseAfter == method {
		w.loseAfter = ""
		return out, context.DeadlineExceeded
	}
	return out, err
}

type fakeUpstream struct {
	mu        sync.Mutex
	address   string
	count     int
	err       error
	switchErr error
}

func (f *fakeUpstream) Read(context.Context) (Ingress, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return Ingress{Address: f.address, Fingerprint: f.address}, f.err
}
func (f *fakeUpstream) Switch(_ context.Context, in Ingress, ip string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.switchErr != nil {
		return f.switchErr
	}
	if in.Fingerprint != f.address {
		return errors.New("changed")
	}
	f.address = ip
	f.count++
	return nil
}

type pair struct {
	a, b   *Controller
	ab, ba *wire
	router *fakeUpstream
}

func newPair(t *testing.T) *pair {
	t.Helper()
	p := &pair{ab: &wire{}, ba: &wire{}, router: &fakeUpstream{address: "127.0.0.1"}}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	makeNode := func(id, peer, ip, peerip string, w *wire) *Controller {
		cfg := config.Default()
		cfg.Node.ID = id
		cfg.Cluster = &config.ClusterConfig{ID: "dmz", PeerID: peer, PeerURL: "http://peer", PeerToken: strings.Repeat("test-token", 4), InitialWriter: "a", Address: ip, PeerAddress: peerip, RouterURL: "http://router/ubus", Username: "test", Password: "test-only", Redirects: []string{"dmz"}, PollInterval: 2, FailoverAfter: 10, AllowHTTPPeer: true}
		cfg.Forward = nil
		if id == "a" {
			cfg.Forward = []config.Rule{{ID: "web", Type: "tcp", Status: "active", LocalHost: "0.0.0.0", LocalPort: port, TargetHost: "127.0.0.1", TargetPort: 3000}}
		}
		cfg.Node.RuleOverrides = map[config.RuleID]config.RuleOverride{"web": {LocalHost: &ip}}
		path := filepath.Join(t.TempDir(), "config.json")
		raw, _ := json.Marshal(cfg)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		store, _, err := config.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		mgr := gateway.New(store, events.NewBroker())
		mgr.Start()
		c, err := NewController(store, mgr, p.router, w)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { mgr.Stop(); c.Close() })
		return c
	}
	p.a = makeNode("a", "b", "127.0.0.1", "127.0.0.2", p.ab)
	p.b = makeNode("b", "a", "127.0.0.2", "127.0.0.1", p.ba)
	p.ab.target = p.b
	p.ba.target = p.a
	return p
}
func bootstrap(t *testing.T, p *pair) {
	t.Helper()
	if err := p.a.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := p.b.SyncStep(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestPairReplicationLostAcknowledgmentAndCrashRecovery(t *testing.T) {
	p := newPair(t)
	bootstrap(t, p)
	first := p.a.replica.state().Committed
	r := first.Rules[0]
	r.TargetPort = 4000
	p.ab.loseAfter = "commit"
	if _, err := p.a.mgr.Update("web", r); err == nil {
		t.Fatal("lost ACK reported success")
	} else {
		var pending *PendingError
		if !errors.As(err, &pending) {
			t.Fatalf("missing pending state: %v", err)
		}
	}
	if p.a.store.Rules().Snapshot().Checksum != first.Checksum {
		t.Fatal("unconfirmed change applied on writer")
	}
	if p.b.replica.state().Committed.Rules[0].TargetPort != 4000 {
		t.Fatal("peer did not persist before ACK")
	}
	// Reopen the actual durable record, preserving a pending commit decision.
	reloaded, err := OpenReplica(p.a.cfg, p.a.node, p.a.replica.path, first, p.ab)
	if err != nil {
		t.Fatal(err)
	}
	if err = reloaded.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reloaded.state().Pending != nil || reloaded.state().Committed.Checksum != p.b.replica.state().Committed.Checksum {
		t.Fatal("recovery did not converge")
	}
	if reloaded.state().Committed.Revision != first.Revision+1 {
		t.Fatal("retry created another revision")
	}
	if p.b.store.Get().Node.RuleOverrides["web"].LocalHost == nil || *p.b.store.Get().Node.RuleOverrides["web"].LocalHost != "127.0.0.2" {
		t.Fatal("private binding was overwritten")
	}
}
func TestPrepareIsNotCommittedAndFailoverRejectsDelayedMessages(t *testing.T) {
	p := newPair(t)
	bootstrap(t, p)
	old := p.b.replica.state().Committed
	r := old.Rules[0]
	r.TargetPort = 4444
	p.ab.loseAfter = "prepare"
	_, err := p.a.mgr.Update("web", r)
	if err == nil {
		t.Fatal("prepare ACK loss accepted")
	}
	tx := *p.a.replica.state().Pending
	if p.b.replica.state().Committed.Checksum != old.Checksum {
		t.Fatal("prepared state was committed")
	}
	if err = p.b.replica.FreezeTakeover(); err != nil {
		t.Fatal(err)
	}
	if err = p.b.replica.CommitPeer(tx.PairID, tx.ID); !errors.Is(err, ErrFrozen) {
		t.Fatalf("delayed commit accepted: %v", err)
	}
	if err = p.b.replica.Prepare(tx); !errors.Is(err, ErrFrozen) {
		t.Fatalf("delayed prepare accepted: %v", err)
	}
	if err = p.a.replica.ObserveTakeover(p.b.peerState()); err != nil {
		t.Fatal(err)
	}
	if p.a.replica.state().Pending != nil || !p.a.replica.state().Frozen {
		t.Fatal("old writer did not archive the abandoned request and freeze")
	}
	if err = p.a.replica.Transfer(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.b.replica.state().Writer != "b" || p.b.replica.state().Frozen {
		t.Fatal("controlled writer transfer failed")
	}
}
func TestAutomaticFailoverNonpreemptionAndBadPeerCredentials(t *testing.T) {
	p := newPair(t)
	bootstrap(t, p)
	ctx := context.Background()
	if err := p.a.TrafficStep(ctx); err != nil {
		t.Fatal(err)
	}
	if !p.a.mgr.Serving() {
		t.Fatal("A not active")
	}
	p.ba.fault = true
	_ = p.b.SyncStep(ctx)
	p.b.offlineSince = time.Now().Add(-time.Minute)
	if err := p.b.TrafficStep(ctx); err != nil {
		t.Fatal(err)
	}
	if p.router.count != 0 {
		t.Fatal("authentication failure triggered takeover")
	}
	p.ba.fault = false
	p.ba.offline = true
	_ = p.b.SyncStep(ctx)
	p.b.offlineSince = time.Now().Add(-time.Minute)
	if err := p.b.TrafficStep(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.b.TrafficStep(ctx); err != nil {
		t.Fatal(err)
	}
	if p.router.address != "127.0.0.2" || !p.b.mgr.Serving() {
		t.Fatal("B did not take over")
	}
	if err := p.a.TrafficStep(ctx); err != nil {
		t.Fatal(err)
	}
	if p.a.mgr.Serving() || p.router.count != 1 {
		t.Fatal("old primary preempted or continued forwarding")
	}
	changed := p.b.replica.state().Committed.Rules[0]
	changed.TargetPort++
	if _, err := p.b.mgr.Update("web", changed); !errors.Is(err, config.ErrClusterUnavailable) {
		t.Fatalf("backup writable without promotion: %v", err)
	}
}
func TestRouterFailureDoesNotBlockRuleReplication(t *testing.T) {
	p := newPair(t)
	bootstrap(t, p)
	p.router.err = errors.New("router offline")
	if err := p.a.TrafficStep(context.Background()); err == nil {
		t.Fatal("router error missing")
	}
	r := p.a.replica.state().Committed.Rules[0]
	r.TargetPort = 4567
	if _, err := p.a.mgr.Update("web", r); err != nil {
		t.Fatal(err)
	}
	if p.b.replica.state().Committed.Rules[0].TargetPort != 4567 {
		t.Fatal("rules depended on router")
	}
}
func TestPromotionRejoinAndRejectDivergentEpoch(t *testing.T) {
	p := newPair(t)
	bootstrap(t, p)
	old := p.a.replica.state().Committed
	if err := p.b.replica.Promote(false); err == nil {
		t.Fatal("promotion lacked fencing confirmation")
	}
	if err := p.b.replica.Promote(true); err != nil {
		t.Fatal(err)
	}
	if err := p.b.materialize(); err != nil {
		t.Fatal(err)
	}
	r := old.Rules[0]
	r.TargetPort = 6789
	if _, err := p.b.mgr.Update("web", r); err != nil {
		t.Fatal(err)
	}
	tx := Transaction{ID: nonce(), PairID: p.a.replica.state().PairID, Kind: "rules", Expected: old.Checksum, Next: config.WithWriter(old, 1, "a", old.Rules)}
	if err := p.b.replica.Prepare(tx); err == nil {
		t.Fatal("old writer accepted after promotion")
	}
	if err := p.a.replica.Rejoin(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := p.a.SyncStep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.b.replica.state().Degraded {
		t.Fatal("matching rejoined node did not restore replicated mode")
	}
	if p.a.replica.state().Committed.Rules[0].TargetPort != 6789 {
		t.Fatal("rejoin lost latest rule")
	}
}
func TestPeerHTTPAuthenticationIdentityAndCompactHeartbeat(t *testing.T) {
	p := newPair(t)
	bootstrap(t, p)
	srv := httptest.NewServer(p.b.PeerHandler(func(context.Context, Mutation) Reply { return Reply{Status: 200} }))
	defer srv.Close()
	cfg := p.a.cfg
	cfg.PeerURL = srv.URL
	client, err := NewPeerClient(cfg, "a")
	if err != nil {
		t.Fatal(err)
	}
	out, err := client.Call(context.Background(), "heartbeat", PeerRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Snapshot != nil || out.State == nil || out.State.Checksum == "" {
		t.Fatal("heartbeat did not return a compact summary")
	}
	client.cfg.PeerToken = "bad"
	if _, err := client.Call(context.Background(), "heartbeat", PeerRequest{}); err == nil {
		t.Fatal("bad credentials accepted")
	}
	client.cfg.PeerToken = cfg.PeerToken
	client.node = "impostor"
	if _, err := client.Call(context.Background(), "heartbeat", PeerRequest{}); err == nil {
		t.Fatal("wrong identity accepted")
	}
	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("peer endpoint is public")
	}
}
func TestCorruptJournalAndDuplicateProcessRefused(t *testing.T) {
	p := newPair(t)
	bootstrap(t, p)
	if _, err := NewController(p.a.store, p.a.mgr, p.router, p.ab); err == nil {
		t.Fatal("duplicate writer process acquired journal")
	}
	if err := os.WriteFile(p.a.replica.path, []byte(`{"checksum":"wrong","state":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReplica(p.a.cfg, "a", p.a.replica.path, p.a.replica.state().Committed, p.ab); err == nil {
		t.Fatal("corrupt journal accepted")
	}
}
func TestReplicaIdempotencyAndConflictingPrepare(t *testing.T) {
	p := newPair(t)
	bootstrap(t, p)
	before := p.a.replica.state().Committed
	id := "test-request-12345"
	if _, err := p.a.replica.BeginEdit(id, "request-hash", before.Checksum); err != nil {
		t.Fatal(err)
	}
	r := before.Rules[0]
	r.TargetPort = 4444
	if _, err := p.a.mgr.Update("web", r); err != nil {
		t.Fatal(err)
	}
	reply := Reply{Status: 200, Body: json.RawMessage(`{"saved":true}`)}
	if err := p.a.replica.EndEdit(id, "request-hash", reply); err != nil {
		t.Fatal(err)
	}
	cached, err := p.a.replica.BeginEdit(id, "request-hash", before.Checksum)
	if err != nil || cached == nil || cached.Status != 200 {
		t.Fatal("retry was not idempotent")
	}
	if _, err = p.a.replica.BeginEdit(id, "other-content", before.Checksum); !errors.Is(err, config.ErrRevisionConflict) {
		t.Fatal("ID reuse accepted")
	}
	tx := Transaction{ID: nonce(), PairID: p.a.replica.state().PairID, Kind: "rules", Expected: before.Checksum, Next: config.WithWriter(before, 1, "a", before.Rules)}
	if err = p.b.replica.Prepare(tx); !errors.Is(err, config.ErrRevisionConflict) {
		t.Fatalf("stale parent accepted: %v", err)
	}
}

func TestWriterHandoffRecoversLostAcknowledgements(t *testing.T) {
	for _, method := range []string{"handoff-prepare", "handoff-commit"} {
		t.Run(method, func(t *testing.T) {
			p := newPair(t)
			bootstrap(t, p)
			p.ab.loseAfter = method
			if err := p.a.replica.Transfer(context.Background()); err == nil {
				t.Fatal("lost handoff response reported success")
			}
			if _, err := p.a.replica.BeginEdit("interrupted-handoff", "hash", p.a.Status().Checksum); !errors.Is(err, config.ErrClusterUnavailable) {
				t.Fatal("old writer remained writable during handoff")
			}
			if err := p.a.replica.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			a, b := p.a.replica.state(), p.b.replica.state()
			if a.Writer != "b" || b.Writer != "b" || a.Epoch != 2 || b.Epoch != 2 || a.Transfer != nil || b.Transfer != nil || a.Committed.Checksum != b.Committed.Checksum {
				t.Fatal("handoff did not converge")
			}
		})
	}
}

func TestRearmRequiresRestoredReplicationAndManualFailback(t *testing.T) {
	p := newPair(t)
	bootstrap(t, p)
	ctx := context.Background()
	if err := p.b.replica.FreezeTakeover(); err != nil {
		t.Fatal(err)
	}
	if err := p.b.replica.MarkSwitched(); err != nil {
		t.Fatal(err)
	}
	p.router.address = "127.0.0.2"
	if err := p.b.Rearm(ctx); err == nil {
		t.Fatal("rearmed while B still owns ingress")
	}
	if err := p.a.SyncStep(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.a.replica.Transfer(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.b.SyncStep(ctx); err != nil {
		t.Fatal(err)
	}
	p.router.address = "127.0.0.1"
	if err := p.a.TrafficStep(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.b.Rearm(ctx); err != nil {
		t.Fatal(err)
	}
	if p.b.replica.state().Switched || p.b.replica.state().Takeover {
		t.Fatal("sticky takeover was not reset")
	}
	p.ba.offline = true
	_ = p.b.SyncStep(ctx)
	p.b.offlineSince = time.Now().Add(-time.Minute)
	if err := p.b.TrafficStep(ctx); err != nil {
		t.Fatal(err)
	}
	if p.router.address != "127.0.0.2" {
		t.Fatal("second failover did not work")
	}
}
