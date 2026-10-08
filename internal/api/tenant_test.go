package api

import (
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
)

// login returns a new client session for another user of the same server.
func (h *harness) login(t *testing.T, username, password string) *harness {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	u := &harness{ts: h.ts, client: &http.Client{Jar: jar}, auth: h.auth, srv: h.srv}
	if code, out := u.do(t, "POST", "/api/auth/login", `{"username":"`+username+`","password":"`+password+`"}`); code != 200 {
		t.Fatalf("login %s: %d %v", username, code, out)
	}
	return u
}

func tcpRule(port string) string {
	return `{"type":"tcp","status":"inactive","localPort":` + port + `,"targetHost":"127.0.0.1","targetPort":3000}`
}

func TestTenantBoundaries(t *testing.T) {
	h := newHarness(t)
	h.setup(t)
	if code, out := h.do(t, "POST", "/api/tenants", `{"id":"team1","name":"Team 1","portRanges":[[20000,20099]],"quota":{"maxRules":2}}`); code != 201 {
		t.Fatalf("create tenant: %d %v", code, out)
	}
	if code, out := h.do(t, "POST", "/api/users", `{"username":"alice","password":"password123","tenantId":"team1"}`); code != 201 || out["role"] != "member" {
		t.Fatalf("create member: %d %v", code, out)
	}
	if code, out := h.do(t, "POST", "/api/users", `{"username":"bob","password":"password123"}`); code != 400 {
		t.Fatalf("member without tenant: %d %v", code, out)
	}
	active := func(port string) string {
		return strings.Replace(tcpRule(port), `"status":"inactive"`, `"status":"active","localHost":"127.0.0.1"`, 1)
	}
	if code, out := h.do(t, "POST", "/api/rules", active("20000")); code != 201 {
		t.Fatalf("admin rule: %d %v", code, out)
	}
	adminRule := "1"

	m := h.login(t, "alice", "password123")
	if _, out := m.do(t, "GET", "/api/auth/state", ""); out["role"] != "member" || out["tenantId"] != "team1" || out["tenantName"] != "Team 1" {
		t.Fatalf("member state: %v", out)
	}
	if _, out := m.do(t, "GET", "/api/rules", ""); len(out["items"].([]any)) != 0 {
		t.Fatalf("member sees foreign rules: %v", out)
	}
	if code, _ := m.do(t, "GET", "/api/rules/"+adminRule, ""); code != 404 {
		t.Fatalf("member reads foreign rule: %d", code)
	}
	if code, _ := m.do(t, "DELETE", "/api/rules/"+adminRule, ""); code != 404 {
		t.Fatalf("member deletes foreign rule: %d", code)
	}
	for _, path := range []string{"/api/users", "/api/tenants", "/api/settings", "/api/cluster", "/api/node", "/api/dns", "/api/update/status"} {
		if code, _ := m.do(t, "GET", path, ""); code != 403 {
			t.Fatalf("member reached %s: %d", path, code)
		}
	}
	if code, _ := m.do(t, "POST", "/api/users", `{"username":"eve","password":"password123","role":"admin"}`); code != 403 {
		t.Fatalf("member created a user: %d", code)
	}

	// Ports outside the tenant's ranges and listeners of other tenants are refused.
	if code, out := m.do(t, "POST", "/api/rules", tcpRule("8080")); code != 400 || out["field"] != "localPort" {
		t.Fatalf("out-of-range port: %d %v", code, out)
	}
	if code, out := m.do(t, "POST", "/api/rules", active("20000")); code != 409 || strings.Contains(out["message"].(string), adminRule) {
		t.Fatalf("conflict leaked a foreign rule: %d %v", code, out)
	}
	code, out := m.do(t, "POST", "/api/rules", strings.Replace(tcpRule("20001"), `{`, `{"owner":"other",`, 1))
	if code != 201 || out["owner"] != "team1" {
		t.Fatalf("member rule: %d %v", code, out)
	}
	own := out["id"].(string)
	if code, out := m.do(t, "POST", "/api/rules", tcpRule("20002")); code != 201 {
		t.Fatalf("second rule: %d %v", code, out)
	}
	if code, out := m.do(t, "POST", "/api/rules", tcpRule("20003")); code != 409 || out["error"] != "quota_exceeded" {
		t.Fatalf("rule limit: %d %v", code, out)
	}
	if _, out := m.do(t, "GET", "/api/rules", ""); len(out["items"].([]any)) != 2 {
		t.Fatalf("member rules: %v", out)
	}
	if _, out := m.do(t, "GET", "/api/tenant", ""); out["id"] != "team1" || out["rules"] != float64(2) {
		t.Fatalf("own tenant: %v", out)
	}
	if code, _ := m.do(t, "GET", "/api/metrics/realtime?rule="+adminRule, ""); code != 404 {
		t.Fatalf("member read foreign metrics: %d", code)
	}
	if code, _ := m.do(t, "GET", "/api/metrics/realtime?rule="+own, ""); code != 200 {
		t.Fatalf("member own metrics: %d", code)
	}
	if code, out := m.do(t, "GET", "/api/overview", ""); code != 200 || out["configPath"] != nil || out["rules"].(map[string]any)["total"] != float64(2) {
		t.Fatalf("member overview: %d %v", code, out)
	}
	// Import replace only touches the tenant's own rules.
	if code, out := m.do(t, "POST", "/api/rules/import", `{"mode":"replace","forward":[`+tcpRule("20010")+`]}`); code != 200 {
		t.Fatalf("member import: %d %v", code, out)
	}
	if code, _ := h.do(t, "GET", "/api/rules/"+adminRule, ""); code != 200 {
		t.Fatal("tenant import replaced an administrator rule")
	}

	// Administrators see every rule; a PUT without an owner keeps it.
	_, out = h.do(t, "GET", "/api/rules", "")
	if len(out["items"].([]any)) != 2 {
		t.Fatalf("admin rules: %v", out)
	}
	var tenantRule string
	for _, it := range out["items"].([]any) {
		if r := it.(map[string]any); r["owner"] == "team1" {
			tenantRule = r["id"].(string)
		}
	}
	if code, out := h.do(t, "PUT", "/api/rules/"+tenantRule, tcpRule("20011")); code != 200 || out["owner"] != "team1" {
		t.Fatalf("admin update kept owner: %d %v", code, out)
	}
	if code, out := h.do(t, "PUT", "/api/tenants/team1", `{"name":"Team 1","portRanges":[[30000,30099]]}`); code != 400 {
		t.Fatalf("narrowed ranges stranded rules: %d %v", code, out)
	}
	if code, _ := h.do(t, "DELETE", "/api/tenants/team1", ""); code != 409 {
		t.Fatalf("deleted tenant in use: %d", code)
	}

	// Disabling a member ends its sessions.
	_, users := h.do(t, "GET", "/api/users", "")
	var alice string
	for _, it := range users["items"].([]any) {
		if u := it.(map[string]any); u["username"] == "alice" {
			alice = u["id"].(string)
		}
	}
	if code, out := h.do(t, "PUT", "/api/users/"+alice, `{"disabled":true}`); code != 200 {
		t.Fatalf("disable: %d %v", code, out)
	}
	if code, _ := m.do(t, "GET", "/api/rules", ""); code != 401 {
		t.Fatalf("disabled member still signed in: %d", code)
	}

	// The last administrator cannot lock everyone out.
	_, state := h.do(t, "GET", "/api/auth/state", "")
	self := state["userId"].(string)
	if code, _ := h.do(t, "PUT", "/api/users/"+self, `{"role":"member","tenantId":"team1"}`); code != 409 {
		t.Fatalf("admin demoted itself: %d", code)
	}
	if code, _ := h.do(t, "DELETE", "/api/users/"+self, ""); code != 409 {
		t.Fatalf("admin deleted itself: %d", code)
	}
}

