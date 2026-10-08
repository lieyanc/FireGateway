package api

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lieyanc/FireGateway/internal/config"
)

func connectionRequest() config.ClusterConnection {
	return config.ClusterConnection{NodeID: "a", Cluster: &config.ClusterConfig{
		ID: "dmz", PeerID: "b", InitialWriter: "a", PeerPort: 9091,
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
	body.Cluster.PeerPort = 9092
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
	if saved.Cluster.PeerToken != connectionRequest().Cluster.PeerToken || saved.Cluster.Password != connectionRequest().Cluster.Password || saved.Cluster.PeerPort != body.Cluster.PeerPort {
		t.Fatal("secret retention or save failed")
	}
	// Invalid drafts fail before persistence, including local CA checks.
	for _, mutate := range []func(*config.ClusterConnection){
		func(c *config.ClusterConnection) { c.NodeID = "invalid id" },
		func(c *config.ClusterConnection) { c.Cluster.ID = "invalid id" },
		func(c *config.ClusterConnection) { c.Cluster.PeerPort = 70000 },
		func(c *config.ClusterConnection) { c.Cluster.PeerPort = config.Default().API.Port },
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
	var routerCalls atomic.Int32
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { routerCalls.Add(1); w.WriteHeader(500) }))
	defer router.Close()
	// Nothing listens on the peer's node port: the probe reports it offline.
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	body := connectionRequest()
	body.Cluster.Address, body.Cluster.PeerAddress = "127.0.0.2", "127.0.0.1"
	body.Cluster.PeerPort, body.Cluster.RouterURL = closed.Addr().(*net.TCPAddr).Port, router.URL
	closed.Close()
	if code, out := h.do(t, "PUT", "/api/cluster/config", encodeConnection(t, body)); code != 200 {
		t.Fatalf("save: %d %v", code, out)
	}
	if code, out := h.do(t, "POST", "/api/cluster/test-peer", ""); code != 502 || out["error"] != "peer_unavailable" {
		t.Fatalf("probe of an absent peer: %d %v", code, out)
	}
	if routerCalls.Load() != 0 {
		t.Fatal("probe contacted the router")
	}
	if _, out := h.do(t, "GET", "/api/cluster/config", ""); out["restartRequired"] != true {
		t.Fatal("probe applied pending configuration")
	}
}
