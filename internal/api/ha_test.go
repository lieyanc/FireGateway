package api

import "testing"

func TestNodeOverridesAPIAndReadiness(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/api/node", "/api/cluster"} {
		if code, _ := h.do(t, "GET", path, ""); code != 401 {
			t.Fatalf("%s was public", path)
		}
	}
	if code, _ := h.do(t, "GET", "/ready", ""); code != 503 {
		t.Fatalf("empty gateway ready: %d", code)
	}
	h.setup(t)
	if code, out := h.do(t, "GET", "/api/cluster", ""); code != 200 || out["role"] != "standalone" {
		t.Fatalf("cluster status: %d %v", code, out)
	}
	if code, out := h.do(t, "POST", "/api/rules", `{"id":"web","type":"tcp","status":"inactive","localHost":"0.0.0.0","localPort":8000,"targetHost":"192.0.2.1","targetPort":9000}`); code != 201 {
		t.Fatalf("create: %d %v", code, out)
	}
	if code, out := h.do(t, "PUT", "/api/node", `{"id":"","ruleOverrides":{"web":{"localHost":"127.0.0.1","targetHost":"127.0.0.1","targetPort":3000}}}`); code != 200 {
		t.Fatalf("override: %d %v", code, out)
	}
	_, out := h.do(t, "GET", "/api/rules/web", "")
	if out["targetHost"] != "192.0.2.1" || out["targetPort"] != float64(9000) {
		t.Fatal("local override leaked into shared rule")
	}
	_, out = h.do(t, "GET", "/api/node", "")
	effective := out["effectiveRules"].([]any)[0].(map[string]any)
	if effective["targetHost"] != "127.0.0.1" || effective["targetPort"] != float64(3000) {
		t.Fatal("effective rule is incorrect")
	}
	if _, ok := out["auth"]; ok {
		t.Fatal("node endpoint exposes credentials")
	}
	if code, _ := h.do(t, "PUT", "/api/node", `{"id":"","ruleOverrides":{"web":{"localPort":70000}}}`); code != 400 {
		t.Fatalf("invalid override accepted: %d", code)
	}
}