func TestTokensBelongToTheirUser(t *testing.T) {
	h := newHarness(t)
	h.setup(t)
	h.do(t, "POST", "/api/tenants", `{"id":"team1","name":"Team 1","portRanges":[[20000,20099]]}`)
	h.do(t, "POST", "/api/users", `{"username":"alice","password":"password123","tenantId":"team1"}`)
	_, out := h.do(t, "POST", "/api/auth/tokens", `{"name":"admin-ci"}`)
	adminToken := out["item"].(map[string]any)["id"].(string)

	m := h.login(t, "alice", "password123")
	_, out = m.do(t, "POST", "/api/auth/tokens", `{"name":"ci"}`)
	token := out["token"].(string)
	if _, out := m.do(t, "GET", "/api/auth/tokens", ""); len(out["items"].([]any)) != 1 {
		t.Fatalf("member sees other tokens: %v", out)
	}
	if code, _ := m.do(t, "DELETE", "/api/auth/tokens/"+adminToken, ""); code != 404 {
		t.Fatalf("member revoked an admin token: %d", code)
	}
	// The token acts as its owner, with the owner's limits.
	if code, _ := h.do(t, "GET", "/api/users", "", "Authorization", "Bearer "+token); code != 403 {
		t.Fatalf("member token reached admin API: %d", code)
	}
	if code, out := h.do(t, "POST", "/api/rules", tcpRule("20005"), "Authorization", "Bearer "+token); code != 201 || out["owner"] != "team1" {
		t.Fatalf("member token rule: %d %v", code, out)
	}
	// A password change signs out the member's other sessions.
	other := h.login(t, "alice", "password123")
	if code, out := m.do(t, "POST", "/api/auth/password", `{"currentPassword":"password123","newPassword":"password456"}`); code != 200 {
		t.Fatalf("password: %d %v", code, out)
	}
	if code, _ := m.do(t, "GET", "/api/rules", ""); code != 200 {
		t.Fatalf("password change signed out the caller: %d", code)
	}
	if code, _ := other.do(t, "GET", "/api/rules", ""); code != 401 {
		t.Fatalf("other session survived a password change: %d", code)
	}
}
