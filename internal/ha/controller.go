package ha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lieyanc/FireGateway/internal/config"
	"github.com/lieyanc/FireGateway/internal/gateway"
)

type Status struct {
	PeerEpoch             int64      `json:"peerEpoch"`
	TakenOver             bool       `json:"takenOver"`
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
	release      func()
	closeOnce    sync.Once
	store        *config.Store
	mgr          *gateway.Manager
	cfg          config.ClusterConfig
	node         string
	replica      *Replica
	peer         PeerAPI
	upstream     Upstream
	edit         sync.Mutex
	traffic      sync.Mutex
	mu           sync.RWMutex
	status       Status
	peerInfo     *PeerState
	offlineSince time.Time
	peerFault    bool
	prepared     string
	now          func() time.Time
}

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
	c := &Controller{release: release, store: store, mgr: mgr, cfg: cfg, node: node, replica: r, peer: peer, upstream: upstream, now: time.Now, status: Status{Enabled: true, NodeID: node, ClusterID: cfg.ID, Role: "disconnected", PeerState: "unknown", UpstreamState: "unknown"}}
	// The durable journal is the only authority after bootstrap, never an
	// arbitrary rules.json that was edited while the process was stopped.
	if err = store.Rules().Restore(r.state().Committed); err != nil {
		release()
		return nil, err
	}
	store.Rules().SetBackend(r)
	return c, nil
}
func (c *Controller) peerState() PeerState {
	d := c.replica.state()
	c.mu.RLock()
	prepared := c.prepared
	c.mu.RUnlock()
	if c.mgr.AppliedRevision() != d.Committed.Revision || c.store.Rules().Snapshot().Checksum != d.Committed.Checksum {
		prepared = ""
	}
	out := PeerState{NodeID: c.node, PairID: d.PairID, Paired: d.Paired, Writer: d.Writer, Epoch: d.Epoch, Revision: d.Committed.Revision, Checksum: d.Committed.Checksum, PreparedChecksum: prepared, Frozen: d.Frozen, Takeover: d.Takeover, Degraded: d.Degraded, Transferring: d.Transfer != nil}
	if d.Pending != nil {
		out.PendingID = d.Pending.ID
	}
	return out
}
func (c *Controller) Status() Status {
	c.mu.RLock()
	out := c.status
	prepared := c.prepared
	peer := clone(c.peerInfo)
	c.mu.RUnlock()
	d := c.replica.state()
	out.Paired = d.Paired
	out.TakenOver = d.Takeover || d.Switched
	out.Writer = d.Writer
	out.Epoch = d.Epoch
	out.DesiredRevision = d.Committed.Revision
	out.Checksum = d.Committed.Checksum
	out.AppliedRevision = c.mgr.AppliedRevision()
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
	}
	if c.store.Rules().Snapshot().Checksum == d.Committed.Checksum && out.AppliedRevision == d.Committed.Revision {
		out.LocalPreparedChecksum = prepared
	}
	if out.Role == "active" && !c.mgr.Serving() {
		out.Role = "disconnected"
	}
	return out
}
func (c *Controller) materialize() error {
	d := c.replica.state()
	if c.store.Rules().Snapshot().Checksum != d.Committed.Checksum {
		if err := c.mgr.RestoreRules(d.Committed); err != nil {
			c.mgr.Demote()
			return err
		}
	}
	return nil
}
func (c *Controller) Bootstrap() error {
	c.edit.Lock()
	defer c.edit.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), PeerTimeout)
	defer cancel()
	if err := c.replica.Bootstrap(ctx); err != nil {
		return err
	}
	return c.materialize()
}
func (c *Controller) Action(ctx context.Context, action string, confirmed bool) error {
	c.edit.Lock()
	defer c.edit.Unlock()
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
	return c.materialize()
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
func (c *Controller) Mutate(ctx context.Context, req Mutation, apply func(Mutation) Reply) Reply {
	if len(req.ID) < 8 || len(req.ID) > 128 || strings.ContainsAny(req.ID, "\r\n") || req.Expected == "" {
		return errorReply(fmt.Errorf("Idempotency-Key and If-Match are required for cluster writes: %w", config.ErrRevisionConflict))
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
		return *out.Reply
	}
	c.edit.Lock()
	defer c.edit.Unlock()
	if err := c.materialize(); err != nil {
		return errorReply(err)
	}
	raw, _ := json.Marshal(struct {
		Method, Path, Expected string
		Body                   json.RawMessage
	}{req.Method, req.Path, req.Expected, req.Body})
	hash := digest(raw)
	cached, err := c.replica.BeginEdit(req.ID, hash, req.Expected)
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
	reply := apply(req)
	d = c.replica.state()
	reply.Version = d.Committed.Checksum
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
func (c *Controller) SyncStep(ctx context.Context) error {
	probe, cancel := context.WithTimeout(ctx, 3*time.Second)
	out, err := c.peer.Call(probe, "heartbeat", PeerRequest{})
	cancel()
	c.mu.Lock()
	if err != nil || out.State == nil {
		if err == nil {
			err = &PeerFault{"peer omitted heartbeat"}
		}
		var fault *PeerFault
		c.peerFault = errors.As(err, &fault) || errors.Is(err, config.ErrRevisionConflict) || errors.Is(err, config.ErrClusterUnavailable)
		if c.peerFault {
			c.offlineSince = time.Time{}
			c.status.PeerState = "error"
		} else {
			if c.offlineSince.IsZero() {
				c.offlineSince = c.now()
			}
			c.status.PeerState = "offline"
		}
		c.status.SyncError = err.Error()
	} else {
		c.peerInfo = out.State
		c.offlineSince = time.Time{}
		c.peerFault = false
		c.status.PeerState = "online"
		c.status.SyncError = ""
		now := c.now()
		c.status.LastSync = &now
	}
	c.mu.Unlock()
	c.edit.Lock()
	defer c.edit.Unlock()
	if err == nil && out.State != nil && out.State.Frozen && out.State.Takeover {
		if e := c.replica.ObserveTakeover(*out.State); e != nil {
			err = e
		}
	}
	recoverCtx, done := context.WithTimeout(ctx, PeerTimeout)
	defer done()
	if e := c.replica.Recover(recoverCtx); e != nil {
		err = e
	}
	if e := c.materialize(); e != nil {
		err = e
	}
	d := c.replica.state()
	if out.State != nil && out.State.Degraded && d.Writer == c.cfg.PeerID && out.State.PairID == d.PairID && out.State.Checksum == d.Committed.Checksum {
		_, e := c.peer.Call(recoverCtx, "resume", PeerRequest{PairID: d.PairID, Checksum: d.Committed.Checksum, Epoch: d.Epoch})
		if e != nil {
			err = e
		}
	}
	if err != nil {
		c.mu.Lock()
		c.status.SyncError = err.Error()
		c.mu.Unlock()
	}
	return err
}
func (c *Controller) TrafficStep(ctx context.Context) error {
	c.traffic.Lock()
	defer c.traffic.Unlock()
	started := c.now()
	readCtx, cancel := context.WithTimeout(ctx, RouterTimeout)
	in, err := c.upstream.Read(readCtx)
	cancel()
	if err != nil {
		c.mu.Lock()
		c.status.UpstreamState = "unavailable"
		c.status.Error = err.Error()
		c.mu.Unlock()
		return err
	}
	c.edit.Lock()
	defer c.edit.Unlock()
	d := c.replica.state()
	c.mu.Lock()
	c.status.Address = in.Address
	c.status.Owner = c.cfg.PeerID
	if in.Address == c.cfg.Address {
		c.status.Owner = c.node
	}
	c.status.UpstreamState = "observed"
	c.mu.Unlock()
	fail := func(err error) error {
		c.mgr.Demote()
		c.mu.Lock()
		c.prepared = ""
		c.status.Role = "disconnected"
		c.status.Error = err.Error()
		c.mu.Unlock()
		return err
	}
	if !d.Paired {
		return fail(errors.New("pair the nodes before enabling ingress"))
	}
	if err = c.materialize(); err != nil {
		return fail(err)
	}
	if err = c.mgr.ValidateIngress(c.cfg.Address, d.Committed.Rules); err != nil {
		return fail(err)
	}
	if in.Address != c.cfg.Address && c.mgr.Serving() {
		c.mgr.Demote()
	}
	if err = c.mgr.Prepare(); err != nil {
		return fail(err)
	}
	c.mu.Lock()
	c.prepared = d.Committed.Checksum
	c.status.Role = "standby"
	c.status.Error = ""
	offline := c.offlineSince
	fault := c.peerFault
	c.mu.Unlock()
	if d.Takeover && !d.Switched {
		c.mu.Lock()
		c.status.UpstreamState = "switch_pending"
		c.mu.Unlock()
		return fail(errors.New("upstream switch is unconfirmed; retry the switch explicitly"))
	}
	if in.Address == c.cfg.PeerAddress {
		// Fixed initial direction prevents mutually isolated nodes from taking
		// ingress back and forth. Recovered A never writes its IP over B.
		if c.node == c.cfg.InitialWriter || d.Switched || fault || offline.IsZero() || c.now().Sub(offline) < time.Duration(c.cfg.FailoverAfter)*time.Second {
			return nil
		}
		// Repeat the direct authenticated probe immediately before taking over.
		probe, stop := context.WithTimeout(ctx, 2*time.Second)
		_, probeErr := c.peer.Call(probe, "heartbeat", PeerRequest{})
		stop()
		var pf *PeerFault
		if probeErr == nil || errors.As(probeErr, &pf) || errors.Is(probeErr, config.ErrClusterUnavailable) || errors.Is(probeErr, config.ErrRevisionConflict) {
			return nil
		}
		if err = c.replica.FreezeTakeover(); err != nil {
			return fail(err)
		}
		d = c.replica.state()
		// Freeze and re-prepare together: a peer commit may have arrived after
		// the first readiness check but before the takeover freeze.
		if err = c.materialize(); err != nil {
			return fail(err)
		}
		if err = c.mgr.ValidateIngress(c.cfg.Address, d.Committed.Rules); err != nil {
			return fail(err)
		}
		if err = c.mgr.Prepare(); err != nil {
			return fail(err)
		}
		c.mu.Lock()
		c.prepared = d.Committed.Checksum
		c.mu.Unlock()
		switchCtx, stop := context.WithTimeout(ctx, RouterTimeout)
		err = c.upstream.Switch(switchCtx, in, c.cfg.Address)
		stop()
		if err != nil {
			c.mu.Lock()
			c.status.UpstreamState = "switch_pending"
			c.mu.Unlock()
			return fail(err)
		}
		if err = c.replica.MarkSwitched(); err != nil {
			return fail(err)
		}
		// A fresh read in the next iteration starts the local observation window.
		c.mu.Lock()
		c.status.UpstreamState = "applied"
		c.mu.Unlock()
		return nil
	}
	if in.Address != c.cfg.Address {
		return fail(errors.New("unknown upstream destination"))
	}
	if d.Takeover && !d.Switched {
		return fail(errors.New("upstream switch is unconfirmed; retry the switch explicitly"))
	}
	until := started.Add(time.Duration(c.cfg.PollInterval*2+2) * time.Second)
	if !c.now().Before(until) || !c.mgr.Activate(until, d.Committed.Checksum) {
		return fail(errors.New("ingress observation expired or rules changed"))
	}
	c.mu.Lock()
	c.status.Role = "active"
	c.mu.Unlock()
	return nil
}
func (c *Controller) RetrySwitch(ctx context.Context) error {
	c.traffic.Lock()
	defer c.traffic.Unlock()
	c.edit.Lock()
	defer c.edit.Unlock()
	d := c.replica.state()
	if !d.Takeover || d.Switched {
		return errors.New("no uncertain switch to retry")
	}
	if err := c.materialize(); err != nil {
		return err
	}
	if err := c.mgr.ValidateIngress(c.cfg.Address, d.Committed.Rules); err != nil {
		return err
	}
	if err := c.mgr.Prepare(); err != nil {
		return err
	}
	in, err := c.upstream.Read(ctx)
	if err != nil {
		return err
	}
	if err = c.upstream.Switch(ctx, in, c.cfg.Address); err != nil {
		return err
	}
	return c.replica.MarkSwitched()
}
func (c *Controller) Close() { c.closeOnce.Do(func() { c.release() }) }

func (c *Controller) Run(ctx context.Context) {
	defer c.Close()
	var wg sync.WaitGroup
	run := func(interval time.Duration, fn func(context.Context) error) {
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
				}
			}
		}()
	}
	run(time.Duration(c.cfg.PollInterval)*time.Second, c.SyncStep)
	run(time.Duration(c.cfg.PollInterval)*time.Second, c.TrafficStep)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			c.mgr.Demote()
			wg.Wait()
			return
		case <-tick.C:
			if c.mgr.LeaseExpired() {
				c.mgr.Demote()
			}
		}
	}
}

