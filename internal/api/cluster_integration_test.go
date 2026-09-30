package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/lieyanc/FireGateway/internal/auth"
	"github.com/lieyanc/FireGateway/internal/config"
	"github.com/lieyanc/FireGateway/internal/events"
	"github.com/lieyanc/FireGateway/internal/gateway"
	"github.com/lieyanc/FireGateway/internal/ha"
	"github.com/lieyanc/FireGateway/internal/metrics"
)

type unusedRouter struct{}

func (unusedRouter) Read(context.Context) (ha.Ingress, error) {
	return ha.Ingress{}, errors.New("router intentionally unavailable")
}
func (unusedRouter) Switch(context.Context, ha.Ingress, string) error {
	return errors.New("unexpected router call")
}

func TestRulesReplicateThroughAuthenticatedPeerAPI(t *testing.T) {
	ta := httptest.NewUnstartedServer(nil)
	tb := httptest.NewUnstartedServer(nil)
	makeNode := func(id, peer, ip, peerIP string, ts, other *httptest.Server) (*harness, *ha.Controller) {
		cfg := config.Default()
		cfg.Node.ID = id
		cfg.Forward = nil
		cfg.Cluster = &config.ClusterConfig{ID: "api-test", PeerID: peer, PeerURL: "http://" + other.Listener.Addr().String(), PeerToken: strings.Repeat("pair-test-token-", 3), Address: ip, PeerAddress: peerIP, InitialWriter: "a", RouterURL: "http://router/ubus", Username: "test", Password: "test-only", Redirects: []string{"dmz"}, PollInterval: 2, FailoverAfter: 10, AllowHTTPPeer: true}
		dir := t.TempDir()
		path := filepath.Join(dir, "config.json")
		b, _ := json.Marshal(cfg)
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
		store, _, err := config.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		broker := events.NewBroker()
		mgr := gateway.New(store, broker)
		mgr.Start()
		client, err := ha.NewPeerClient(*cfg.Cluster, id)
		if err != nil {
			t.Fatal(err)
		}
		ctrl, err := ha.NewController(store, mgr, unusedRouter{}, client)
		if err != nil {
			t.Fatal(err)
		}
		authSvc := auth.New(store)
		s := &Server{Deps: Deps{Store: store, Manager: mgr, Cluster: ctrl, Auth: authSvc, Broker: broker, Sampler: metrics.New(mgr, store, broker), Started: time.Now()}, boot: store.Get()}
		s.static = newStaticFS(fstest.MapFS{"index.html": {Data: []byte("app")}})
		ts.Config.Handler = s.routes()
		ts.Start()
		t.Cleanup(func() { ts.Close(); mgr.Stop(); ctrl.Close() })
		jar, _ := cookiejar.New(nil)
		return &harness{ts: ts, client: &http.Client{Jar: jar}, auth: authSvc}, ctrl
	}
	a, ca := makeNode("a", "b", "127.0.0.1", "127.0.0.2", ta, tb)
	b, cb := makeNode("b", "a", "127.0.0.2", "127.0.0.1", tb, ta)
	a.setup(t)
	b.setup(t)
	if code, out := a.do(t, "POST", "/api/cluster/bootstrap", ""); code != 200 {
		t.Fatalf("bootstrap: %d %v", code, out)
	}
	if err := ca.SyncStep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := cb.SyncStep(context.Background()); err != nil {
		t.Fatal(err)
	}
	initial := ca.Status().Checksum
	body := `{"type":"tcp","status":"inactive","localPort":8080,"targetHost":"127.0.0.1","targetPort":3000}`
	headers := []string{"If-Match", initial, "Idempotency-Key", "create-request-0001"}
	if code, out := b.do(t, "POST", "/api/rules", body, headers...); code != 201 || out["id"] != "1" {
		t.Fatalf("standby proxy create: %d %v", code, out)
	}
	committed := ca.Status().Checksum
	if code, out := b.do(t, "POST", "/api/rules", body, headers...); code != 201 || out["id"] != "1" {
		t.Fatalf("idempotent retry: %d %v", code, out)
	}
	if committed != ca.Status().Checksum {
		t.Fatal("retry published another revision")
	}
	headers[3] = "stale-request-0002"
	if code, _ := a.do(t, "POST", "/api/rules", body, headers...); code != 409 {
		t.Fatalf("stale expected version accepted: %d", code)
	}
	if code, _ := a.do(t, "POST", "/api/rules", body); code != 409 {
		t.Fatal("cluster write accepted without version/idempotency headers")
	}
	if err := cb.SyncStep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if code, out := b.do(t, "GET", "/api/rules/1", ""); code != 200 || out["targetPort"] != float64(3000) {
		t.Fatalf("peer did not materialize committed rules: %d %v", code, out)
	}
	if code, out := b.do(t, "PUT", "/api/node", `{"id":"b","ruleOverrides":{"1":{"targetPort":4000}}}`); code != 200 {
		t.Fatalf("node override: %d %v", code, out)
	}
	if code, out := b.do(t, "GET", "/api/rules/1", ""); code != 200 || out["targetPort"] != float64(3000) {
		t.Fatal("node override changed shared target")
	}
	if ca.Status().Checksum != cb.Status().Checksum || ca.Status().Checksum != committed {
		t.Fatal("local override changed shared version")
	}
	if code, out := a.do(t, "POST", "/api/cluster/transactions/create-request-0001", ""); code != 404 && code != 405 {
		t.Fatalf("incorrect transaction method accepted: %d %v", code, out)
	}
	if code, out := a.do(t, "GET", "/api/cluster/transactions/create-request-0001", ""); code != 200 || out["committed"] != true {
		t.Fatalf("missing transaction receipt: %d %v", code, out)
	}
	// Authenticated web users cannot bypass dedicated node authentication.
	if code, _ := b.do(t, "POST", "/internal/ha/v1", `{"protocol":1}`); code != 401 {
		t.Fatalf("peer endpoint accepted web cookie: %d", code)
	}
	if code, _ := b.do(t, "POST", "/api/cluster/promote", `{"archiveLocal":true}`); code == 200 {
		t.Fatal("archive confirmation substituted for fencing confirmation")
	}
	// A batch is one replicated publication, including its partial-item report.
	headers = []string{"If-Match", committed, "Idempotency-Key", "batch-request-0003"}
	before := ca.Status().DesiredRevision
	if code, out := b.do(t, "POST", "/api/rules/batch", `{"action":"delete","ids":["1","missing"]}`, headers...); code != 200 || len(out["ok"].([]any)) != 1 || len(out["failed"].([]any)) != 1 {
		t.Fatalf("batch: %d %v", code, out)
	}
	if ca.Status().DesiredRevision != before+1 {
		t.Fatal("batch did not publish exactly once")
	}
}
