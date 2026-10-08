package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSplitMigrationAndPrivateNodeSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := `{"auth":{"username":"admin","passwordHash":"private"},"forward":[{"id":"web","type":"tcp","status":"active","localHost":"0.0.0.0","localPort":8080,"targetHost":"127.0.0.1","targetPort":3000}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s, _, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), `"forward"`) {
		t.Fatal("legacy rules still stored with node secrets")
	}
	b, _ = os.ReadFile(s.Rules().Path())
	if strings.Contains(string(b), "private") || strings.Contains(string(b), "passwordHash") {
		t.Fatal("secrets leaked into shared rules")
	}
	if !strings.Contains(string(b), `"web"`) {
		t.Fatal("migration lost the rule")
	}
	rev := s.Rules().Snapshot().Revision
	_, err = s.Update(func(c *Config) error {
		ip := "127.0.0.2"
		c.Node.RuleOverrides["web"] = RuleOverride{LocalHost: &ip}
		c.Auth.Username = "renamed"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Rules().Snapshot().Revision != rev {
		t.Fatal("local settings changed shared revision")
	}
	reopened, state, err := Open(path)
	if err != nil || state != Loaded || len(reopened.Get().Forward) != 1 {
		t.Fatalf("reopen: %v %v", state, err)
	}
	r, err := reopened.Get().Node.Resolve(reopened.Get().Forward[0])
	if err != nil || r.LocalHost != "127.0.0.2" || reopened.Get().Forward[0].LocalHost != "0.0.0.0" {
		t.Fatalf("override changed shared rule: %+v %v", r, err)
	}
}

func TestRuleSnapshotConflictsAndCorruption(t *testing.T) {
	s, _, err := Open(filepath.Join(t.TempDir(), "c.json"))
	if err != nil {
		t.Fatal(err)
	}
	first := s.Rules().Snapshot()
	next := NewRuleSet("", 2, []Rule{{ID: "web", Type: "tcp", Status: "active", LocalHost: "127.0.0.1", LocalPort: 8000, TargetHost: "127.0.0.1", TargetPort: 9000}}, nil)
	if err := s.Rules().Install(next); err != nil {
		t.Fatal(err)
	}
	if err := s.Rules().Install(first); err != ErrRevisionConflict {
		t.Fatalf("stale snapshot: %v", err)
	}
	if _, err := s.Rules().Update(first, nil, nil); err != ErrRevisionConflict {
		t.Fatalf("stale write: %v", err)
	}
	next.Rules[0].TargetPort = 9999
	if err := s.Rules().Install(next); err == nil {
		t.Fatal("accepted bad checksum")
	}
	if s.Rules().Snapshot().Rules[0].TargetPort != 9000 {
		t.Fatal("failed update changed live rules")
	}
}

func TestMigrationConflictPreservesOriginal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	s, _, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c := s.Get()
	c.Forward = []Rule{{ID: "different", Type: "tcp", Status: "inactive", LocalPort: 1, TargetHost: "localhost", TargetPort: 2}}
	b, _ := json.Marshal(c)
	_ = os.WriteFile(path, b, 0o600)
	if _, _, err := Open(path); err == nil {
		t.Fatal("silently discarded conflicting inline rules")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(b) {
		t.Fatal("failed migration changed original config")
	}
}

func TestNodePortOverrideRejectsRanges(t *testing.T) {
	port := 4000
	node := NodeConfig{RuleOverrides: map[RuleID]RuleOverride{"range": {TargetPort: &port}}}
	_, err := node.Resolve(Rule{ID: "range", Type: "tcp", Status: "active", LocalPortRange: []int{1000, 1002}, TargetHost: "localhost", TargetPortRange: []int{2000, 2002}})
	if err == nil {
		t.Fatal("single target port silently replaced a range")
	}
}
