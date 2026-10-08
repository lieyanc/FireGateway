package ha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lieyanc/FireGateway/internal/config"
	"github.com/lieyanc/FireGateway/internal/gateway"
)

type Status struct {
	PeerEpoch             int64      `json:"peerEpoch"`
	TakenOver             bool       `json:"takenOver"`
	PeerTakenOver         bool       `json:"peerTakenOver"`
	InitialWriter         string     `json:"initialWriter"`
	Enabled               bool       `json:"enabled"`
	NodeID                string     `json:"nodeId"`
	ClusterID             string     `json:"clusterId"`
	Role                  string     `json:"role"`
	Owner                 string     `json:"owner"`
	Address               string     `json:"address"`
	DesiredRevision       int64      `json:"desiredRevision"`
	AppliedRevision       int64      `json:"appliedRevision"`
	Checksum              string     `json:"checksum"`
	Epoch                 int64      `json:"epoch"`
	Paired                bool       `json:"paired"`
	ConfigRole            string     `json:"configRole"`
	Writer                string     `json:"writer"`
	ReplicationState      string     `json:"replicationState"`
	PeerState             string     `json:"peerState"`
	PeerRevision          int64      `json:"peerRevision"`
	PeerChecksum          string     `json:"peerChecksum"`
	PeerPreparedChecksum  string     `json:"peerPreparedChecksum"`
	LocalPreparedChecksum string     `json:"localPreparedChecksum"`
	PendingUpdateID       string     `json:"pendingUpdateId,omitempty"`
	UpstreamState         string     `json:"upstreamState"`
	Error                 string     `json:"error,omitempty"`
	SyncError             string     `json:"syncError,omitempty"`
	LastSync              *time.Time `json:"lastSync,omitempty"`
}
type Controller struct {
	release   func()
	closeOnce sync.Once
	store     *config.Store
	mgr       *gateway.Manager
	cfg       config.ClusterConfig
	node      string
	replica   *Replica
	peer      PeerAPI
	upstream  Upstream
	usage     UsageExchange
	now       func() time.Time
	server    *http.Server
	mutator   atomic.Pointer[func(context.Context, Mutation) Reply]
	kick      chan struct{}

	// Lock order: traffic, writes, apply, mu. Only writes is ever held across
	// a peer call, and the ingress path never takes it, so a slow or absent
	// peer cannot delay the decision who forwards traffic.
	writes  sync.Mutex // serializes replicated writes and recovery
	apply   sync.Mutex // serializes installing rules and binding listeners
	traffic sync.Mutex // serializes ingress observation and router switches

	mu             sync.RWMutex
	status         Status
	peerInfo       *PeerState
	offlineSince   time.Time // peer unreachable since
	unhealthySince time.Time // peer reachable but unable to forward since
	peerFault      bool
	prepared       string    // committed checksum this node's listeners are ready for
	readyErr       string    // why this node is not ready
	ingressErr     string    // last router observation or switch failure
	switchHold     time.Time // an uncertain switch may still roll back until then
}

// UsageExchange shares per-tenant traffic counts with the peer so quotas
// cover both nodes.
type UsageExchange interface {
	Local() map[string]config.TenantUsage
	SetPeer(map[string]config.TenantUsage)
}

// SetUsage installs the usage exchange; call it before Run.
func (c *Controller) SetUsage(u UsageExchange) { c.usage = u }

func NewController(store *config.Store, mgr *gateway.Manager, upstream Upstream, peer PeerAPI) (*Controller, error) {
	cfg := *store.Get().Cluster
	node := store.Get().Node.ID
	release, err := lockJournal(store.Rules().Path() + ".ha.json.lock")
	if err != nil {
		return nil, err
	}
	r, err := OpenReplica(cfg, node, store.Rules().Path()+".ha.json", store.Rules().Snapshot(), peer)
	if err != nil {
		release()
		return nil, err
	}
	c := &Controller{release: release, store: store, mgr: mgr, cfg: cfg, node: node, replica: r, peer: peer, upstream: upstream, now: time.Now, kick: make(chan struct{}, 1), status: Status{Enabled: true, NodeID: node, ClusterID: cfg.ID, InitialWriter: cfg.InitialWriter, PeerState: "unknown", UpstreamState: "unknown"}}
	// The durable journal is the only authority after bootstrap, never an
	// arbitrary rules.json that was edited while the process was stopped.
	if err = store.Rules().Restore(r.state().Committed); err != nil {
		release()
		return nil, err
	}
	store.Rules().SetBackend(r)
	return c, nil
}

