package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// copyFile copies src into a temp dir so Open may complete it in place.
func copyFile(t *testing.T, src string) string {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), filepath.Base(src))
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadLegacyConfig(t *testing.T) {
	for _, f := range []string{"../../config.example.json", "../../config-test-local.json"} {
		s, state, err := Open(copyFile(t, f))
		if err != nil || state != Completed {
			t.Fatalf("%s: err=%v state=%v", f, err, state)
		}
		for _, r := range s.Get().Forward {
			if _, err := r.Mappings(); err != nil {
				t.Errorf("%s rule %s: %v", f, r.ID, err)
			}
		}
	}
	s, _, _ := Open(copyFile(t, "../../config.example.json"))
	if m, _ := s.Get().Forward[2].Mappings(); len(m) != 11 || m[10] != (Mapping{4010, 5010}) {
		t.Errorf("unexpected range expansion: %v", m)
	}
}

// The template must be exactly what the Store writes, so a created file is
// the template verbatim and reopening it is a no-op.
func TestTemplateRoundTrip(t *testing.T) {
	var c Config
	if err := json.Unmarshal(template, &c); err != nil {
		t.Fatal(err)
	}
	out, _ := json.MarshalIndent(&c, "", "  ")
	if string(out)+"\n" != string(template) {
		t.Errorf("template.json is not in canonical form, want:\n%s", out)
	}
	p := filepath.Join(t.TempDir(), "c.json")
	if _, state, err := Open(p); err != nil || state != Created {
		t.Fatalf("err=%v state=%v", err, state)
	}
	if data, _ := os.ReadFile(p); string(data) != string(template) {
		t.Errorf("created file differs from template:\n%s", data)
	}
	if _, state, err := Open(p); err != nil || state != Loaded {
		t.Fatalf("reopen: err=%v state=%v", err, state)
	}
}

func TestCompleteMissingFields(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	os.WriteFile(p, []byte(`{"api":{"port":9000,"enabled":false},"logging":null,"forward":[{"id":1,"type":"tcp","status":"active","localPort":1,"targetHost":"h","targetPort":2}]}`), 0o644)
	s, state, err := Open(p)
	if err != nil || state != Completed {
		t.Fatalf("err=%v state=%v", err, state)
	}
	c := s.Get()
	if c.API.Port != 9000 || c.API.Enabled || c.API.Host != "127.0.0.1" || !c.Logging.EnableConsole ||
		c.Logging.Level != "info" || c.Update.Repo != "lieyanc/FireGateway" || len(c.Forward) != 1 {
		t.Errorf("unexpected completed config: %+v", c)
	}
	data, _ := os.ReadFile(p)
	for _, want := range []string{`"port": 9000`, `"enabled": false`, `"host": "127.0.0.1"`, `"proxyBaseUrl": ""`, `"sessionTtl": 604800`, `"id": 1`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("completed file lacks %s:\n%s", want, data)
		}
	}
	if _, state, err := Open(p); err != nil || state != Loaded {
		t.Fatalf("reopen: err=%v state=%v", err, state)
	}
}

func TestEnvOverridesDefaults(t *testing.T) {
	t.Setenv("API_PORT", "9100")
	p := filepath.Join(t.TempDir(), "c.json")
	os.WriteFile(p, []byte(`{"api":{"host":"0.0.0.0"}}`), 0o644)
	s, _, err := Open(p)
	if err != nil || s.Get().API.Port != 9100 || s.Get().API.Host != "0.0.0.0" {
		t.Fatalf("err=%v api=%+v", err, s.Get().API)
	}
}

func TestInvalidRange(t *testing.T) {
	r := Rule{LocalPortRange: []int{1000, 1005}, TargetPortRange: []int{2000, 2004}}
	if _, err := r.Mappings(); err == nil {
		t.Fatal("expected mismatched range error")
	}
}

func TestInvalidJSON(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	os.WriteFile(p, []byte(`{"api":`), 0o644)
	if _, _, err := Open(p); err == nil {
		t.Fatal("expected error")
	}
	if data, _ := os.ReadFile(p); string(data) != `{"api":` {
		t.Errorf("invalid file was modified: %s", data)
	}
}

