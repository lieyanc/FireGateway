package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func testConnection() ClusterConnection {
	return ClusterConnection{NodeID: "a", Cluster: &ClusterConfig{
		ID: "dmz", PeerID: "b", InitialWriter: "a", PeerURL: "https://peer.example:9090",
		PeerToken: strings.Repeat("test-only-secret", 3), Address: "192.0.2.10", PeerAddress: "192.0.2.11",
		RouterURL: "https://router.example/ubus", Username: "test", Password: "test-only-password",
		Redirects: []string{"dmz"}, PollInterval: 2, FailoverAfter: 10,
	}}
}

func TestConnectionStagingPreservesRuntimeAndOtherWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	s, _, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Update(func(c *Config) error {
		c.Forward = []Rule{{ID: "web", Type: "tcp", Status: "inactive", LocalPort: 8080, TargetHost: "localhost", TargetPort: 3000}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	before := s.Rules().Snapshot()
	if err := s.UpdateClusterConnection(func(c *ClusterConnection) error { *c = testConnection(); return nil }); err != nil {
		t.Fatal(err)
	}
	if s.Get().Cluster != nil || s.Get().Node.ID != "" || !reflect.DeepEqual(before, s.Rules().Snapshot()) {
		t.Fatal("saving connection changed the running gateway")
	}
	saved, pending := s.ClusterConnection()
	if !pending || saved.Cluster.PeerToken == "" {
		t.Fatal("saved connection missing")
	}
	saved.Cluster.PeerToken = "mutated copy"
	if got, _ := s.ClusterConnection(); got.Cluster.PeerToken == "mutated copy" {
		t.Fatal("connection getter aliases internal state")
	}
	_, err = s.Update(func(c *Config) error {
		c.Auth.Username = "new-admin"
		c.Logging.Level = "debug"
		port := 4000
		c.Node.RuleOverrides["web"] = RuleOverride{TargetPort: &port}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Changes made while waiting to restart must be adopted as well.
	_, err = s.Update(func(c *Config) error { c.Forward[0].TargetPort = 5000; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Reload(); err == nil {
		t.Fatal("reload applied a pending connection")
	}
	reopened, _, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.Get()
	if !reflect.DeepEqual(got.Cluster, testConnection().Cluster) || got.Node.ID != "a" || got.Auth.Username != "new-admin" || got.Logging.Level != "debug" || *got.Node.RuleOverrides["web"].TargetPort != 4000 {
		t.Fatal("later config writes lost pending connection or local settings")
	}
	if got.Forward[0].TargetPort != 5000 || reopened.Rules().Snapshot().ClusterID != "dmz" {
		t.Fatal("restart lost standalone rules")
	}
	if _, pending := reopened.ClusterConnection(); pending {
		t.Fatal("restart did not clear pending state")
	}
	if _, _, err := Open(path); err != nil {
		t.Fatalf("adoption is not repeatable: %v", err)
	}
	if mode, err := os.Stat(path); err != nil || mode.Mode().Perm() != 0600 {
		t.Fatal("credentials file is not private")
	}
}

func TestConnectionValidationAndIdentityProtection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	s, _, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateClusterConnection(func(c *ClusterConnection) error { *c = testConnection(); return nil }); err != nil {
		t.Fatal(err)
	}
	s, _, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	for _, change := range []func(*ClusterConnection){
		func(c *ClusterConnection) { c.Cluster = nil },
		func(c *ClusterConnection) { c.NodeID = "changed" },
		func(c *ClusterConnection) { c.Cluster.ID = "other" },
		func(c *ClusterConnection) { c.Cluster.PeerID = "other" },
		func(c *ClusterConnection) { c.Cluster.InitialWriter = "b" },
		func(c *ClusterConnection) { c.Cluster.PeerURL = "http://peer.example" },
		func(c *ClusterConnection) { c.Cluster.PeerToken = "short" },
	} {
		if err := s.UpdateClusterConnection(func(c *ClusterConnection) error { change(c); return nil }); err == nil {
			t.Fatal("invalid connection change accepted")
		}
		after, _ := os.ReadFile(path)
		if string(after) != string(before) {
			t.Fatal("failed validation changed disk")
		}
	}
	if err := s.UpdateClusterConnection(func(c *ClusterConnection) error { c.Cluster.PeerURL = "https://new-peer.example"; return nil }); err != nil {
		t.Fatal(err)
	}
	if s.Get().Cluster.PeerURL != testConnection().Cluster.PeerURL {
		t.Fatal("running transport changed before restart")
	}
}

func TestCancelPendingConnectionAndFailedSave(t *testing.T) {
	s, _, err := Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateClusterConnection(func(c *ClusterConnection) error { *c = testConnection(); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateClusterConnection(func(c *ClusterConnection) error { *c = ClusterConnection{}; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, pending := s.ClusterConnection(); pending {
		t.Fatal("cancel still pending")
	}
	// A non-file target reliably exercises write failure even when tests run as root.
	s.path = t.TempDir()
	if err := s.UpdateClusterConnection(func(c *ClusterConnection) error { *c = testConnection(); return nil }); err == nil {
		t.Fatal("expected write failure")
	}
	if got, pending := s.ClusterConnection(); pending || got.Cluster != nil {
		t.Fatal("failed save published pending state")
	}
}

func TestStandaloneAdoptionRejectsExistingCheckpointAndCluster(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	if err := writeJSONFile(path, NewRuleSet("", 1, nil)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".ha.json", []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := openRules(path, nil, "dmz"); err == nil {
		t.Fatal("adopted rules over an existing checkpoint")
	}
	if err := writeJSONFile(path, NewRuleSet("old-cluster", 1, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := openRules(path, nil, "new-cluster"); err == nil {
		t.Fatal("relabelled another cluster's rules")
	}
	data, _ := os.ReadFile(path)
	var snapshot RuleSet
	if err := json.Unmarshal(data, &snapshot); err != nil || snapshot.ClusterID != "old-cluster" {
		t.Fatal("failed adoption modified rules")
	}
}