// wake asks the sync loop to run now, e.g. after the peer committed a change.
func (c *Controller) wake() {
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// ready reports whether this node can forward the committed rules d.
func (c *Controller) ready(d replicaDisk) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.prepared != "" && c.prepared == d.Committed.Checksum && c.store.Rules().Snapshot().Checksum == d.Committed.Checksum
}

func (c *Controller) peerState() PeerState {
	d := c.replica.state()
	prepared := ""
	if c.ready(d) {
		prepared = d.Committed.Checksum
	}
	out := PeerState{NodeID: c.node, PairID: d.PairID, Paired: d.Paired, Writer: d.Writer, Epoch: d.Epoch, Revision: d.Committed.Revision, Checksum: d.Committed.Checksum, PreparedChecksum: prepared, Frozen: d.Frozen, Takeover: d.Takeover, Degraded: d.Degraded, Transferring: d.Transfer != nil}
	if d.Pending != nil {
		out.PendingID = d.Pending.ID
	}
	if c.usage != nil {
		out.Usage = c.usage.Local()
	}
	return out
}
func (c *Controller) Status() Status {
	c.mu.RLock()
	out := c.status
	peer := clone(c.peerInfo)
	out.Error = strings.Join(nonEmpty(c.ingressErr, c.readyErr), "; ")
	c.mu.RUnlock()
	d := c.replica.state()
	ready := c.ready(d)
	out.Paired = d.Paired
	out.TakenOver = d.Takeover || d.Switched
	out.Writer = d.Writer
	out.Epoch = d.Epoch
	out.DesiredRevision = d.Committed.Revision
	out.Checksum = d.Committed.Checksum
	out.AppliedRevision = c.store.Rules().Snapshot().Revision
	out.ConfigRole = "replica"
	if d.Writer == c.node {
		out.ConfigRole = "writer"
	}
	if d.Frozen || d.Transfer != nil || (!d.Degraded && out.PeerState != "online") {
		out.ConfigRole = "read_only"
	}
	out.ReplicationState = "waiting"
	if d.Degraded {
		out.ReplicationState = "local_only"
	} else if d.Pending != nil {
		out.ReplicationState = "pending"
		out.PendingUpdateID = d.Pending.ID
	} else if peer != nil && out.PeerState == "online" && peer.PairID == d.PairID && peer.Checksum == d.Committed.Checksum {
		out.ReplicationState = "synced"
	}
	if !d.Paired {
		out.ReplicationState = "unpaired"
	}
	if peer != nil {
		out.PeerRevision = peer.Revision
		out.PeerEpoch = peer.Epoch
		out.PeerChecksum = peer.Checksum
		out.PeerPreparedChecksum = peer.PreparedChecksum
		out.PeerTakenOver = peer.Takeover
	}
	if ready {
		out.LocalPreparedChecksum = d.Committed.Checksum
	}
	switch {
	case c.mgr.Serving():
		out.Role = "active"
	case ready:
		out.Role = "standby"
	default:
		out.Role = "disconnected"
	}
	if d.Takeover && !d.Switched && out.UpstreamState != "unavailable" {
		out.UpstreamState = "switch_pending"
	}
	return out
}
func nonEmpty(values ...string) []string {
	out := values[:0]
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// materialize installs the committed snapshot as this node's rule set.
// Callers hold c.apply.
func (c *Controller) materialize() error {
	d := c.replica.state()
	if c.store.Rules().Snapshot().Checksum != d.Committed.Checksum {
		return c.mgr.RestoreRules(d.Committed)
	}
	return nil
}

// refresh installs the committed rules and binds their listeners behind the
// closed gate. The node is ready when the router's destination reaches every
// active rule and all of them bound; a rule that cannot bind is reported but
// never stops the others.
func (c *Controller) refresh() error {
	c.apply.Lock()
	defer c.apply.Unlock()
	d := c.replica.state()
	err := c.materialize()
	if err == nil && !d.Paired {
		err = errors.New("pair the nodes before enabling ingress")
	}
	if err == nil {
		err = c.mgr.ValidateIngress(c.cfg.Address, d.Committed.Rules)
	}
	if err == nil {
		err = c.mgr.Prepare()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prepared, c.readyErr = d.Committed.Checksum, ""
	if err != nil {
		c.prepared, c.readyErr = "", err.Error()
	}
	return err
}
func (c *Controller) Bootstrap() error {
	c.writes.Lock()
	defer c.writes.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), PeerTimeout)
	defer cancel()
	if err := c.replica.Bootstrap(ctx); err != nil {
		return err
	}
	return c.refresh()
}
func (c *Controller) Action(ctx context.Context, action string, confirmed bool) error {
	if action == "failback" {
		return c.Failback(ctx)
	}
	c.writes.Lock()
	defer c.writes.Unlock()
	ctx, cancel := context.WithTimeout(ctx, PeerTimeout)
	defer cancel()
	var err error
	switch action {
	case "transfer":
		err = c.replica.Transfer(ctx)
	case "promote":
		err = c.replica.Promote(confirmed)
	case "rejoin":
		err = c.replica.Rejoin(ctx, confirmed)
	default:
		return errors.New("unknown cluster action")
	}
	if err != nil {
		return err
	}
	return c.refresh()
}
func errorReply(err error) Reply {
	code, status := "unavailable", 503
	var pending *PendingError
	if errors.As(err, &pending) {
		code = "sync_pending"
	} else if errors.Is(err, config.ErrRevisionConflict) {
		code = "conflict"
		status = 409
	}
	fields := map[string]string{"error": code, "message": err.Error()}
	if pending != nil {
		fields["updateId"] = pending.ID
	}
	body, _ := json.Marshal(fields)
	return Reply{Status: status, Body: body}
}

