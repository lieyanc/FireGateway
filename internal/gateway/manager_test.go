package gateway

import (
	"errors"
	"net"
	"path/filepath"
	"testing"

	"github.com/lieyanc/FireGateway/internal/config"
	"github.com/lieyanc/FireGateway/internal/events"
)

func newManager(t *testing.T) (*Manager, *config.Store) {
	t.Helper()
	store, _, err := config.Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(store, events.NewBroker())
	m.Start()
	t.Cleanup(m.Stop)
	return m, store
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func tcpRule(port int) config.Rule {
	return config.Rule{Type: "tcp", LocalHost: "127.0.0.1", LocalPort: port, TargetHost: "127.0.0.1", TargetPort: 9}
}

func TestCreateUpdateDelete(t *testing.T) {
	m, store := newManager(t)
	r, err := m.Create(tcpRule(freePort(t)))
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != "1" || r.Status != config.StatusActive {
		t.Fatalf("unexpected rule: %+v", r)
	}
	rn := m.Runner(r.ID)
	if rt := m.Runtime(&r); rn == nil || rt.State != StateRunning {
		t.Fatalf("rule not running: %+v", rt)
	}

	// Policy-only change keeps the same runner (no re-bind).
	r.Name = "renamed"
	r.Limits = &config.Limits{MaxConnections: 5}
	if _, err := m.Update(r.ID, r); err != nil {
		t.Fatal(err)
	}
	if m.Runner(r.ID) != rn || rn.Name() != "renamed" {
		t.Fatal("policy update should be applied in place")
	}

	// Changing the listen port re-binds.
	r.LocalPort = freePort(t)
	if _, err := m.Update(r.ID, r); err != nil {
		t.Fatal(err)
	}
	if m.Runner(r.ID) == rn {
		t.Fatal("port change should restart the runner")
	}

	if _, err := m.SetActive(r.ID, false); err != nil {
		t.Fatal(err)
	}
	if m.Runner(r.ID) != nil {
		t.Fatal("disabled rule still running")
	}
	if err := m.Delete(r.ID); err != nil {
		t.Fatal(err)
	}
	if len(store.Get().Forward) != 0 {
		t.Fatal("rule not removed from config")
	}
	if err := m.Delete(r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestConflicts(t *testing.T) {
	m, _ := newManager(t)
	p := freePort(t)
	if _, err := m.Create(tcpRule(p)); err != nil {
		t.Fatal(err)
	}
	var ce *ConflictError
	dup := tcpRule(p)
	dup.LocalHost = "0.0.0.0"
	if _, err := m.Create(dup); !errors.As(err, &ce) {
		t.Fatalf("expected listen conflict, got %v", err)
	}
	// An inactive rule may share the address.
	dup.Status = config.StatusInactive
	if _, err := m.Create(dup); err != nil {
		t.Fatalf("inactive duplicate rejected: %v", err)
	}
	withID := tcpRule(freePort(t))
	withID.ID = "1"
	if _, err := m.Create(withID); !errors.As(err, &ce) {
		t.Fatalf("expected duplicate id conflict, got %v", err)
	}
	var fe *config.FieldError
	if _, err := m.Create(config.Rule{Type: "tcp"}); !errors.As(err, &fe) {
		t.Fatalf("expected validation error, got %v", err)
	}
}

func TestImportAndBatch(t *testing.T) {
	m, store := newManager(t)
	a, _ := m.Create(tcpRule(freePort(t)))
	res, err := m.Import([]config.Rule{tcpRule(freePort(t)), {ID: a.ID, Type: "udp", LocalHost: "127.0.0.1",
		LocalPort: freePort(t), TargetHost: "127.0.0.1", TargetPort: 9}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 || res.Updated != 1 || len(store.Get().Forward) != 2 {
		t.Fatalf("unexpected import result %+v, rules=%d", res, len(store.Get().Forward))
	}
	if got, _ := m.Get(a.ID); got.Type != "udp" || m.Runner(a.ID).Type != "udp" {
		t.Fatal("merge did not update existing rule")
	}

	// A bad rule aborts the whole import.
	var fe *config.FieldError
	if _, err := m.Import([]config.Rule{{Type: "bogus"}}, true); !errors.As(err, &fe) || fe.Field != "forward[0].type" {
		t.Fatalf("expected indexed validation error, got %v", err)
	}
	if len(store.Get().Forward) != 2 {
		t.Fatal("failed import modified config")
	}

	br, err := m.Batch("disable", []config.RuleID{"1", "2", "missing"})
	if err != nil || len(br.OK) != 2 || len(br.Failed) != 1 {
		t.Fatalf("batch: %+v %v", br, err)
	}
	if len(m.Runners()) != 0 {
		t.Fatal("batch disable left runners")
	}

	res, err = m.Import(nil, true)
	if err != nil || res.Removed != 2 || len(store.Get().Forward) != 0 {
		t.Fatalf("replace with empty: %+v %v", res, err)
	}
}
