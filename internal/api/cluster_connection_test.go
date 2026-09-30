package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lieyanc/FireGateway/internal/config"
	"github.com/lieyanc/FireGateway/internal/ha"
)

func connectionRequest() config.ClusterConnection {
	return config.ClusterConnection{NodeID: "a", Cluster: &config.ClusterConfig{
		ID: "dmz", PeerID: "b", InitialWriter: "a", PeerURL: "https://peer.example:9090",
		PeerToken: strings.Repeat("test-only-secret", 3), Address: "192.0.2.10", PeerAddress: "192.0.2.11",
		RouterURL: "https://router.example/ubus", Username: "test", Password: "test-only-password",
		Redirects: []string{"dmz"}, PollInterval: 2, FailoverAfter: 10,
	}}
}

func encodeConnection(t *testing.T, c config.ClusterConnection) string {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestWebConnectionSettingsAuthSecretsAndPersistence(t *testing.T) {
	h := newHarness(t)
	for _, req := range [][2]string{{"GET", "/api/cluster/config"}, {"PUT", "/api/cluster/config"}, {"POST", "/api/cluster/test-peer"}} {
		if code, _ := h.do(t, req[0], req[1], "{}"); code != 401 {
			t.Fatalf("public connection endpoint: %v %d", req, code)
		}
	}
	h.setup(t)
	if code, out := h.do(t, "GET", "/api/cluster/config", ""); code != 200 || out["cluster"] != nil || out["restartRequired"] != false {
		t.Fatalf("initial: %d %v", code, out)
	}
	body := connectionRequest()
	if code, _ := h.do(t, "PUT", "/api/cluster/config", encodeConnection(t, body), "Origin", "https://elsewhere.example"); code != 403 {
		t.Fatal("cross-site config save accepted")
	}
	code, out := h.do(t, "PUT", "/api/cluster/config", encodeConnection(t, body))
	if code != 200 || out["restartRequired"] != true || out["peerTokenConfigured"] != true || out["routerPasswordConfigured"] != true {
		t.Fatalf("save: %d %v", code, out)
	}
	cluster := out["cluster"].(map[string]any)
	if cluster["peerToken"] != "" || cluster["password"] != "" {
		t.Fatal("save response exposed secrets")
	}
	// Redaction must not modify stored secrets. A second edit preserves them.
	body.Cluster.PeerToken, body.Cluster.Password = "", ""
	body.Cluster.PeerURL = "https://changed-peer.example"
	if code, out := h.do(t, "PUT", "/api/cluster/config", encodeConnection(t, body)); code != 200 {
		t.Fatalf("retain secrets: %d %v", code, out)
	}
	if code, out := h.do(t, "GET", "/api/cluster", ""); code != 200 || out["role"] != "standalone" {
		t.Fatal("saved connection changed live role")
	}
	if code, out := h.do(t, "PUT", "/api/node", `{"id":"","ruleOverrides":{}}`); code != 200 {
		t.Fatalf("override while pending: %d %v", code, out)
	}
	_, overview := h.do(t, "GET", "/api/overview", "")
	path := overview["configPath"].(string)
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved config.Config
	if err := json.Unmarshal(disk, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Cluster.PeerToken != connectionRequest().Cluster.PeerToken || saved.Cluster.Password != connectionRequest().Cluster.Password || saved.Cluster.PeerURL != body.Cluster.PeerURL {
		t.Fatal("secret retention or save failed")
	}
	// Invalid drafts fail before persistence, including local CA checks.
	for _, mutate := range []func(*config.ClusterConnection){
		func(c *config.ClusterConnection) { c.NodeID = "invalid id" },
		func(c *config.ClusterConnection) { c.Cluster.ID = "invalid id" },
		func(c *config.ClusterConnection) { c.Cluster.PeerURL = "http://peer.example" },
		func(c *config.ClusterConnection) { c.Cluster.PeerCAFile = t.TempDir() + "/missing.pem" },
		func(c *config.ClusterConnection) { c.Cluster.CAFile = t.TempDir() + "/missing.pem" },
	} {
		bad := connectionRequest()
		mutate(&bad)
		if code, out := h.do(t, "PUT", "/api/cluster/config", encodeConnection(t, bad)); code != 400 {
			t.Fatalf("invalid draft: %d %v", code, out)
		}
		after, _ := os.ReadFile(path)
		if string(after) != string(disk) {
			t.Fatal("invalid draft changed saved connection")
		}
	}
	req, _ := http.NewRequest("GET", h.ts.URL+"/api/cluster/config", nil)
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("connection response may be cached")
	}
}

func TestWebPeerProbeUsesSavedSettingsWithoutMutations(t *testing.T) {
	h := newHarness(t)
	h.setup(t)
	if code, _ := h.do(t, "POST", "/api/cluster/test-peer", ""); code != 400 {
		t.Fatal("test accepted absent settings")
	}
	var peerCalls, routerCalls atomic.Int32
	var wrongIdentity atomic.Bool
	body := connectionRequest()
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peerCalls.Add(1)
		var req ha.PeerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/internal/ha/v1" || req.Method != "heartbeat" || req.NodeID != "a" || req.Receiver != "b" || req.ClusterID != "dmz" || r.Header.Get("Authorization") != "Bearer "+connectionRequest().Cluster.PeerToken {
			t.Error("invalid test request or credentials")
		}
		id := "b"
		if wrongIdentity.Load() {
			id = "impostor"
		}
		_ = json.NewEncoder(w).Encode(ha.PeerResponse{Protocol: 1, NodeID: id, State: &ha.PeerState{NodeID: id}})
	}))
	defer peer.Close()
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { routerCalls.Add(1); w.WriteHeader(500) }))
	defer router.Close()
	body.Cluster.PeerURL, body.Cluster.RouterURL, body.Cluster.AllowHTTPPeer = peer.URL, router.URL, true
	if code, out := h.do(t, "PUT", "/api/cluster/config", encodeConnection(t, body)); code != 200 {
		t.Fatalf("save: %d %v", code, out)
	}
	if peerCalls.Load() != 0 || routerCalls.Load() != 0 {
		t.Fatal("saving settings contacted a device")
	}
	if code, out := h.do(t, "POST", "/api/cluster/test-peer", ""); code != 200 || out["connected"] != true || out["nodeId"] != "b" {
		t.Fatalf("probe: %d %v", code, out)
	}
	wrongIdentity.Store(true)
	if code, _ := h.do(t, "POST", "/api/cluster/test-peer", ""); code != 502 {
		t.Fatal("accepted mismatched peer identity")
	}
	if peerCalls.Load() != 2 || routerCalls.Load() != 0 {
		t.Fatal("probe did more than peer heartbeat")
	}
	if _, out := h.do(t, "GET", "/api/cluster/config", ""); out["restartRequired"] != true {
		t.Fatal("probe applied pending configuration")
	}
}
