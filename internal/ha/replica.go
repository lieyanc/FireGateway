package ha

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/lieyanc/FireGateway/internal/config"
)

var ErrFrozen = fmt.Errorf("shared rules are read-only until the pair is synchronized or the writer is explicitly promoted: %w", config.ErrClusterUnavailable)

// PendingError means the transaction may already be durable on the peer.
// It must be queried/retried, never presented as an ordinary rolled-back write.
type PendingError struct {
	ID    string
	Cause error
}

func (e *PendingError) Error() string {
	return "update " + e.ID + " is pending confirmation: " + e.Cause.Error()
}
func (e *PendingError) Unwrap() error { return e.Cause }

type Reply struct {
	Status     int             `json:"status"`
	Body       json.RawMessage `json:"body,omitempty"`
	Version    string          `json:"version,omitempty"`
	Durability string          `json:"durability,omitempty"`
}
type RequestRecord struct {
	Order     int64  `json:"order"`
	Hash      string `json:"hash"`
	Committed bool   `json:"committed"`
	Version   string `json:"version,omitempty"`
	Reply     *Reply `json:"reply,omitempty"`
}
type Transaction struct {
	ID          string         `json:"id"`
	PairID      string         `json:"pairId"`
	Kind        string         `json:"kind"`
	Phase       string         `json:"phase"`
	Expected    string         `json:"expected"`
	Next        config.RuleSet `json:"next"`
	RequestID   string         `json:"requestId,omitempty"`
	RequestHash string         `json:"requestHash,omitempty"`
}
type Handoff struct {
	ID       string         `json:"id"`
	To       string         `json:"to"`
	Epoch    int64          `json:"epoch"`
	Decided  bool           `json:"decided"`
	Snapshot config.RuleSet `json:"snapshot"`
}

// One atomic, checksummed checkpoint is authoritative. rules.json is its
// materialized view, so a crash between those files is recoverable.
type replicaDisk struct {
	Sequence     int64                    `json:"sequence"`
	Format       int                      `json:"format"`
	NodeID       string                   `json:"nodeId"`
	PeerID       string                   `json:"peerId"`
	PairID       string                   `json:"pairId"`
	Paired       bool                     `json:"paired"`
	Writer       string                   `json:"writer"`
	Epoch        int64                    `json:"epoch"`
	Frozen       bool                     `json:"frozen"`
	Takeover     bool                     `json:"takeover"`
	Switched     bool                     `json:"switched"`
	Degraded     bool                     `json:"degraded"`
	Committed    config.RuleSet           `json:"committed"`
	Pending      *Transaction             `json:"pending,omitempty"`
	Transfer     *Handoff                 `json:"transfer,omitempty"`
	Requests     map[string]RequestRecord `json:"requests"`
	LastID       string                   `json:"lastId,omitempty"`
	LastTransfer string                   `json:"lastTransfer,omitempty"`
}
type Replica struct {
	op               sync.Mutex
	mu               sync.RWMutex
	cfg              config.ClusterConfig
	node, path       string
	disk             replicaDisk
	peer             PeerAPI
	editID, editHash string
}

func nonce() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func clone[T any](v T) T     { b, _ := json.Marshal(v); var out T; _ = json.Unmarshal(b, &out); return out }
func atomicJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".fg-ha-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(b); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return replaceDurable(f.Name(), path)
}
func OpenReplica(cfg config.ClusterConfig, node, path string, initial config.RuleSet, peer PeerAPI) (*Replica, error) {
	r := &Replica{cfg: cfg, node: node, path: path, peer: peer}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if initial.SchemaVersion == 2 {
			return nil, errors.New("replication journal is missing; restore it or explicitly rejoin with a fresh rules file")
		}
		d := replicaDisk{Format: 1, NodeID: node, PeerID: cfg.PeerID, Writer: cfg.InitialWriter, Committed: initial, Requests: map[string]RequestRecord{}}
		if err = r.save(d); err != nil {
			return nil, err
		}
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	var env struct {
		Checksum string          `json:"checksum"`
		State    json.RawMessage `json:"state"`
	}
	if err = json.Unmarshal(b, &env); err != nil {
		return nil, err
	}
	if digest(env.State) != env.Checksum {
		return nil, errors.New("replication journal checksum mismatch")
	}
	if err = json.Unmarshal(env.State, &r.disk); err != nil {
		return nil, err
	}
	d := r.disk
	if d.Format != 1 || d.NodeID != node || d.PeerID != cfg.PeerID || d.Committed.ClusterID != cfg.ID || (d.Writer != node && d.Writer != cfg.PeerID) {
		return nil, errors.New("replication journal identity mismatch")
	}
	if err = d.Committed.Verify(); err != nil {
		return nil, err
	}
	if d.Transfer != nil {
		if err = d.Transfer.Snapshot.Verify(); err != nil {
			return nil, err
		}
	}
	if d.Pending != nil {
		if err = d.Pending.Next.Verify(); err != nil {
			return nil, err
		}
	}
	if d.Requests == nil {
		r.disk.Requests = map[string]RequestRecord{}
	}
	return r, nil
}
func (r *Replica) state() replicaDisk { r.mu.RLock(); defer r.mu.RUnlock(); return clone(r.disk) }