// Executor applies replicated management mutations on the writer.
type Executor interface {
	// Version returns the caller-visible version a request must match, or ""
	// when the mutation carries no precondition.
	Version(req Mutation, committed config.RuleSet) string
	Execute(req Mutation) Reply
}

func (c *Controller) Mutate(ctx context.Context, req Mutation, exec Executor) Reply {
	if len(req.ID) < 8 || len(req.ID) > 128 || strings.ContainsAny(req.ID, "\r\n") {
		return errorReply(fmt.Errorf("Idempotency-Key is required for cluster writes: %w", config.ErrRevisionConflict))
	}
	d := c.replica.state()
	if d.Writer != c.node {
		if d.Frozen || !d.Paired {
			return errorReply(ErrFrozen)
		}
		out, err := c.peer.Call(ctx, "mutate", PeerRequest{Mutation: &req})
		if err != nil {
			return errorReply(err)
		}
		if out.Reply == nil {
			return errorReply(errors.New("peer omitted mutation result"))
		}
		// The writer replies after the commit reached this node; apply it now
		// so the caller reads its own write here.
		_ = c.refresh()
		return *out.Reply
	}
	c.writes.Lock()
	defer c.writes.Unlock()
	c.apply.Lock()
	err := c.materialize()
	c.apply.Unlock()
	if err != nil {
		return errorReply(err)
	}
	raw, _ := json.Marshal(struct {
		Method, Path, Expected, Actor string
		Body                          json.RawMessage
	}{req.Method, req.Path, req.Expected, req.Actor, req.Body})
	hash := digest(raw)
	cached, err := c.replica.BeginEdit(req.ID, hash, func(committed config.RuleSet) error {
		if v := exec.Version(req, committed); v != "" && v != req.Expected {
			if req.Expected == "" {
				return fmt.Errorf("If-Match is required for rule writes: %w", config.ErrRevisionConflict)
			}
			return config.ErrRevisionConflict
		}
		return nil
	})
	if err != nil {
		return errorReply(err)
	}
	if cached != nil {
		return *cached
	}
	c.mu.RLock()
	info := clone(c.peerInfo)
	online := c.status.PeerState == "online"
	c.mu.RUnlock()
	current := c.replica.state()
	if !current.Degraded && (!online || info == nil || info.PairID != current.PairID || info.Epoch != current.Epoch || info.Writer != current.Writer) {
		reply := errorReply(ErrFrozen)
		_ = c.replica.EndEdit(req.ID, hash, reply)
		return reply
	}
	reply := exec.Execute(req)
	_ = c.refresh()
	d = c.replica.state()
	reply.Version = exec.Version(req, d.Committed)
	reply.Durability = "replicated"
	if d.Pending != nil {
		reply.Durability = "pending"
	}
	if d.Degraded {
		reply.Durability = "local_only"
	}
	if err = c.replica.EndEdit(req.ID, hash, reply); err != nil {
		return errorReply(&PendingError{req.ID, err})
	}
	return reply
}
func (c *Controller) Transaction(id string) (any, bool) {
	d := c.replica.state()
	if d.Pending != nil && (d.Pending.ID == id || d.Pending.RequestID == id) {
		return d.Pending, true
	}
	if v, ok := d.Requests[id]; ok {
		return v, true
	}
	return nil, false
}

