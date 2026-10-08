package quota

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/lieyanc/FireGateway/internal/config"
)

func newStore(t *testing.T, tenants ...config.Tenant) *config.Store {
	t.Helper()
	store, _, err := config.Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Update(func(c *config.Config) error {
		c.Access = &config.Access{SessionSecret: "0123456789abcdef0123", Users: []config.User{}, Tenants: tenants, Tokens: []config.APIToken{}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func tenant(id string, limit int64) config.Tenant {
	return config.Tenant{ID: id, Name: id, PortRanges: []config.PortRange{}, Quota: config.Quota{MonthlyBytes: limit}}
}

func TestTrackerCountsBothNodesInTheCurrentPeriod(t *testing.T) {
	store := newStore(t, tenant("team1", 1000), tenant("team2", 0))
	dir := t.TempDir()
	tr := New(store, dir)
	tr.Add(map[string]int64{"team1": 600, "team2": 1 << 40, "ghost": 5})
	if over := tr.Over(); len(over) != 0 {
		t.Fatalf("over before the limit: %v", over)
	}
	since := store.Access().Tenant("team1").PeriodStart(time.Now())
	tr.SetPeer(map[string]Usage{"team1": {Since: since, Bytes: 400}})
	if over := tr.Over(); !over["team1"] || over["team2"] {
		t.Fatalf("combined usage not enforced: %v", over)
	}
	// Counts from an earlier period do not apply.
	tr.SetPeer(map[string]Usage{"team1": {Since: since.AddDate(0, -1, 0), Bytes: 400}})
	if tr.Used(store.Access().Tenant("team1")).Bytes != 600 {
		t.Fatal("stale peer usage was counted")
	}
	if _, ok := tr.Local()["ghost"]; ok {
		t.Fatal("unknown tenant was counted")
	}

	// Counts survive a restart.
	if err := tr.Save(); err != nil {
		t.Fatal(err)
	}
	again := New(store, dir)
	if again.Used(store.Access().Tenant("team1")).Bytes != 600 {
		t.Fatal("usage was not persisted")
	}

	// A manual reset starts a new period on every node.
	_, err := store.Update(func(c *config.Config) error {
		at := time.Now().UTC()
		c.Access.Tenant("team1").UsageResetAt = &at
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := again.Used(store.Access().Tenant("team1")).Bytes; n != 0 {
		t.Fatalf("usage after reset: %d", n)
	}
	again.Add(map[string]int64{"team1": 10})
	if n := again.Used(store.Access().Tenant("team1")).Bytes; n != 10 {
		t.Fatalf("usage counted after reset: %d", n)
	}
}

func TestPeriodStartUsesResetDay(t *testing.T) {
	tn := config.Tenant{Quota: config.Quota{ResetDay: 15}}
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if got := tn.PeriodStart(at("2026-03-20T10:00:00Z")); !got.Equal(at("2026-03-15T00:00:00Z")) {
		t.Fatalf("after reset day: %v", got)
	}
	if got := tn.PeriodStart(at("2026-03-10T10:00:00Z")); !got.Equal(at("2026-02-15T00:00:00Z")) {
		t.Fatalf("before reset day: %v", got)
	}
}
