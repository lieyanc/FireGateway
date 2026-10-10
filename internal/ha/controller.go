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

// Status describes the pair in an operator's terms: which node is primary,
// who forwards, whether the rules are in sync, and what needs attention.
type Status struct {
	Enabled       bool   `json:"enabled"`
	NodeID        string `json:"nodeId"`
	PeerID        string `json:"peerId,omitempty"`
	ClusterID     string `json:"clusterId,omitempty"`
	InitialWriter string `json:"initialWriter,omitempty"`
	Paired        bool   `json:"paired"`
	// Role is primary, backup or standalone. The primary edits the shared
	// rules and keeps the router pointing at itself.
	Role string `json:"role"`
	// Serving reports whether the router sends new connections to this node.
	Serving bool `json:"serving"`
	// Ready reports whether every active rule is bound on this node.
	Ready bool `json:"ready"`
	// Sync is unpaired, synced, syncing, waiting, local_only or conflict.
	Sync string `json:"sync"`
	// Writable reports whether shared rules can be edited through this node.
	Writable bool          `json:"writable"`
	Peer     PeerStatus    `json:"peer"`
	Ingress  IngressStatus `json:"ingress"`
	Issues   []Issue       `json:"issues"`
	Details  Details       `json:"details"`
}

type PeerStatus struct {
	// State is online, offline, error (reachable but misconfigured) or unknown.
	State   string `json:"state"`
	Role    string `json:"role,omitempty"`
	Ready   bool   `json:"ready"`
	Serving bool   `json:"serving"`
}

type IngressStatus struct {
	// State is observed, switching, unavailable or unknown.
	State   string `json:"state"`
	Owner   string `json:"owner,omitempty"`
	Address string `json:"address,omitempty"`
}

// Issue is something the operator should know or fix; Code is stable for
// clients, Message carries the underlying error.
type Issue struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

type Details struct {
	Epoch           int64      `json:"epoch"`
	PeerEpoch       int64      `json:"peerEpoch"`
	Revision        int64      `json:"revision"`
	PeerRevision    int64      `json:"peerRevision"`
	Checksum        string     `json:"checksum"`
	PeerChecksum    string     `json:"peerChecksum,omitempty"`
	PendingUpdateID string     `json:"pendingUpdateId,omitempty"`
	LastSync        *time.Time `json:"lastSync,omitempty"`
}

// maxClaimTries bounds uncertain router switches per claim, so two nodes
// that both believe they are primary cannot move ingress back and forth.
const maxClaimTries = 3

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
	kick      chan struct{} // runs the sync loop now
	nudge     chan struct{} // runs the traffic loop now

	// Lock order: traffic, writes, apply, mu. Only writes is ever held across
	// a peer call, and the ingress path never takes it, so a slow or absent
	// peer cannot delay the decision who forwards traffic.
	writes  sync.Mutex // serializes replicated writes and recovery
	apply   sync.Mutex // serializes installing rules and binding listeners
	traffic sync.Mutex // serializes ingress observation and router switches

	mu             sync.RWMutex
	link           string // peer link: online, offline, error or unknown
	syncErr        string
	lastSync       *time.Time
	split          bool // both nodes are primary and at least one saved alone
	ingress        IngressStatus
	peerInfo       *PeerState
	offlineSince   time.Time // peer unreachable since
	unhealthySince time.Time // peer reachable but unable to forward since
	peerFault      bool
	prepared       string    // committed checksum this node's listeners are ready for
	readyErr       string    // why this node is not ready
	ingressErr     string    // last router observation or switch failure
	switchHold     time.Time // an uncertain switch may still roll back until then
	claimTries     int       // uncertain switches of the current claim
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
	c := &Controller{release: release, store: store, mgr: mgr, cfg: cfg, node: node, replica: r, peer: peer, upstream: upstream, now: time.Now, kick: make(chan struct{}, 1), nudge: make(chan struct{}, 1), link: "unknown", ingress: IngressStatus{State: "unknown"}}
	// The durable journal is the only authority after bootstrap, never an
	// arbitrary rules.json that was edited while the process was stopped.
	if err = store.Rules().Restore(r.state().Committed); err != nil {
		release()
		return nil, err
	}
	store.Rules().SetBackend(r)
	return c, nil
}

