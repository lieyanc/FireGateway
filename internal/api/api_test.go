package api

import (
	"encoding/json"
	"io"
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
	"github.com/lieyanc/FireGateway/internal/metrics"
	"github.com/lieyanc/FireGateway/internal/updater"
)

type harness struct {
	ts     *httptest.Server
	client *http.Client
	auth   *auth.Service
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	store, _, err := config.Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	broker := events.NewBroker()
	mgr := gateway.New(store, broker)
	t.Cleanup(mgr.Stop)
	a := auth.New(store)
	s := &Server{Deps: Deps{
		Store: store, Manager: mgr, Sampler: metrics.New(mgr, store, broker), Broker: broker, Auth: a,
		Updater: updater.New(func() updater.Config { return updater.Config{} }, func() string { return t.TempDir() }, nil, updater.RestartHooks{}),
		Started: time.Now(),
	}, boot: store.Get()}
	s.static = newStaticFS(fstest.MapFS{
		"index.html":    {Data: []byte("<html>app</html>")},
		"assets/app.js": {Data: []byte(strings.Repeat("console.log(1);", 200))},
	})
	ts := httptest.NewServer(s.routes())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	return &harness{ts: ts, client: &http.Client{Jar: jar}, auth: a}
}

func (h *harness) do(t *testing.T, method, path, body string, hdr ...string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, h.ts.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var out map[string]any
	json.Unmarshal(data, &out)
	return resp.StatusCode, out
}

func (h *harness) setup(t *testing.T) {
	t.Helper()
	body := `{"setupToken":"` + h.auth.SetupToken() + `","username":"admin","password":"password123"}`
	if code, out := h.do(t, "POST", "/api/auth/setup", body); code != 200 {
		t.Fatalf("setup: %d %v", code, out)
	}
}

func TestAuthFlow(t *testing.T) {
	h := newHarness(t)
	if code, _ := h.do(t, "GET", "/api/rules", ""); code != 401 {
		t.Fatalf("unauthenticated rules: %d", code)
	}
	if _, out := h.do(t, "GET", "/api/auth/state", ""); out["initialized"] != false {
		t.Fatalf("state: %v", out)
	}
	if code, _ := h.do(t, "POST", "/api/auth/setup", `{"setupToken":"wrong","username":"a","password":"password123"}`); code != 403 {
		t.Fatalf("bad setup token: %d", code)
	}
	h.setup(t)
	if code, _ := h.do(t, "POST", "/api/auth/setup", `{"setupToken":"x","username":"a","password":"password123"}`); code != 409 {
		t.Fatalf("second setup: %d", code)
	}
	if code, out := h.do(t, "GET", "/api/rules", ""); code != 200 {
		t.Fatalf("authenticated rules: %d %v", code, out)
	}

	// Cross-site writes with the session cookie are rejected.
	if code, _ := h.do(t, "POST", "/api/rules", `{}`, "Origin", "https://evil.example"); code != 403 {
		t.Fatalf("cross-site POST: %d", code)
	}

	// API token works without the cookie.
	_, out := h.do(t, "POST", "/api/auth/tokens", `{"name":"ci"}`)
	token, _ := out["token"].(string)
	if !strings.HasPrefix(token, "fgw_") {
		t.Fatalf("token: %v", out)
	}
	h.do(t, "POST", "/api/auth/logout", "")
	if code, _ := h.do(t, "GET", "/api/rules", ""); code != 401 {
		t.Fatalf("after logout: %d", code)
	}
	if code, _ := h.do(t, "GET", "/api/rules", "", "Authorization", "Bearer "+token); code != 200 {
		t.Fatalf("bearer: %d", code)
	}
	if code, _ := h.do(t, "GET", "/api/rules", "", "Authorization", "Bearer fgw_nope"); code != 401 {
		t.Fatalf("bad bearer: %d", code)
	}

	// Login throttling kicks in after repeated failures.
	var code int
	for range 6 {
		code, _ = h.do(t, "POST", "/api/auth/login", `{"username":"admin","password":"wrong-password"}`)
	}
	if code != 429 {
		t.Fatalf("expected 429 after failures, got %d", code)
	}
}