// op serializes all durable mutations; mu only protects quick snapshots.
func (r *Replica) save(d replicaDisk) error {
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if err = atomicJSON(r.path, struct {
		Checksum string          `json:"checksum"`
		State    json.RawMessage `json:"state"`
	}{digest(b), b}); err != nil {
		return err
	}
	r.mu.Lock()
	r.disk = clone(d)
	r.mu.Unlock()
	return nil
}
func (r *Replica) call(ctx context.Context, method string, req PeerRequest) (PeerResponse, error) {
	return r.peer.Call(ctx, method, req)
}
func (r *Replica) Bootstrap(ctx context.Context) error {
	r.op.Lock()
	defer r.op.Unlock()
	d := r.state()
	if d.Paired {
		return nil
	}
	if d.Writer != r.node {
		return errors.New("initialize on the configured initialWriter")
	}
	if d.Pending == nil {
		next := config.WithWriter(d.Committed, 1, r.node, d.Committed.Rules)
		d.PairID = nonce()
		d.Epoch = 1
		d.Pending = &Transaction{ID: nonce(), PairID: d.PairID, Kind: "pair", Phase: "prepare", Next: next}
		if err := r.save(d); err != nil {
			return err
		}
	}
	return r.finish(ctx)
}
func (r *Replica) Commit(previous, next config.RuleSet) (config.RuleSet, error) {
	r.op.Lock()
	defer r.op.Unlock()
	d := r.state()
	if !d.Paired || d.Writer != r.node || d.Frozen || d.Transfer != nil {
		return previous, ErrFrozen
	}
	if d.Pending != nil {
		return previous, &PendingError{d.Pending.ID, config.ErrClusterUnavailable}
	}
	if d.Committed.Checksum != previous.Checksum {
		return previous, config.ErrRevisionConflict
	}
	next = config.WithWriter(previous, d.Epoch, r.node, next.Rules)
	if err := next.Verify(); err != nil {
		return previous, err
	}
	tx := Transaction{ID: nonce(), PairID: d.PairID, Kind: "rules", Phase: "prepare", Expected: previous.Checksum, Next: next, RequestID: r.editID, RequestHash: r.editHash}
	if d.Degraded {
		d.Pending = &tx
		if err := r.save(d); err != nil {
			return previous, err
		}
		if err := r.acceptCommit(tx.ID); err != nil {
			return previous, err
		}
		return next, nil
	}
	d.Pending = &tx
	if err := r.save(d); err != nil {
		return previous, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), PeerTimeout)
	defer cancel()
	if err := r.finish(ctx); err != nil {
		return previous, &PendingError{tx.ID, err}
	}
	return next, nil
}
func (r *Replica) finish(ctx context.Context) error {
	d := r.state()
	tx := d.Pending
	if tx == nil {
		return nil
	}
	// Always replay prepare first: a lost response or a receiver restart must
	// not turn a retry into a different transaction.
	if _, err := r.call(ctx, "prepare", PeerRequest{Transaction: tx}); err != nil {
		return err
	}
	if tx.Phase != "commit" {
		tx.Phase = "commit"
		d.Pending = tx
		if err := r.save(d); err != nil {
			return err
		}
	}
	if _, err := r.call(ctx, "commit", PeerRequest{PairID: d.PairID, ID: tx.ID}); err != nil {
		return err
	}
	return r.acceptCommit(tx.ID)
}
func (r *Replica) acceptCommit(id string) error {
	d := r.state()
	if d.LastID == id {
		return nil
	}
	if d.Pending == nil || d.Pending.ID != id {
		return config.ErrRevisionConflict
	}
	tx := d.Pending
	d.Committed = tx.Next
	d.Paired = true
	d.LastID = id
	d.Pending = nil
	if tx.RequestID != "" {
		d.Sequence++
		d.Requests[tx.RequestID] = RequestRecord{Order: d.Sequence, Hash: tx.RequestHash, Committed: true, Version: tx.Next.Checksum}
	}
	trimRequests(&d)
	return r.save(d)
}
func trimRequests(d *replicaDisk) {
	if len(d.Requests) <= 128 {
		return
	}
	ids := make([]string, 0, len(d.Requests))
	for id := range d.Requests {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return d.Requests[ids[i]].Order < d.Requests[ids[j]].Order })
	for _, id := range ids[:len(ids)-128] {
		delete(d.Requests, id)
	}
}