// wake runs both loops now, e.g. after the peer committed a change or handed
// this node the primary role.
func (c *Controller) wake() {
	for _, ch := range []chan struct{}{c.kick, c.nudge} {
		select {
		case ch <- struct{}{}:
		default:
		}
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
	out := PeerState{NodeID: c.node, PairID: d.PairID, Paired: d.Paired, Writer: d.Writer, Epoch: d.Epoch, Revision: d.Committed.Revision, Checksum: d.Committed.Checksum, PreparedChecksum: prepared, Serving: c.mgr.Serving(), Degraded: d.Degraded, Transferring: d.Transfer != nil}
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
	link, syncErr, lastSync, split := c.link, c.syncErr, c.lastSync, c.split
	in, ingressErr, readyErr := c.ingress, c.ingressErr, c.readyErr
	peer := clone(c.peerInfo)
	c.mu.RUnlock()
	d := c.replica.state()
	out := Status{Enabled: true, NodeID: c.node, PeerID: c.cfg.PeerID, ClusterID: c.cfg.ID, InitialWriter: c.cfg.InitialWriter, Paired: d.Paired, Role: "backup", Serving: c.mgr.Serving(), Ready: c.ready(d), Ingress: in, Peer: PeerStatus{State: link}, Issues: []Issue{}}
	primary := d.Writer == c.node
	if primary {
		out.Role = "primary"
	}
	online := link == "online" && peer != nil && peer.PairID == d.PairID
	if online {
		out.Peer.Role = "backup"
		if peer.Writer == peer.NodeID {
			out.Peer.Role = "primary"
		}
		out.Peer.Ready = peer.PreparedChecksum != "" && peer.PreparedChecksum == peer.Checksum
		out.Peer.Serving = peer.Serving
	}
	if primary && d.Claiming && in.State == "observed" && in.Owner != c.node {
		out.Ingress.State = "switching"
	}
	switch {
	case !d.Paired:
		out.Sync = "unpaired"
	case split:
		out.Sync = "conflict"
	case d.Degraded:
		out.Sync = "local_only"
	case d.Pending != nil || d.Transfer != nil:
		out.Sync = "syncing"
	case online && peer.Epoch == d.Epoch && peer.Checksum == d.Committed.Checksum:
		out.Sync = "synced"
	default:
		out.Sync = "waiting"
	}
	// Mirrors Mutate: the primary needs the peer in step unless it saves
	// alone; a backup forwards edits to an online primary.
	agreed := online && peer.Writer == d.Writer && peer.Epoch == d.Epoch
	out.Writable = d.Paired && d.Transfer == nil && d.Pending == nil && ((primary && (d.Degraded || agreed)) || (!primary && agreed))

	add := func(code, msg string) { out.Issues = append(out.Issues, Issue{code, msg}) }
	if !d.Paired {
		add("unpaired", "")
	}
	switch link {
	case "offline":
		add("peer_offline", syncErr)
	case "error":
		add("peer_error", syncErr)
	case "online":
		if syncErr != "" {
			add("sync_error", syncErr)
		}
	}
	if split {
		add("conflict", "")
	}
	if d.Paired && !out.Ready {
		add("not_ready", readyErr)
	}
	if online && d.Paired && !out.Peer.Ready {
		add("peer_not_ready", "")
	}
	switch {
	case in.State == "unavailable":
		add("router_unavailable", ingressErr)
	case ingressErr != "":
		add("ingress_error", ingressErr)
	}
	if in.State == "observed" && in.Owner == "" {
		add("ingress_unknown", in.Address)
	}
	if primary && d.Paired && !d.Claiming && in.State == "observed" && in.Owner == c.cfg.PeerID {
		add("ingress_elsewhere", "")
	}
	if d.Degraded {
		add("local_only", "")
	} else if primary && d.Paired && !online {
		add("read_only", "")
	}

	out.Details = Details{Epoch: d.Epoch, Revision: d.Committed.Revision, Checksum: d.Committed.Checksum, LastSync: lastSync}
	if d.Pending != nil {
		out.Details.PendingUpdateID = d.Pending.ID
	}
	if peer != nil {
		out.Details.PeerEpoch, out.Details.PeerRevision, out.Details.PeerChecksum = peer.Epoch, peer.Revision, peer.Checksum
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
	defer c.wake()
	return c.refresh()
}

// Promote lets this node save edits alone while the peer is unreachable; a
// backup also takes over ingress. The peer follows once it is back, unless
// it saved edits alone too.
func (c *Controller) Promote(confirmed bool) error {
	if !confirmed {
		return errors.New("confirm saving edits on this node alone")
	}
	c.writes.Lock()
	defer c.writes.Unlock()
	c.mu.RLock()
	online := c.link == "online"
	c.mu.RUnlock()
	if online {
		return errors.New("the peer is online; switch over instead")
	}
	if err := c.replica.Promote(); err != nil {
		return err
	}
	c.resetClaim()
	defer c.wake()
	return c.refresh()
}

// Rejoin discards this node's rules, archived first, and adopts the peer's.
func (c *Controller) Rejoin(ctx context.Context, confirmed bool) error {
	c.writes.Lock()
	defer c.writes.Unlock()
	ctx, cancel := context.WithTimeout(ctx, PeerTimeout)
	defer cancel()
	if err := c.replica.Rejoin(ctx, confirmed); err != nil {
		return err
	}
	c.mu.Lock()
	c.split = false
	c.mu.Unlock()
	defer c.wake()
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
		if !d.Paired {
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
	online := c.link == "online"
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

// SyncStep exchanges state with the peer, follows a newer primary, finishes
// interrupted replication and keeps the local listeners ready for the
// committed rules.
func (c *Controller) SyncStep(ctx context.Context) error {
	peer, err := c.heartbeat(ctx)
	c.writes.Lock()
	defer c.writes.Unlock()
	errs := []error{err}
	stepCtx, done := context.WithTimeout(ctx, PeerTimeout)
	defer done()
	d := c.replica.state()
	split := false
	if peer != nil {
		split = diverged(d, *peer, c.node, c.cfg.PeerID)
		if follows(d, *peer, c.cfg.PeerID) {
			errs = append(errs, c.follow(stepCtx))
		}
	}
	errs = append(errs, c.replica.Recover(stepCtx))
	d = c.replica.state()
	if peer != nil && peer.Degraded && d.Writer == c.cfg.PeerID && peer.PairID == d.PairID && peer.Epoch == d.Epoch && peer.Checksum == d.Committed.Checksum {
		_, e := c.peer.Call(stepCtx, "resume", PeerRequest{PairID: d.PairID, Checksum: d.Committed.Checksum, Epoch: d.Epoch})
		errs = append(errs, e)
	}
	if peer != nil && c.handBack(d, *peer) {
		errs = append(errs, c.replica.Transfer(stepCtx))
	}
	err = errors.Join(errs...)
	c.mu.Lock()
	c.split = split
	if err != nil {
		c.syncErr = err.Error()
	}
	c.mu.Unlock()
	// Readiness errors are reported separately; they are not sync failures.
	_ = c.refresh()
	return err
}

// follow adopts the state of a peer that became primary while this node was
// away, or that saved edits alone.
func (c *Controller) follow(ctx context.Context) error {
	out, err := c.peer.Call(ctx, "snapshot", PeerRequest{})
	if err != nil {
		return err
	}
	if out.State == nil || out.Snapshot == nil {
		return &PeerFault{"peer omitted its snapshot"}
	}
	return c.replica.Follow(*out.State, *out.Snapshot)
}

// handBack reports whether the router was pointed at the peer from outside
// while both nodes agree on the rules. The primary then hands its role to
// the node that has the traffic instead of taking the traffic back.
func (c *Controller) handBack(d replicaDisk, st PeerState) bool {
	c.mu.RLock()
	in := c.ingress
	c.mu.RUnlock()
	return d.Paired && d.Writer == c.node && !d.Claiming && !d.Degraded && d.Pending == nil && d.Transfer == nil &&
		in.State == "observed" && in.Owner == c.cfg.PeerID &&
		st.PairID == d.PairID && st.Epoch == d.Epoch && st.Checksum == d.Committed.Checksum && st.PreparedChecksum == st.Checksum
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
			c.link = "error"
		} else {
			if c.offlineSince.IsZero() {
				c.offlineSince = now
			}
			c.link = "offline"
		}
		c.syncErr = err.Error()
		return nil, err
	}
	st := out.State
	c.peerInfo = st
	if c.usage != nil {
		c.usage.SetPeer(st.Usage)
	}
	c.offlineSince = time.Time{}
	c.peerFault = false
	// The primary is unhealthy when it holds our committed rules but cannot
	// forward them. Differing checksums mean replication is mid-flight.
	if st.PairID == d.PairID && st.Writer == st.NodeID && st.Checksum == d.Committed.Checksum && st.PreparedChecksum != st.Checksum {
		if c.unhealthySince.IsZero() {
			c.unhealthySince = now
		}
	} else {
		c.unhealthySince = time.Time{}
	}
	c.link = "online"
	c.syncErr = ""
	c.lastSync = &now
	return st, nil
}
func isFault(err error) bool {
	var fault *PeerFault
	return errors.As(err, &fault) || errors.Is(err, config.ErrRevisionConflict) || errors.Is(err, config.ErrClusterUnavailable)
}

// TrafficStep follows the router. Its DNAT destination is the only arbiter of
// who forwards: the gate opens exactly while the router points here. When
// the router cannot be read the last observed role stands, so a router API
// outage never interrupts forwarding. The primary points the router at
// itself when it has to; a backup takes over from a failed primary.
func (c *Controller) TrafficStep(ctx context.Context) error {
	c.traffic.Lock()
	defer c.traffic.Unlock()
	readCtx, cancel := context.WithTimeout(ctx, RouterTimeout)
	in, err := c.upstream.Read(readCtx)
	cancel()
	if err != nil {
		c.mu.Lock()
		c.ingress.State = "unavailable"
		c.ingressErr = err.Error()
		c.mu.Unlock()
		return err
	}
	d := c.replica.state()
	mine := in.Address == c.cfg.Address
	c.mgr.SetServing(mine && d.Paired)
	c.observe(in.Address)
	switch {
	case d.Paired && d.Writer == c.node && d.Claiming:
		err = c.claim(ctx, in, mine)
	case d.Paired && d.Writer != c.node && c.ready(d) && c.takeoverDue(ctx, d):
		err = c.takeover(ctx, in, mine)
	}
	c.mu.Lock()
	c.ingressErr = ""
	if err != nil {
		c.ingressErr = err.Error()
	}
	c.mu.Unlock()
	return err
}

func (c *Controller) observe(address string) {
	owner := ""
	switch address {
	case c.cfg.Address:
		owner = c.node
	case c.cfg.PeerAddress:
		owner = c.cfg.PeerID
	}
	c.mu.Lock()
	c.ingress = IngressStatus{State: "observed", Owner: owner, Address: address}
	c.mu.Unlock()
}

// takeoverDue decides whether this backup should replace the primary. Only
// a backup takes over, and a primary that lost ingress while cut off stays
// primary in its own view until it hears from the peer and follows it, so
// two nodes that cannot see each other never pass ingress back and forth.
func (c *Controller) takeoverDue(ctx context.Context, d replicaDisk) bool {
	if !d.Paired || d.Writer == c.node {
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

// takeover makes this backup the primary at a new epoch, which fences the
// old primary's late writes, then points the router here.
func (c *Controller) takeover(ctx context.Context, in Ingress, mine bool) error {
	if err := c.replica.Promote(); err != nil {
		return err
	}
	c.resetClaim()
	return c.claim(ctx, in, mine)
}

func (c *Controller) resetClaim() {
	c.mu.Lock()
	c.switchHold, c.claimTries = time.Time{}, 0
	c.mu.Unlock()
}

// claim points the router at this primary. A switch whose outcome is
// uncertain is not repeated until OpenWrt's rollback window has passed; the
// router then shows whether it stuck. Router already pointing here at the
// first look may stem from such a switch before a restart, so that is held
// for one window too.
func (c *Controller) claim(ctx context.Context, in Ingress, mine bool) error {
	now := c.now()
	c.mu.Lock()
	if mine && c.switchHold.IsZero() {
		c.switchHold = now.Add(RollbackWindow)
	}
	hold, tries := c.switchHold, c.claimTries
	c.mu.Unlock()
	if now.Before(hold) {
		return nil
	}
	if mine {
		c.resetClaim()
		return c.replica.SetClaiming(false)
	}
	if tries >= maxClaimTries {
		return errors.New("the router keeps returning to the peer; check the node link and the router, then switch over again")
	}
	// Bind again right before switching: the router must never point at
	// listeners that are not ready.
	if err := c.refresh(); err != nil {
		return fmt.Errorf("this node is not ready to forward: %w", err)
	}
	switchCtx, cancel := context.WithTimeout(ctx, RouterTimeout)
	err := c.upstream.Switch(switchCtx, in, c.cfg.Address)
	cancel()
	if errors.Is(err, ErrSwitchUncertain) {
		c.mu.Lock()
		c.switchHold = now.Add(RollbackWindow)
		c.claimTries++
		c.mu.Unlock()
	}
	if err != nil {
		return err
	}
	c.mgr.SetServing(true)
	c.observe(c.cfg.Address)
	c.resetClaim()
	return c.replica.SetClaiming(false)
}

// inSync fetches the peer's state and requires both nodes to hold the same
// committed rules with nothing in flight, the precondition of a switchover.
func (c *Controller) inSync(ctx context.Context) (*PeerState, error) {
	st, err := c.heartbeat(ctx)
	if err != nil {
		return nil, fmt.Errorf("the peer must be online to switch over: %w", err)
	}
	d := c.replica.state()
	if !d.Paired || st.PairID != d.PairID || st.Epoch != d.Epoch || st.Checksum != d.Committed.Checksum || d.Degraded || st.Degraded || d.Pending != nil || st.PendingID != "" || d.Transfer != nil || st.Transferring {
		return nil, fmt.Errorf("wait until both nodes are in sync, then switch over: %w", config.ErrClusterUnavailable)
	}
	return st, nil
}

// Switchover makes target, this node or its peer, the primary. The role is
// handed over first and the new primary then points the router at itself,
// so a failure halfway leaves a primary that keeps trying, never two.
func (c *Controller) Switchover(ctx context.Context, target string) error {
	switch target {
	case c.node:
		if err := c.refresh(); err != nil {
			return fmt.Errorf("this node is not ready to forward: %w", err)
		}
		if d := c.replica.state(); d.Writer != c.node {
			if _, err := c.inSync(ctx); err != nil {
				return err
			}
			callCtx, cancel := context.WithTimeout(ctx, PeerTimeout)
			_, err := c.peer.Call(callCtx, "yield", PeerRequest{PairID: d.PairID, Epoch: d.Epoch, Checksum: d.Committed.Checksum})
			cancel()
			if err != nil {
				return err
			}
		} else if err := c.replica.SetClaiming(true); err != nil {
			return err
		}
		c.resetClaim()
		return c.TrafficStep(ctx)
	case c.cfg.PeerID:
		c.writes.Lock()
		defer c.writes.Unlock()
		d := c.replica.state()
		callCtx, cancel := context.WithTimeout(ctx, PeerTimeout)
		defer cancel()
		if d.Writer != c.node {
			_, err := c.peer.Call(callCtx, "claim", PeerRequest{PairID: d.PairID, Epoch: d.Epoch})
			return err
		}
		st, err := c.inSync(callCtx)
		if err != nil {
			return err
		}
		if st.PreparedChecksum != st.Checksum {
			return fmt.Errorf("the peer is not ready to forward these rules: %w", config.ErrClusterUnavailable)
		}
		// The peer is woken by the handoff and claims the router itself.
		return c.replica.Transfer(callCtx)
	}
	return fmt.Errorf("unknown node %q", target)
}

// yield hands the primary role to the peer on its request.
func (c *Controller) yield(ctx context.Context, req PeerRequest) error {
	c.writes.Lock()
	defer c.writes.Unlock()
	d := c.replica.state()
	if d.Writer != c.node {
		return nil
	}
	if req.PairID != d.PairID || req.Epoch != d.Epoch || req.Checksum != d.Committed.Checksum {
		return fmt.Errorf("the nodes are not in sync yet: %w", config.ErrRevisionConflict)
	}
	return c.replica.Transfer(ctx)
}

// claimRequest makes this primary point the router at itself on the
// peer's request.
func (c *Controller) claimRequest(req PeerRequest) error {
	d := c.replica.state()
	if req.PairID != d.PairID || d.Writer != c.node {
		return config.ErrRevisionConflict
	}
	if err := c.replica.SetClaiming(true); err != nil {
		return err
	}
	c.resetClaim()
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
	loop(c.TrafficStep, c.nudge)
	<-ctx.Done()
	c.mgr.SetServing(false)
	wg.Wait()
}

func (c *Controller) Snapshot() config.RuleSet { return c.replica.state().Committed }
