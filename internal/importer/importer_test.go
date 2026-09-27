package importer

import (
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/lieyanc/FireGateway/internal/config"
)

func mustParse(t *testing.T, format, text string) *Result {
	t.Helper()
	res, err := Parse(format, text)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return res
}

func hasWarning(res *Result, line int, sub string) bool {
	for _, w := range res.Warnings {
		if (line == 0 || w.Line == line || w.Index == line) && strings.Contains(w.Message, sub) {
			return true
		}
	}
	return false
}

func TestDetect(t *testing.T) {
	cases := map[string]string{
		"# rinetd\n0.0.0.0 80 10.0.0.1 80\n": FormatRinetd,
		"\n  {\"forward\": []}":              FormatJSON,
		"[{\"type\":\"tcp\"}]":               FormatJSON,
		"\"forward\": [":                     FormatJSON,
		"// note\n{":                         FormatJSON,
		"":                                   FormatRinetd,
	}
	for in, want := range cases {
		if got := Detect(in); got != want {
			t.Errorf("Detect(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestRinetdBasic(t *testing.T) {
	res := mustParse(t, FormatAuto, `
# comment
logfile /var/log/rinetd.log
logcommon
0.0.0.0 80 10.1.1.2 8080   # web
0 53/udp 10.1.1.3 53/udp [timeout=3600]
[::] 2222 [2001:db8::1] 22
192.168.1.1 ssh 10.0.0.9 ssh[src=10.1.1.2]
0.0.0.0 81/tcp 10.1.1.2 81/udp
`)
	if res.Format != FormatRinetd {
		t.Fatalf("format = %s", res.Format)
	}
	want := []config.Rule{
		{Type: "tcp", Status: "active", LocalHost: "0.0.0.0", LocalPort: 80, TargetHost: "10.1.1.2", TargetPort: 8080, Remark: "Imported from rinetd (line 5)"},
		{Type: "udp", Status: "active", LocalHost: "0.0.0.0", LocalPort: 53, TargetHost: "10.1.1.3", TargetPort: 53, Remark: "Imported from rinetd (line 6)"},
		{Type: "tcp", Status: "active", LocalHost: "::", LocalPort: 2222, TargetHost: "2001:db8::1", TargetPort: 22, Remark: "Imported from rinetd (line 7)"},
		{Type: "tcp", Status: "active", LocalHost: "192.168.1.1", LocalPort: 22, TargetHost: "10.0.0.9", TargetPort: 22, Remark: "Imported from rinetd (line 8)"},
	}
	if !reflect.DeepEqual(res.Rules, want) {
		t.Fatalf("rules:\n got %+v\nwant %+v", res.Rules, want)
	}
	for line, sub := range map[int]string{6: "timeout", 8: "src=", 9: "tcp to udp"} {
		if !hasWarning(res, line, sub) {
			t.Errorf("missing warning %q on line %d: %+v", sub, line, res.Warnings)
		}
	}
}

func TestRinetdACL(t *testing.T) {
	// Allow/deny lines after a forwarding rule belong to that rule.
	res := mustParse(t, FormatRinetd, `allow 192.168.*
allow 10.0.0.1
deny 192.168.1.66
0.0.0.0 1000 h 1
0.0.0.0 1001 h 1
allow 192.168.2.*
0.0.0.0 1002 h 1
allow 172.16.0.1
0.0.0.0 1003 h 1
deny *
`)
	if len(res.Rules) != 4 {
		t.Fatalf("got %d rules", len(res.Rules))
	}
	// Global allows minus the global deny.
	if a := res.Rules[0].ACL; a == nil || a.Mode != "allow" || len(a.CIDRs) != 17 || a.CIDRs[0] != "10.0.0.1" ||
		a.CIDRs[1] != "192.168.0.0/24" || a.CIDRs[16] != "192.168.128.0/17" {
		t.Errorf("rule 1 acl = %+v", res.Rules[0].ACL)
	}
	// Intersection of the global and local allows.
	if a := res.Rules[1].ACL; a == nil || !reflect.DeepEqual(a.CIDRs, []string{"192.168.2.0/24"}) {
		t.Errorf("rule 2 acl = %+v", a)
	}
	// A local allow outside the global allows, and a local "deny *": nobody passes.
	for i, line := range map[int]int{2: 7, 3: 9} {
		if res.Rules[i].Status != config.StatusInactive || !hasWarning(res, line, "every client") {
			t.Errorf("rule %d = %+v, warnings %+v", i+1, res.Rules[i], res.Warnings)
		}
	}
}

func TestRinetdDenyOnly(t *testing.T) {
	res := mustParse(t, FormatRinetd, "deny 10.*\n0.0.0.0 1 h 1\ndeny 1.2.3.1?\n0.0.0.0 2 h 2\nallow *\n")
	if a := res.Rules[0].ACL; a == nil || a.Mode != "deny" || !reflect.DeepEqual(a.CIDRs, []string{"1.2.3.10/31", "1.2.3.12/30", "1.2.3.16/30", "10.0.0.0/8"}) {
		t.Errorf("rule 1 acl = %+v", a)
	}
	// "allow *" allows everyone, leaving only the global deny.
	if a := res.Rules[1].ACL; a == nil || !reflect.DeepEqual(a.CIDRs, []string{"10.0.0.0/8"}) {
		t.Errorf("rule 2 acl = %+v", a)
	}
}

func TestRinetdUnconvertiblePatterns(t *testing.T) {
	res := mustParse(t, FormatRinetd, "0.0.0.0 1 h 1\nallow 10.*.1\nallow 2001:db8::*\n")
	// The allow lines existed but none converted: deny everyone rather than open up.
	if r := res.Rules[0]; r.Status != config.StatusInactive {
		t.Errorf("rule = %+v", r)
	}
	if !hasWarning(res, 2, "span octets") || !hasWarning(res, 3, "IPv6") {
		t.Errorf("warnings = %+v", res.Warnings)
	}
}

func TestRinetdMergeRanges(t *testing.T) {
	var b strings.Builder
	for p := 5000; p < 5010; p++ {
		b.WriteString("0.0.0.0 " + strconv.Itoa(p) + " 10.0.0.1 " + strconv.Itoa(p+1000) + "\n")
	}
	b.WriteString("0.0.0.0 5010 10.0.0.2 6010\n") // different target host
	b.WriteString("0.0.0.0 5011/udp 10.0.0.2 6011/udp\n")
	res := mustParse(t, FormatRinetd, b.String())
	if len(res.Rules) != 3 {
		t.Fatalf("got %d rules: %+v", len(res.Rules), res.Rules)
	}
	r := res.Rules[0]
	if !reflect.DeepEqual(r.LocalPortRange, []int{5000, 5009}) || !reflect.DeepEqual(r.TargetPortRange, []int{6000, 6009}) ||
		r.LocalPort != 0 || r.Remark != "Imported from rinetd (lines 1-10)" {
		t.Errorf("merged rule = %+v", r)
	}
	if res.Rules[1].LocalPort != 5010 || res.Rules[2].Type != "udp" {
		t.Errorf("rules = %+v", res.Rules[1:])
	}
}

func TestRinetdDuplicateListener(t *testing.T) {
	res := mustParse(t, FormatRinetd, "0.0.0.0 80 a 80\n10.0.0.1 80 b 80\n0.0.0.0 80/udp c 80/udp\n")
	if res.Rules[0].Status != "active" || res.Rules[1].Status != "inactive" || res.Rules[2].Status != "active" {
		t.Errorf("rules = %+v", res.Rules)
	}
	if !hasWarning(res, 2, "same tcp port") {
		t.Errorf("warnings = %+v", res.Warnings)
	}
}

func TestRinetdErrors(t *testing.T) {
	if _, err := Parse(FormatRinetd, "# nothing\nlogcommon\n"); err == nil {
		t.Fatal("expected an error without forwarding rules")
	}
	res := mustParse(t, FormatRinetd, "0.0.0.0 99999 h 1\n0.0.0.0 1 h nosuchservice\n0.0.0.0 1 h 1 junk\n0.0.0.0 2 h 2\n")
	if len(res.Rules) != 1 || len(res.Warnings) != 3 {
		t.Errorf("rules = %+v warnings = %+v", res.Rules, res.Warnings)
	}
}

const legacyConfig = `{
  "api": {"enabled": true, "host": "127.0.0.1", "port": 8080, "enableCors": true},
  "logging": {"level": "info"},
  "forward": [
    {"id": 1, "name": "TCP", "type": "tcp", "status": "active", "localHost": "0.0.0.0",
     "localPort": 3333, "targetHost": "192.168.1.123", "targetPort": 1234},
    {"id": 2, "name": "UDP", "type": "UDP", "status": "active", "localHost": "192.168.1.3",
     "localPort": "3456", "targetHost": "192.168.1.10", "targetPort": 5678},
    {"id": 3, "type": "tcp", "status": "active", "localHost": "0.0.0.0",
     "localPortRange": [4000, 4010], "targetHost": "192.168.1.124", "targetPortRange": [5000, 5010]},
    {"id": 4, "type": "tcp", "localHost": "0.0.0.0", "localPort": 1, "targetHost": "h", "targetPort": 1},
    {"id": 5, "type": "http", "status": "active", "localPort": 2, "targetHost": "h", "targetPort": 2}
  ]
}`

func TestJSONLegacyConfig(t *testing.T) {
	res := mustParse(t, FormatAuto, legacyConfig)
	if res.Format != FormatJSON || len(res.Rules) != 4 {
		t.Fatalf("format %s, rules %+v, warnings %+v", res.Format, res.Rules, res.Warnings)
	}
	if r := res.Rules[1]; r.ID != "2" || r.Type != "udp" || r.LocalPort != 3456 {
		t.Errorf("rule 2 = %+v", r)
	}
	if r := res.Rules[2]; !reflect.DeepEqual(r.LocalPortRange, []int{4000, 4010}) {
		t.Errorf("rule 3 = %+v", r)
	}
	if r := res.Rules[3]; r.Status != config.StatusInactive || !hasWarning(res, 4, "no status") {
		t.Errorf("rule 4 = %+v warnings %+v", r, res.Warnings)
	}
	if !hasWarning(res, 5, "type must be tcp or udp") {
		t.Errorf("warnings = %+v", res.Warnings)
	}
}

func TestJSONSnippets(t *testing.T) {
	obj := `{"id": "a", "type": "tcp", "status": "active", "localPort": 1, "targetHost": "h", "targetPort": 1}`
	obj2 := `{"id": "b", "type": "tcp", "status": "inactive", "localPort": 2, "targetHost": "h", "targetPort": 2}`
	cases := map[string]string{
		"single object":       obj,
		"objects with commas": obj + ",\n" + obj2 + ",",
		"bare array":          "[" + obj + "," + obj2 + "]",
		"forward property":    `"forward": [` + obj + `, ` + obj2 + `],`,
		"comments and commas": "// rules\n[\n  " + obj + ", /* second */\n  " + obj2 + ", // last\n]",
	}
	for name, text := range cases {
		res, err := Parse(FormatJSON, text)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(res.Rules) == 0 || res.Rules[0].ID != "a" || (len(res.Rules) > 1 && res.Rules[1].Status != "inactive") {
			t.Errorf("%s: rules = %+v", name, res.Rules)
		}
	}
}

func TestJSONIDs(t *testing.T) {
	res := mustParse(t, FormatJSON, `[
		{"id": "bad id", "type": "tcp", "status": "active", "localPort": 1, "targetHost": "h", "targetPort": 1},
		{"id": 7, "type": "tcp", "status": "active", "localPort": 2, "targetHost": "h", "targetPort": 2},
		{"id": 7, "type": "tcp", "status": "active", "localPort": 3, "targetHost": "h", "targetPort": 3}
	]`)
	if res.Rules[0].ID != "" || res.Rules[1].ID != "7" || res.Rules[2].ID != "" {
		t.Errorf("ids = %q %q %q", res.Rules[0].ID, res.Rules[1].ID, res.Rules[2].ID)
	}
	if !hasWarning(res, 1, "not a valid") || !hasWarning(res, 3, "duplicate id") {
		t.Errorf("warnings = %+v", res.Warnings)
	}
}

func TestJSONErrors(t *testing.T) {
	for text, sub := range map[string]string{
		"":                           "empty",
		`{"api": {"port": 8080}}`:    "no rules found",
		"{\n\"forward\": [\n  {oops": "line 3",
		`{"forward": []}`:            "empty",
	} {
		_, err := Parse(FormatJSON, text)
		if err == nil || !strings.Contains(err.Error(), sub) {
			t.Errorf("Parse(%q) error = %v, want %q", text, err, sub)
		}
	}
	res := mustParse(t, FormatJSON, `[{"type": "tcp", "status": "active", "localPort": [1], "targetHost": "h", "targetPort": 1}]`)
	if len(res.Rules) != 0 || !hasWarning(res, 1, "localPort has the wrong type") {
		t.Errorf("rules %+v warnings %+v", res.Rules, res.Warnings)
	}
}

func TestPrefixSets(t *testing.T) {
	p := func(ss ...string) []netip.Prefix {
		out := make([]netip.Prefix, len(ss))
		for i, s := range ss {
			out[i] = netip.MustParsePrefix(s)
		}
		return out
	}
	if got := subtract(p("10.0.0.0/8"), p("10.1.2.3/32")); len(got) != 24 || got[0] != netip.MustParsePrefix("10.0.0.0/16") {
		t.Errorf("subtract = %v", got)
	}
	if got := aggregate(p("10.0.0.0/25", "10.0.0.128/26", "10.0.0.192/26", "10.0.0.5/32")); !reflect.DeepEqual(got, p("10.0.0.0/24")) {
		t.Errorf("aggregate = %v", got)
	}
	if got := intersect(p("10.0.0.0/8", "::/0"), p("10.9.0.0/16", "192.168.0.0/16", "2001:db8::/32")); !reflect.DeepEqual(got, p("10.9.0.0/16", "2001:db8::/32")) {
		t.Errorf("intersect = %v", got)
	}
	if got, err := patternPrefixes("10.1.*.*"); err != nil || !reflect.DeepEqual(got, p("10.1.0.0/16")) {
		t.Errorf("pattern = %v %v", got, err)
	}
	if got, err := patternPrefixes("10.2?.0.0"); err != nil || len(got) != 10 {
		t.Errorf("pattern = %v %v", got, err)
	}
	if _, err := patternPrefixes("*.*.*.1"); err == nil {
		t.Error("expected an error for a pattern expanding to too many blocks")
	}
}