func TestCreateDefaultAndPersist(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "c.json")
	s, state, err := Open(p)
	if err != nil || state != Created {
		t.Fatalf("err=%v state=%v", err, state)
	}
	_, err = s.Update(func(c *Config) error {
		c.Forward = append(c.Forward, Rule{ID: "7", Type: "tcp", Status: "active", LocalPort: 1, TargetHost: "h", TargetPort: 2})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	// Numeric ids stay numbers on disk.
	if !strings.Contains(string(data), `"id": 7`) {
		t.Errorf("numeric id not preserved:\n%s", data)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Errorf("config mode = %v, want 0600", st.Mode().Perm())
	}
	s2, _, err := Open(p)
	if err != nil || len(s2.Get().Forward) != 1 {
		t.Fatalf("reopen: err=%v rules=%d", err, len(s2.Get().Forward))
	}
}

func TestRuleIDJSON(t *testing.T) {
	for in, want := range map[string]string{`1`: `1`, `"a-1"`: `"a-1"`, `"007"`: `"007"`, `"12"`: `12`} {
		var id RuleID
		if err := json.Unmarshal([]byte(in), &id); err != nil {
			t.Fatal(err)
		}
		out, _ := json.Marshal(id)
		if string(out) != want {
			t.Errorf("%s -> %s, want %s", in, out, want)
		}
	}
}

func TestValidate(t *testing.T) {
	base := func() Rule {
		return Rule{ID: "a", Type: "tcp", Status: "active", LocalHost: "0.0.0.0", LocalPort: 80, TargetHost: "example.com", TargetPort: 8080}
	}
	cases := map[string]struct {
		mod   func(*Rule)
		field string
	}{
		"ok":          {func(*Rule) {}, ""},
		"bad id":      {func(r *Rule) { r.ID = "a b" }, "id"},
		"bad type":    {func(r *Rule) { r.Type = "http" }, "type"},
		"bad listen":  {func(r *Rule) { r.LocalHost = "not a host" }, "localHost"},
		"no target":   {func(r *Rule) { r.TargetHost = "" }, "targetHost"},
		"bad port":    {func(r *Rule) { r.LocalPort = 70000 }, "localPort"},
		"bad cidr":    {func(r *Rule) { r.ACL = &ACL{Mode: "allow", CIDRs: []string{"10.0.0.0/8", "nope"}} }, "acl.cidrs[1]"},
		"bad mode":    {func(r *Rule) { r.ACL = &ACL{Mode: "x", CIDRs: []string{"1.1.1.1"}} }, "acl.mode"},
		"neg limit":   {func(r *Rule) { r.Limits = &Limits{Bandwidth: -1} }, "limits.bandwidth"},
		"ipv6 listen": {func(r *Rule) { r.LocalHost = "::" }, ""},
		"huge range": {func(r *Rule) {
			r.LocalPortRange, r.TargetPortRange = []int{1, 5000}, []int{1, 5000}
		}, "localPortRange"},
	}
	for name, c := range cases {
		r := base()
		c.mod(&r)
		err := r.Validate()
		var field string
		if fe, ok := err.(*FieldError); ok {
			field = fe.Field
		} else if err != nil {
			t.Errorf("%s: unexpected error type %v", name, err)
			continue
		}
		if field != c.field {
			t.Errorf("%s: field=%q want %q (err=%v)", name, field, c.field, err)
		}
	}
}

func TestListenOverlaps(t *testing.T) {
	a := Rule{Type: "tcp", LocalHost: "0.0.0.0", LocalPortRange: []int{4000, 4010}}
	cases := []struct {
		b    Rule
		want bool
	}{
		{Rule{Type: "tcp", LocalHost: "127.0.0.1", LocalPort: 4005}, true},
		{Rule{Type: "udp", LocalHost: "0.0.0.0", LocalPort: 4005}, false},
		{Rule{Type: "tcp", LocalHost: "0.0.0.0", LocalPort: 4011}, false},
	}
	for i, c := range cases {
		if got := a.ListenOverlaps(&c.b); got != c.want {
			t.Errorf("case %d: got %v want %v", i, got, c.want)
		}
	}
	x := Rule{Type: "tcp", LocalHost: "10.0.0.1", LocalPort: 80}
	y := Rule{Type: "tcp", LocalHost: "10.0.0.2", LocalPort: 80}
	if x.ListenOverlaps(&y) {
		t.Error("different specific hosts should not overlap")
	}
}
