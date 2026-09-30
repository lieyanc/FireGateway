package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var ErrRevisionConflict = errors.New("rule revision changed; refresh and retry")
var ErrClusterUnavailable = errors.New("cluster is not synchronized; rule writes are unavailable")

// RuleSet is an immutable, complete publication. Revision is scoped to ClusterID.
type RuleSet struct {
	SchemaVersion  int    `json:"schemaVersion"`
	ClusterID      string `json:"clusterId"`
	Revision       int64  `json:"revision"`
	WriterEpoch    int64  `json:"writerEpoch,omitempty"`
	WriterID       string `json:"writerId,omitempty"`
	ParentChecksum string `json:"parentChecksum,omitempty"`
	Checksum       string `json:"checksum"`
	Rules          []Rule `json:"rules"`
}

func NewRuleSet(clusterID string, revision int64, rules []Rule) RuleSet {
	if rules == nil {
		rules = []Rule{}
	}
	s := RuleSet{SchemaVersion: 1, ClusterID: clusterID, Revision: revision, Rules: rules}
	s.Checksum = s.digest()
	return s
}

func (s RuleSet) digest() string {
	s.Checksum = ""
	b, _ := json.Marshal(s)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (s RuleSet) Verify() error {
	if b, _ := json.Marshal(s); len(b) > 256<<10 {
		return errors.New("rule snapshot exceeds the 256 KiB limit")
	}
	if (s.SchemaVersion != 1 && s.SchemaVersion != 2) || s.Revision < 1 || s.Rules == nil {
		return errors.New("invalid rule snapshot version or rules")
	}
	if s.SchemaVersion == 2 && (s.WriterEpoch < 1 || ValidateID(RuleID(s.WriterID)) != nil) {
		return errors.New("invalid writer authorization")
	}
	if s.Checksum != s.digest() {
		return errors.New("rule snapshot checksum mismatch")
	}
	seen := make(map[RuleID]bool)
	for _, r := range s.Rules {
		if err := r.Validate(); err != nil {
			return fmt.Errorf("rule %s: %w", r.ID, err)
		}
		if seen[r.ID] {
			return fmt.Errorf("duplicate rule id %s", r.ID)
		}
		seen[r.ID] = true
	}
	return nil
}

// RuleBackend provides the single authoritative compare-and-swap operation.
// A successful commit may precede local cache persistence; the next sync recovers it.
type RuleBackend interface {
	Commit(previous, next RuleSet) (RuleSet, error)
}

type RuleStore struct {
	mu        sync.Mutex
	rw        sync.RWMutex
	path      string
	cur       RuleSet
	backend   RuleBackend
	clustered bool
}

func openRules(path string, legacy []Rule, clusterID string) (*RuleStore, error) {
	s := &RuleStore{path: path, clustered: clusterID != ""}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		s.cur = NewRuleSet(clusterID, 1, legacy)
		if err := s.cur.Verify(); err != nil {
			return nil, err
		}
		return s, writeJSONFile(path, s.cur)
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.cur); err != nil {
		return nil, err
	}
	if err := s.cur.Verify(); err != nil {
		return nil, err
	}
	if s.cur.ClusterID != clusterID {
		return nil, fmt.Errorf("rules file belongs to cluster %q, configured cluster is %q; use a separate rulesFile when joining or leaving a cluster", s.cur.ClusterID, clusterID)
	}
	if len(legacy) > 0 && NewRuleSet(clusterID, s.cur.Revision, legacy).Checksum != s.cur.Checksum {
		return nil, errors.New("both inline forward and rulesFile contain different rules; resolve the conflict before migration")
	}
	return s, nil
}

func (s *RuleStore) Path() string { return s.path }
func (s *RuleStore) Snapshot() RuleSet {
	s.rw.RLock()
	defer s.rw.RUnlock()
	b, _ := json.Marshal(s.cur)
	var out RuleSet
	_ = json.Unmarshal(b, &out)
	return out
}
func (s *RuleStore) SetBackend(b RuleBackend) { s.mu.Lock(); defer s.mu.Unlock(); s.backend = b }

func (s *RuleStore) Update(previous RuleSet, rules []Rule) (RuleSet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.Snapshot()
	if previous.Checksum != cur.Checksum {
		return cur, ErrRevisionConflict
	}
	next := NewRuleSet(cur.ClusterID, cur.Revision+1, rules)
	if err := next.Verify(); err != nil {
		return cur, err
	}
	if s.clustered && s.backend == nil {
		return cur, ErrClusterUnavailable
	}
	if s.backend != nil {
		var err error
		next, err = s.backend.Commit(cur, next)
		if err != nil {
			return cur, err
		}
		if err = next.Verify(); err != nil {
			return cur, err
		}
	}
	if err := s.install(next); err != nil {
		return cur, err
	}
	return next, nil
}

func (s *RuleStore) Install(next RuleSet) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := next.Verify(); err != nil {
		return err
	}
	cur := s.Snapshot()
	if next.ClusterID != cur.ClusterID {
		return errors.New("cluster id mismatch")
	}
	if next.WriterEpoch < cur.WriterEpoch || (next.WriterEpoch == cur.WriterEpoch && (next.Revision < cur.Revision || (next.Revision == cur.Revision && next.Checksum != cur.Checksum))) {
		return ErrRevisionConflict
	}
	if next.Checksum == cur.Checksum {
		return nil
	}
	return s.install(next)
}

func (s *RuleStore) install(next RuleSet) error {
	if err := writeJSONFile(s.path, next); err != nil {
		return fmt.Errorf("save rules: %w", err)
	}
	s.rw.Lock()
	b, _ := json.Marshal(next)
	_ = json.Unmarshal(b, &s.cur)
	s.rw.Unlock()
	return nil
}

func (s *RuleStore) Reload() error {
	if s.clustered {
		return errors.New("cluster rules must be changed through the management API")
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var next RuleSet
	if err := json.Unmarshal(b, &next); err != nil {
		return err
	}
	return s.Install(next)
}

func writeJSONFile(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".firegateway-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(append(b, '\n')); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	// Persist the directory entry as well as the file contents on Unix.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// WithWriter constructs a publication under an explicitly authorized writer.
func WithWriter(previous RuleSet, epoch int64, writer string, rules []Rule) RuleSet {
	rev := previous.Revision + 1
	if previous.WriterEpoch != epoch {
		rev = 1
	}
	s := NewRuleSet(previous.ClusterID, rev, rules)
	s.SchemaVersion = 2
	s.WriterEpoch = epoch
	s.WriterID = writer
	s.ParentChecksum = previous.Checksum
	s.Checksum = s.digest()
	return s
}

// Restore replaces the materialized view from the durable replication journal.
// Callers must authorize pairing/recovery before invoking it; it is not an API
// for arbitrary incoming snapshots.
func (s *RuleStore) Restore(next RuleSet) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := next.Verify(); err != nil {
		return err
	}
	if next.ClusterID != s.Snapshot().ClusterID {
		return errors.New("cluster id mismatch")
	}
	if next.Checksum == s.Snapshot().Checksum {
		return nil
	}
	return s.install(next)
}