// SyncStep exchanges state with the peer, finishes interrupted replication
// and keeps the local listeners ready for the committed rules.
func (c *Controller) SyncStep(ctx context.Context) error {
	peer, err := c.heartbeat(ctx)
	c.writes.Lock()
	defer c.writes.Unlock()
	errs := []error{err}
	if peer != nil && peer.Frozen && peer.Takeover {
		errs = append(errs, c.replica.ObserveTakeover(*peer))
	}
	recoverCtx, done := context.WithTimeout(ctx, PeerTimeout)
	defer done()
	errs = append(errs, c.replica.Recover(recoverCtx))
	d := c.replica.state()
	if peer != nil && peer.Degraded && d.Writer == c.cfg.PeerID && peer.PairID == d.PairID && peer.Checksum == d.Committed.Checksum {
		_, e := c.peer.Call(recoverCtx, "resume", PeerRequest{PairID: d.PairID, Checksum: d.Committed.Checksum, Epoch: d.Epoch})
		errs = append(errs, e)
	}
	err = errors.Join(errs...)
	if err != nil {
		c.mu.Lock()
		c.status.SyncError = err.Error()
		c.mu.Unlock()
	}
	// Readiness errors are reported separately; they are not sync failures.
	_ = c.refresh()
	return err
}

// heartbeat records the peer's reachability and health. Errors that prove
// the peer is up but misconfigured never count towards takeover.
func (c *Controller) heartbeat(ctx context.Context) (*PeerState, error) {
	probe, cancel := context.WithTimeout(ctx, 3*time.Second)
	out, err := c.peer.Call(probe, "heartbeat", PeerRequest{})
	cancel()
	if err == nil && out.State == nil {
		err = &PeerFault{"peer omitted heartbeat"}
	}
	d := c.replica.state()
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.peerFault = isFault(err)
		c.unhealthySince = time.Time{}
		if c.peerFault {
			c.offlineSince = time.Time{}
			c.status.PeerState = "error"
		} else {
			if c.offlineSince.IsZero() {
				c.offlineSince = now
			}
			c.status.PeerState = "offline"
		}
		c.status.SyncError = err.Error()
		return nil, err
	}
	st := out.State
	c.peerInfo = st
	if c.usage != nil {
		c.usage.SetPeer(st.Usage)
	}
	c.offlineSince = time.Time{}
	c.peerFault = false
	// The peer is unhealthy when it holds our committed rules but cannot
	// forward them. Differing checksums mean replication is mid-flight.
	if st.PairID == d.PairID && st.Checksum == d.Committed.Checksum && st.PreparedChecksum != st.Checksum {
		if c.unhealthySince.IsZero() {
			c.unhealthySince = now
		}
	} else {
		c.unhealthySince = time.Time{}
	}
	c.status.PeerState = "online"
	c.status.SyncError = ""
	c.status.LastSync = &now
	return st, nil
}
func isFault(err error) bool {
	var fault *PeerFault
	return errors.As(err, &fault) || errors.Is(err, config.ErrRevisionConflict) || errors.Is(err, config.ErrClusterUnavailable)
}