func TestRulesAPI(t *testing.T) {
	h := newHarness(t)
	h.setup(t)
	code, out := h.do(t, "POST", "/api/rules", `{"type":"tcp","localHost":"127.0.0.1","localPort":70000,"targetHost":"x","targetPort":1}`)
	if code != 400 || out["error"] != "validation" || out["field"] != "localPort" {
		t.Fatalf("validation: %d %v", code, out)
	}
	code, out = h.do(t, "POST", "/api/rules", `{"name":"web","type":"tcp","status":"inactive","localHost":"127.0.0.1","localPort":18080,"targetHost":"127.0.0.1","targetPort":80}`)
	if code != 201 || out["id"] != "1" {
		t.Fatalf("create: %d %v", code, out)
	}
	rt, _ := out["runtime"].(map[string]any)
	if rt["state"] != "stopped" {
		t.Fatalf("runtime: %v", out)
	}
	if code, _ := h.do(t, "GET", "/api/rules/nope", ""); code != 404 {
		t.Fatalf("missing rule: %d", code)
	}
	if code, out := h.do(t, "GET", "/api/metrics/history?range=24h", ""); code != 200 || len(out["points"].([]any)) != 1440 {
		t.Fatalf("history: %d", code)
	}
	if code, _ := h.do(t, "GET", "/api/metrics/history?range=2d", ""); code != 400 {
		t.Fatalf("bad range: %d", code)
	}
	if code, out := h.do(t, "PUT", "/api/settings", `{"update":{"enabled":true,"channel":"beta","checkInterval":3600,"source":"github","repo":"a/b"}}`); code != 400 || out["field"] != "update.channel" {
		t.Fatalf("settings validation: %d %v", code, out)
	}
	code, out = h.do(t, "PUT", "/api/settings", `{"api":{"host":"0.0.0.0","port":9000,"enableCors":false}}`)
	if code != 200 || out["restartRequired"] != true {
		t.Fatalf("settings: %d %v", code, out)
	}
}

func TestImportParseAPI(t *testing.T) {
	h := newHarness(t)
	h.setup(t)
	code, out := h.do(t, "POST", "/api/rules/import/parse", `{"format":"auto","text":"0.0.0.0 18081 127.0.0.1 80\nallow 10.*\n0.0.0.0 x h 1\n"}`)
	rules, _ := out["rules"].([]any)
	warnings, _ := out["warnings"].([]any)
	if code != 200 || out["format"] != "rinetd" || len(rules) != 1 || len(warnings) != 1 {
		t.Fatalf("parse: %d %v", code, out)
	}
	if r := rules[0].(map[string]any); r["id"] != "" || r["localPort"] != float64(18081) || r["acl"] == nil {
		t.Fatalf("parsed rule: %v", r)
	}
	if code, out := h.do(t, "POST", "/api/rules/import/parse", `{"format":"json","text":"{\"api\":{}}"}`); code != 400 || out["field"] != "text" {
		t.Fatalf("parse error: %d %v", code, out)
	}

	dir := t.TempDir()
	saved := rinetdPaths
	t.Cleanup(func() { rinetdPaths = saved })
	rinetdPaths = []string{filepath.Join(dir, "missing.conf")}
	if code, _ := h.do(t, "GET", "/api/rules/import/rinetd", ""); code != 404 {
		t.Fatalf("missing rinetd config: %d", code)
	}
	conf := filepath.Join(dir, "rinetd.conf")
	if err := os.WriteFile(conf, []byte("0.0.0.0 18082/udp 127.0.0.1 53/udp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rinetdPaths = append(rinetdPaths, conf)
	code, out = h.do(t, "GET", "/api/rules/import/rinetd", "")
	if code != 200 || out["path"] != conf || len(out["rules"].([]any)) != 1 {
		t.Fatalf("local rinetd: %d %v", code, out)
	}
}

func TestStatic(t *testing.T) {
	h := newHarness(t)
	for path, wantCache := range map[string]string{
		"/":              "no-cache",
		"/rules/1":       "no-cache",
		"/assets/app.js": "immutable",
	} {
		req, _ := http.NewRequest("GET", h.ts.URL+path, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Cache-Control"), wantCache) {
			t.Errorf("%s: %d %q", path, resp.StatusCode, resp.Header.Get("Cache-Control"))
		}
		if path == "/assets/app.js" && resp.Header.Get("Content-Encoding") != "gzip" {
			t.Errorf("%s not gzipped", path)
		}
	}
	if code, _ := h.do(t, "GET", "/missing.png", ""); code != 404 {
		t.Errorf("missing asset: %d", code)
	}
	if code, out := h.do(t, "GET", "/api/nope", ""); code != 401 && code != 404 {
		t.Errorf("unknown api: %d %v", code, out)
	}
}