func (r *Replica) Prepare(tx Transaction) error {
	r.op.Lock()
	defer r.op.Unlock()
	d := r.state()
	if err := tx.Next.Verify(); err != nil {
		return err
	}
	if tx.Next.ClusterID != r.cfg.ID || tx.Next.WriterID != r.cfg.PeerID || len(tx.ID) < 16 || len(tx.PairID) < 16 {
		return config.ErrRevisionConflict
	}
	if d.LastID == tx.ID && d.Committed.Checksum == tx.Next.Checksum {
		return nil
	}
	if d.Frozen || d.Transfer != nil || d.Degraded {
		return ErrFrozen
	}
	if tx.Kind == "pair" {
		if d.Paired || r.cfg.InitialWriter != r.cfg.PeerID || (d.PairID != "" && d.PairID != tx.PairID) {
			return config.ErrRevisionConflict
		}
		// Existing local rules require explicit rejoin, never silent overwrite.
		a, _ := json.Marshal(d.Committed.Rules)
		b, _ := json.Marshal(tx.Next.Rules)
		if len(d.Committed.Rules) > 0 && string(a) != string(b) {
			return errors.New("peer has local rules; export them and explicitly rejoin")
		}
		if tx.Next.WriterEpoch != 1 {
			return config.ErrRevisionConflict
		}
		d.PairID = tx.PairID
		d.Epoch = 1
		d.Writer = r.cfg.PeerID
	} else if tx.Kind == "rules" {
		if !d.Paired || d.Writer != r.cfg.PeerID || d.PairID != tx.PairID || d.Epoch != tx.Next.WriterEpoch || tx.Expected != d.Committed.Checksum || tx.Next.ParentChecksum != tx.Expected {
			return config.ErrRevisionConflict
		}
		want := config.WithWriter(d.Committed, d.Epoch, d.Writer, tx.Next.Rules)
		if want.Checksum != tx.Next.Checksum {
			return config.ErrRevisionConflict
		}
	} else {
		return errors.New("unknown transaction kind")
	}
	if d.Pending != nil {
		if d.Pending.ID != tx.ID || d.Pending.Next.Checksum != tx.Next.Checksum || d.Pending.RequestID != tx.RequestID || d.Pending.RequestHash != tx.RequestHash {
			return config.ErrRevisionConflict
		}
		return nil
	}
	tx.Phase = "prepare"
	d.Pending = &tx
	return r.save(d)
}
func (r *Replica) CommitPeer(pair, id string) error {
	r.op.Lock()
	defer r.op.Unlock()
	d := r.state()
	if pair != d.PairID {
		return config.ErrRevisionConflict
	}
	if d.LastID == id {
		return nil
	}
	if d.Frozen || d.Degraded || d.Writer != r.cfg.PeerID {
		return ErrFrozen
	}
	return r.acceptCommit(id)
}
func (r *Replica) Recover(ctx context.Context) error {
	r.op.Lock()
	defer r.op.Unlock()
	d := r.state()
	if d.Transfer != nil && d.Transfer.To == r.cfg.PeerID {
		return r.finishHandoff(ctx)
	}
	if d.Pending != nil && d.Writer == r.node && !d.Frozen {
		if d.Degraded {
			return r.acceptCommit(d.Pending.ID)
		}
		return r.finish(ctx)
	}
	return nil
}
func (r *Replica) FreezeTakeover() error {
	r.op.Lock()
	defer r.op.Unlock()
	d := r.state()
	if !d.Paired || d.Transfer != nil {
		return ErrFrozen
	}
	if d.Pending != nil {
		if err := atomicJSON(r.path+".archive-"+nonce(), d); err != nil {
			return err
		}
		d.Pending = nil
	}
	d.Frozen = true
	d.Takeover = true
	return r.save(d)
}
func (r *Replica) MarkSwitched() error {
	r.op.Lock()
	defer r.op.Unlock()
	d := r.state()
	d.Switched = true
	return r.save(d)
}
func (r *Replica) BeginEdit(id, hash, expected string) (*Reply, error) {
	r.op.Lock()
	defer r.op.Unlock()
	d := r.state()
	if rec, ok := d.Requests[id]; ok {
		if rec.Hash != hash {
			return nil, config.ErrRevisionConflict
		}
		if rec.Reply != nil {
			v := clone(*rec.Reply)
			return &v, nil
		}
		if rec.Committed {
			return nil, fmt.Errorf("request already committed at %s; query the transaction and refresh: %w", rec.Version, config.ErrRevisionConflict)
		}
	}
	if d.Pending != nil {
		return nil, &PendingError{d.Pending.ID, config.ErrClusterUnavailable}
	}
	if !d.Paired || d.Writer != r.node || d.Frozen || d.Transfer != nil {
		return nil, ErrFrozen
	}
	if expected == "" || d.Committed.Checksum != expected {
		return nil, config.ErrRevisionConflict
	}
	r.editID = id
	r.editHash = hash
	return nil, nil
}
func (r *Replica) EndEdit(id, hash string, reply Reply) error {
	r.op.Lock()
	defer r.op.Unlock()
	r.editID = ""
	r.editHash = ""
	d := r.state()
	if d.Pending != nil {
		return nil
	}
	rec := d.Requests[id]
	if rec.Order == 0 {
		d.Sequence++
		rec.Order = d.Sequence
	}
	rec.Hash = hash
	rec.Reply = &reply
	d.Requests[id] = rec
	trimRequests(&d)
	return r.save(d)
}
func (r *Replica) Transfer(ctx context.Context) error {
	r.op.Lock()
	defer r.op.Unlock()
	d := r.state()
	if d.Transfer != nil {
		return r.finishHandoff(ctx)
	}
	if !d.Paired || d.Writer != r.node || d.Pending != nil || d.Degraded {
		return ErrFrozen
	}
	d.Transfer = &Handoff{ID: nonce(), To: r.cfg.PeerID, Epoch: d.Epoch + 1, Snapshot: config.WithWriter(d.Committed, d.Epoch+1, r.cfg.PeerID, d.Committed.Rules)}
	if err := r.save(d); err != nil {
		return err
	}
	return r.finishHandoff(ctx)
}
func (r *Replica) finishHandoff(ctx context.Context) error {
	d := r.state()
	h := d.Transfer
	if h == nil {
		return nil
	}
	if !h.Decided {
		if _, err := r.call(ctx, "handoff-prepare", PeerRequest{PairID: d.PairID, Handoff: h}); err != nil {
			return err
		}
		h.Decided = true
		d.Transfer = h
		d.Writer = h.To
		d.Epoch = h.Epoch
		d.Committed = h.Snapshot
		d.Frozen = false
		if err := r.save(d); err != nil {
			return err
		}
	}
	if _, err := r.call(ctx, "handoff-commit", PeerRequest{PairID: d.PairID, Handoff: h}); err != nil {
		return err
	}
	d.LastTransfer = h.ID
	d.Transfer = nil
	return r.save(d)
}
func (r *Replica) ReceiveHandoff(pair string, h Handoff, commit bool) error {
	r.op.Lock()
	defer r.op.Unlock()
	d := r.state()
	if pair != d.PairID || !d.Paired || h.To != r.node {
		return config.ErrRevisionConflict
	}
	if d.LastTransfer == h.ID {
		return nil
	}
	if !commit {
		if d.Pending != nil || d.Degraded || d.Writer != r.cfg.PeerID || h.Epoch != d.Epoch+1 || h.Snapshot.Checksum != config.WithWriter(d.Committed, h.Epoch, r.node, d.Committed.Rules).Checksum {
			return config.ErrRevisionConflict
		}
		if d.Transfer != nil && d.Transfer.ID != h.ID {
			return config.ErrRevisionConflict
		}
		h.Decided = false
		d.Transfer = &h
		return r.save(d)
	}
	if d.Transfer == nil || d.Transfer.ID != h.ID || d.Transfer.Snapshot.Checksum != h.Snapshot.Checksum {
		return config.ErrRevisionConflict
	}
	d.Writer = r.node
	d.Epoch = h.Epoch
	d.Committed = h.Snapshot
	d.Frozen = false
	d.Degraded = false
	d.Transfer = nil
	d.LastTransfer = h.ID
	return r.save(d)
}
func (r *Replica) Promote(fenced bool) error {
	if !fenced {
		return errors.New("confirm fencedPeer after stopping or isolating the previous writer")
	}
	r.op.Lock()
	defer r.op.Unlock()
	d := r.state()
	if !d.Paired {
		return ErrFrozen
	}
	if err := atomicJSON(r.path+".archive-"+nonce(), d); err != nil {
		return err
	}
	d.Epoch++
	d.Writer = r.node
	d.Committed = config.WithWriter(d.Committed, d.Epoch, r.node, d.Committed.Rules)
	d.Pending = nil
	d.Transfer = nil
	d.Frozen = false
	d.Degraded = true
	d.PairID = nonce()
	return r.save(d)
}