// TrafficStep follows the router. Its DNAT destination is the only arbiter of
// who forwards: the gate opens exactly while the router points here. When
// the router cannot be read the last observed role stands, so a router API
// outage never interrupts forwarding.
func (c *Controller) TrafficStep(ctx context.Context) error {
	c.traffic.Lock()
	defer c.traffic.Unlock()
	readCtx, cancel := context.WithTimeout(ctx, RouterTimeout)
	in, err := c.upstream.Read(readCtx)
	cancel()
	if err != nil {
		c.mu.Lock()
		c.status.UpstreamState = "unavailable"
		c.ingressErr = err.Error()
		c.mu.Unlock()
		return err
	}
	d := c.replica.state()
	mine := in.Address == c.cfg.Address
	c.mgr.SetServing(mine && d.Paired)
	c.mu.Lock()
	c.status.Address = in.Address
	c.status.Owner = ""
	switch in.Address {
	case c.cfg.Address:
		c.status.Owner = c.node
	case c.cfg.PeerAddress:
		c.status.Owner = c.cfg.PeerID
	}
	c.status.UpstreamState = "observed"
	c.mu.Unlock()
	switch {
	case d.Takeover && !d.Switched:
		err = c.converge(ctx, in, mine)
	case !mine && c.ready(d) && c.takeoverDue(ctx, d):
		err = c.takeover(ctx, in)
	}
	c.mu.Lock()
	c.ingressErr = ""
	if err != nil {
		c.ingressErr = err.Error()
	}
	c.mu.Unlock()
	return err
}

// takeoverDue decides whether this node should claim ingress from its peer.
// Only the designated backup takes over, once until failback, so two nodes
// that cannot see each other never pass ingress back and forth.
func (c *Controller) takeoverDue(ctx context.Context, d replicaDisk) bool {
	if c.node == c.cfg.InitialWriter || d.Switched || !d.Paired {
		return false
	}
	c.mu.RLock()
	offline, unhealthy, fault := c.offlineSince, c.unhealthySince, c.peerFault
	c.mu.RUnlock()
	wait := time.Duration(c.cfg.FailoverAfter) * time.Second
	now := c.now()
	switch {
	case fault:
		return false
	case !offline.IsZero() && now.Sub(offline) >= wait:
		// Repeat a direct probe right before acting on an aged observation.
		probe, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		_, err := c.peer.Call(probe, "heartbeat", PeerRequest{})
		return err != nil && !isFault(err)
	case !unhealthy.IsZero() && now.Sub(unhealthy) >= wait:
		return true
	}
	return false
}

// takeover fences the peer's configuration writes and points the router here.
func (c *Controller) takeover(ctx context.Context, in Ingress) error {
	if err := c.replica.FreezeTakeover(); err != nil {
		return err
	}
	// Prepare again after the freeze: a commit may have landed since the last
	// readiness check, and the router must never point at unbound listeners.
	if err := c.refresh(); err != nil {
		return err
	}
	switchCtx, cancel := context.WithTimeout(ctx, RouterTimeout)
	err := c.upstream.Switch(switchCtx, in, c.cfg.Address)
	cancel()
	if errors.Is(err, ErrSwitchUncertain) {
		c.mu.Lock()
		c.switchHold = c.now().Add(RollbackWindow)
		c.mu.Unlock()
	}
	if err != nil {
		return err
	}
	if err = c.replica.MarkSwitched(); err != nil {
		return err
	}
	c.mgr.SetServing(true)
	c.mu.Lock()
	c.status.Owner, c.status.Address = c.node, c.cfg.Address
	c.mu.Unlock()
	return nil
}