func (c *Controller) Snapshot() config.RuleSet { return c.replica.state().Committed }

// Rearm is a deliberate recovery step after the operator has switched the
// upstream back to A and restored two-node configuration replication.
func (c *Controller) Rearm(ctx context.Context) error {
	c.traffic.Lock()
	defer c.traffic.Unlock()
	c.edit.Lock()
	defer c.edit.Unlock()
	if c.node == c.cfg.InitialWriter {
		return errors.New("rearm on the designated backup")
	}
	d := c.replica.state()
	ctx, cancel := context.WithTimeout(ctx, PeerTimeout)
	defer cancel()
	in, err := c.upstream.Read(ctx)
	if err != nil {
		return err
	}
	if in.Address != c.cfg.PeerAddress {
		return errors.New("switch the upstream back to the initial primary before rearming")
	}
	out, err := c.peer.Call(ctx, "heartbeat", PeerRequest{})
	if err != nil {
		return err
	}
	if out.State == nil || out.State.PairID != d.PairID || out.State.Epoch != d.Epoch || out.State.Checksum != d.Committed.Checksum || out.State.PreparedChecksum != d.Committed.Checksum || out.State.Frozen || out.State.PendingID != "" || out.State.Transferring {
		return ErrFrozen
	}
	c.mgr.Demote()
	return c.replica.Rearm()
}