// Rejoin is explicit and archives all local state before adopting the selected
// peer. The peer must subsequently confirm matching state to leave degraded mode.
func (r *Replica) Rejoin(ctx context.Context, discard bool) error {
	if !discard {
		return errors.New("confirm archiveLocal to archive divergent local state and adopt the peer")
	}
	r.op.Lock()
	defer r.op.Unlock()
	out, err := r.call(ctx, "snapshot", PeerRequest{})
	if err != nil {
		return err
	}
	if out.Snapshot == nil || out.State == nil || !out.State.Paired || out.State.PendingID != "" || out.State.Transferring || out.State.Writer != r.cfg.PeerID {
		return errors.New("peer must be an initialized configuration writer with no pending transaction")
	}
	if err = out.Snapshot.Verify(); err != nil {
		return err
	}
	if out.Snapshot.ClusterID != r.cfg.ID {
		return config.ErrRevisionConflict
	}
	d := r.state()
	if err = atomicJSON(r.path+".archive-"+nonce(), d); err != nil {
		return err
	}
	d.PairID = out.State.PairID
	d.Paired = true
	d.Writer = out.State.Writer
	d.Epoch = out.State.Epoch
	d.Committed = *out.Snapshot
	d.Pending = nil
	d.Transfer = nil
	d.Frozen = false
	d.Degraded = false
	d.LastID = ""
	d.Requests = map[string]RequestRecord{}
	return r.save(d)
}
func (r *Replica) Resume(pair, checksum string, epoch int64) error {
	r.op.Lock()
	defer r.op.Unlock()
	d := r.state()
	if !d.Paired || pair != d.PairID || checksum != d.Committed.Checksum || epoch != d.Epoch || d.Pending != nil || d.Transfer != nil {
		return config.ErrRevisionConflict
	}
	if !d.Degraded {
		return nil
	}
	d.Degraded = false
	return r.save(d)
}