// converge settles a takeover whose switch was not confirmed. The router
// decides: pointing here past OpenWrt's rollback window, the switch is
// final; pointing at the peer, it is retried while takeover is still due.
// A peer that came back healthy keeps ingress until the operator fails back.
func (c *Controller) converge(ctx context.Context, in Ingress, mine bool) error {
	now := c.now()
	c.mu.Lock()
	if mine && c.switchHold.IsZero() {
		// After a restart the hold restarts at the first observation.
		c.switchHold = now.Add(RollbackWindow)
	}
	hold := c.switchHold
	c.mu.Unlock()
	if now.Before(hold) {
		return nil
	}
	if mine {
		c.mu.Lock()
		c.switchHold = time.Time{}
		c.mu.Unlock()
		return c.replica.MarkSwitched()
	}
	if !c.takeoverDue(ctx, c.replica.state()) {
		return nil
	}
	return c.takeover(ctx, in)
}

// Failback returns ingress to the initial primary after a takeover and
// re-arms automatic takeover on the backup. It runs on the initial primary,
// once both nodes are back in sync under one configuration writer.
func (c *Controller) Failback(ctx context.Context) error {
	if c.node != c.cfg.InitialWriter {
		return fmt.Errorf("fail back from the initial primary %s", c.cfg.InitialWriter)
	}
	c.traffic.Lock()
	defer c.traffic.Unlock()
	probe, cancel := context.WithTimeout(ctx, PeerTimeout)
	out, err := c.peer.Call(probe, "heartbeat", PeerRequest{})
	cancel()
	if err != nil {
		return fmt.Errorf("the peer must be online to fail back: %w", err)
	}
	d, st := c.replica.state(), out.State
	if st == nil || d.Frozen || d.Pending != nil || d.Transfer != nil || d.Degraded || st.Frozen || st.PendingID != "" || st.Transferring || st.Degraded || st.PairID != d.PairID || st.Epoch != d.Epoch || st.Checksum != d.Committed.Checksum {
		return fmt.Errorf("transfer the configuration writer and let both nodes sync before failing back: %w", ErrFrozen)
	}
	if err = c.refresh(); err != nil {
		return fmt.Errorf("this node is not ready to forward: %w", err)
	}
	readCtx, cancel := context.WithTimeout(ctx, RouterTimeout)
	in, err := c.upstream.Read(readCtx)
	if err == nil && in.Address != c.cfg.Address {
		err = c.upstream.Switch(readCtx, in, c.cfg.Address)
	}
	cancel()
	if err != nil {
		return err
	}
	c.mgr.SetServing(true)
	c.mu.Lock()
	c.status.Owner, c.status.Address, c.ingressErr = c.node, c.cfg.Address, ""
	c.mu.Unlock()
	if !st.Takeover {
		return nil
	}
	rearmCtx, cancel := context.WithTimeout(ctx, PeerTimeout)
	defer cancel()
	_, err = c.peer.Call(rearmCtx, "rearm", PeerRequest{PairID: d.PairID, Epoch: d.Epoch})
	return err
}

// rearm runs on the backup when the initial primary has failed back.
func (c *Controller) rearm(pair string, epoch int64) error {
	d := c.replica.state()
	if pair != d.PairID || epoch != d.Epoch {
		return config.ErrRevisionConflict
	}
	if err := c.replica.Rearm(); err != nil {
		return err
	}
	c.mu.Lock()
	c.switchHold = time.Time{}
	c.mu.Unlock()
	return nil
}

func (c *Controller) Close() {
	c.closeOnce.Do(func() {
		if c.server != nil {
			_ = c.server.Close()
		}
		c.release()
	})
}

func (c *Controller) Run(ctx context.Context) {
	defer c.Close()
	interval := time.Duration(c.cfg.PollInterval) * time.Second
	var wg sync.WaitGroup
	loop := func(fn func(context.Context) error, kick <-chan struct{}) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tick := time.NewTicker(interval)
			defer tick.Stop()
			for {
				_ = fn(ctx)
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
				case <-kick:
				}
			}
		}()
	}
	loop(c.SyncStep, c.kick)
	loop(c.TrafficStep, nil)
	<-ctx.Done()
	c.mgr.SetServing(false)
	wg.Wait()
}

func (c *Controller) Snapshot() config.RuleSet { return c.replica.state().Committed }
