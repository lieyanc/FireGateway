// Package quota counts each tenant's traffic per quota period, merges the
// peer node's count, and suspends tenants that exceed their monthly limit.
package quota

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lieyanc/FireGateway/internal/config"
)

type Usage = config.TenantUsage

const (
	checkInterval = 2 * time.Second
	saveInterval  = time.Minute
)

// Tracker keeps this node's counts and the latest counts reported by the peer.
type Tracker struct {
	store *config.Store
	path  string

	mu    sync.Mutex
	local map[string]Usage
	peer  map[string]Usage
	dirty bool
}

type saved struct {
	Local map[string]Usage `json:"local"`
	Peer  map[string]Usage `json:"peer"`
}

// New loads the counts saved in dataDir.
func New(store *config.Store, dataDir string) *Tracker {
	t := &Tracker{store: store, path: filepath.Join(dataDir, "tenant-usage.json"), local: map[string]Usage{}, peer: map[string]Usage{}}
	b, err := os.ReadFile(t.path)
	if err == nil {
		var f saved
		if err = json.Unmarshal(b, &f); err == nil {
			if f.Local != nil {
				t.local = f.Local
			}
			if f.Peer != nil {
				t.peer = f.Peer
			}
		}
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("failed to load tenant usage", "err", err)
	}
	return t
}

// Add counts transferred bytes per tenant.
func (t *Tracker) Add(bytes map[string]int64) {
	a := t.store.Access()
	if a == nil {
		return
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, n := range bytes {
		tn := a.Tenant(id)
		if tn == nil {
			continue
		}
		since := tn.PeriodStart(now)
		u := t.local[id]
		if !u.Since.Equal(since) {
			u = Usage{Since: since}
		}
		u.Bytes += n
		t.local[id] = u
		t.dirty = true
	}
}

// Local returns this node's counts, for the peer.
func (t *Tracker) Local() map[string]Usage {
	t.mu.Lock()
	defer t.mu.Unlock()
	return maps.Clone(t.local)
}

// SetPeer records the counts last reported by the peer.
func (t *Tracker) SetPeer(m map[string]Usage) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !maps.Equal(t.peer, m) {
		t.peer = maps.Clone(m)
		t.dirty = true
	}
}

// Used returns the tenant's traffic in its current period on both nodes.
func (t *Tracker) Used(tn *config.Tenant) Usage {
	since := tn.PeriodStart(time.Now())
	t.mu.Lock()
	defer t.mu.Unlock()
	out := Usage{Since: since}
	for _, u := range []Usage{t.local[tn.ID], t.peer[tn.ID]} {
		if u.Since.Equal(since) {
			out.Bytes += u.Bytes
		}
	}
	return out
}

// Over returns the tenants that reached their monthly traffic limit.
func (t *Tracker) Over() map[string]bool {
	out := map[string]bool{}
	a := t.store.Access()
	if a == nil {
		return out
	}
	for i := range a.Tenants {
		tn := &a.Tenants[i]
		if limit := tn.Quota.MonthlyBytes; limit > 0 && t.Used(tn).Bytes >= limit {
			out[tn.ID] = true
		}
	}
	return out
}

// Run applies suspensions until ctx is done, saving counts periodically;
// the caller saves them once more on shutdown.
func (t *Tracker) Run(ctx context.Context, suspend func(map[string]bool)) {
	check := time.NewTicker(checkInterval)
	defer check.Stop()
	save := time.NewTicker(saveInterval)
	defer save.Stop()
	suspend(t.Over())
	for {
		select {
		case <-ctx.Done():
			return
		case <-check.C:
			suspend(t.Over())
		case <-save.C:
			if err := t.Save(); err != nil {
				slog.Warn("failed to save tenant usage", "err", err)
			}
		}
	}
}

// Save writes the counts if they changed, dropping deleted tenants.
func (t *Tracker) Save() error {
	a := t.store.Access()
	t.mu.Lock()
	if !t.dirty {
		t.mu.Unlock()
		return nil
	}
	if a != nil {
		for _, m := range []map[string]Usage{t.local, t.peer} {
			for id := range m {
				if a.Tenant(id) == nil {
					delete(m, id)
				}
			}
		}
	}
	b, err := json.MarshalIndent(saved{t.local, t.peer}, "", "  ")
	t.dirty = false
	t.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(t.path), ".tenant-usage-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), t.path)
}