// The recovered writer reconciles only transactions for which the takeover
// peer can prove the old or proposed committed checksum, then remains frozen.
func (r *Replica) ObserveTakeover(peer PeerState) error {
	r.op.Lock()
	defer r.op.Unlock()
	d := r.state()
	if d.Writer != r.node || !d.Paired || peer.PairID != d.PairID || peer.Writer != r.node || peer.Epoch != d.Epoch {
		return nil
	}
	if d.Pending != nil {
		if peer.Checksum != d.Committed.Checksum && peer.Checksum != d.Pending.Next.Checksum {
			return config.ErrRevisionConflict
		}
		if err := atomicJSON(r.path+".archive-"+nonce(), d); err != nil {
			return err
		}
		if peer.Checksum == d.Pending.Next.Checksum {
			if err := r.acceptCommit(d.Pending.ID); err != nil {
				return err
			}
			d = r.state()
		} else {
			d.Pending = nil
		}
	}
	if peer.Checksum != d.Committed.Checksum {
		return config.ErrRevisionConflict
	}
	if d.Frozen {
		return nil
	}
	d.Frozen = true
	return r.save(d)
}

func (r *Replica) Rearm() error {
	r.op.Lock()
	defer r.op.Unlock()
	d := r.state()
	if !d.Paired || d.Pending != nil || d.Transfer != nil || d.Frozen || d.Degraded {
		return ErrFrozen
	}
	d.Takeover = false
	d.Switched = false
	return r.save(d)
}
