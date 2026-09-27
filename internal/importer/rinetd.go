package importer

import (
	"fmt"
	"net"
	"net/netip"
	"reflect"
	"strconv"
	"strings"

	"github.com/lieyanc/FireGateway/internal/config"
)

// aclList is the prefixes of the allow or deny lines in one scope. seen
// records that such lines exist even if none could be converted, because an
// allow list that lost all its entries must still deny everyone.
type aclList struct {
	seen     bool
	prefixes []netip.Prefix
}

type rinetdServer struct {
	entry
	allow, deny aclList
	// rinetd would fail to start with this rule; import it disabled.
	disabled bool
}

// parseRinetd converts a rinetd.conf. Allow/deny lines before the first
// forwarding rule apply globally, later ones to the preceding rule; both
// levels are folded into each rule's single allow- or deny-list.
func parseRinetd(text string) (*Result, error) {
	res := &Result{Format: FormatRinetd}
	var (
		global  struct{ allow, deny aclList }
		servers []*rinetdServer
	)
	for i, raw := range strings.Split(text, "\n") {
		lineNo := i + 1
		line, _, _ := strings.Cut(raw, "#")
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		at := &entry{line: lineNo}
		switch fields[0] {
		case "allow", "deny":
			if len(fields) != 2 {
				res.warn(at, "expected %q followed by one pattern; line ignored", fields[0])
				continue
			}
			allow, deny := &global.allow, &global.deny
			if len(servers) > 0 {
				s := servers[len(servers)-1]
				allow, deny = &s.allow, &s.deny
			}
			list := allow
			if fields[0] == "deny" {
				list = deny
			}
			list.seen = true
			ps, err := patternPrefixes(fields[1])
			if err != nil {
				res.warn(at, "%s pattern %q skipped: %s", fields[0], fields[1], err)
				continue
			}
			list.prefixes = append(list.prefixes, ps...)
		case "logfile", "pidlogfile", "logcommon":
			// Logging settings have no per-rule equivalent.
		default:
			if s := parseServerLine(res, at, fields); s != nil {
				servers = append(servers, s)
			}
		}
	}
	if len(servers) == 0 {
		return nil, &config.FieldError{Field: "text", Msg: "no rinetd forwarding rules found"}
	}
	entries := make([]entry, 0, len(servers))
	for _, s := range servers {
		acl, denyAll := rinetdACL(global.allow, global.deny, s.allow, s.deny)
		s.rule.ACL = acl
		if denyAll {
			res.warn(&s.entry, "allow/deny rules reject every client; imported as disabled")
			s.disabled = true
		}
		if s.disabled {
			s.rule.Status = config.StatusInactive
		}
		entries = append(entries, s.entry)
	}
	res.collect(mergeRanges(entries))
	return res, nil
}

func parseServerLine(res *Result, at *entry, fields []string) *rinetdServer {
	if len(fields) < 4 {
		res.warn(at, "unrecognized line ignored")
		return nil
	}
	s := &rinetdServer{entry: *at}
	bindPort, bindProto, err := rinetdPort(fields[1])
	if err != nil {
		res.warn(at, "skipped: bind port: %s", err)
		return nil
	}
	// Options may follow the connect port with or without a space.
	connect, opts, hasOpts := strings.Cut(strings.Join(fields[3:], " "), "[")
	connect = strings.TrimSpace(connect)
	if strings.Contains(connect, " ") || (hasOpts && !strings.HasSuffix(opts, "]")) {
		res.warn(at, "skipped: unexpected text after the connect port")
		return nil
	}
	connPort, connProto, err := rinetdPort(connect)
	if err != nil {
		res.warn(at, "skipped: connect port: %s", err)
		return nil
	}
	if bindProto != connProto {
		res.warn(at, "skipped: forwarding %s to %s is not supported", bindProto, connProto)
		return nil
	}
	if hasOpts {
		for _, opt := range strings.Split(strings.TrimSuffix(opts, "]"), ",") {
			key, val, _ := strings.Cut(strings.ReplaceAll(opt, " ", ""), "=")
			switch key {
			case "timeout":
				if bindProto == "udp" {
					res.warn(at, "timeout=%s ignored; UDP sessions use FireGateway's idle timeout", val)
				}
			case "src":
				res.warn(at, "src=%s ignored; the source address is chosen by the system", val)
			default:
				res.warn(at, "unknown option %q ignored", strings.TrimSpace(opt))
			}
		}
	}
	s.rule = config.Rule{
		Type:       bindProto,
		Status:     config.StatusActive,
		LocalHost:  rinetdAddr(fields[0]),
		LocalPort:  bindPort,
		TargetHost: rinetdAddr(fields[2]),
		TargetPort: connPort,
		Remark:     fmt.Sprintf("Imported from rinetd (line %d)", at.line),
	}
	return s
}

// rinetdPort parses "port[/tcp|/udp]", where port may be a service name.
func rinetdPort(s string) (int, string, error) {
	name, proto, found := strings.Cut(s, "/")
	if !found {
		proto = "tcp"
	}
	if proto != "tcp" && proto != "udp" {
		return 0, "", fmt.Errorf("unknown protocol %q", proto)
	}
	if n, err := strconv.Atoi(name); err == nil {
		if n < 1 || n > 65535 {
			return 0, "", fmt.Errorf("port %d out of range", n)
		}
		return n, proto, nil
	}
	n, err := net.LookupPort(proto, name)
	if err != nil || n == 0 {
		return 0, "", fmt.Errorf("unknown service %q", name)
	}
	return n, proto, nil
}

// rinetdAddr accepts rinetd's shorthand "0" for 0.0.0.0 and strips IPv6 brackets.
func rinetdAddr(s string) string {
	if s == "0" {
		return "0.0.0.0"
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		return s[1 : len(s)-1]
	}
	return s
}

// rinetdACL folds global and per-rule allow/deny lines into one list. A
// client passes rinetd when it matches an allow line in every scope that has
// one and no deny line at all, so the allowed set is the intersection of the
// allow scopes minus every deny. denyAll reports that nobody passes.
func rinetdACL(gAllow, gDeny, allow, deny aclList) (acl *config.ACL, denyAll bool) {
	denies := aggregate(append(append([]netip.Prefix(nil), gDeny.prefixes...), deny.prefixes...))
	var allowed []netip.Prefix
	switch {
	case gAllow.seen && allow.seen:
		allowed = intersect(gAllow.prefixes, allow.prefixes)
	case gAllow.seen:
		allowed = aggregate(gAllow.prefixes)
	case allow.seen:
		allowed = aggregate(allow.prefixes)
	}
	// Without an effective allow list, the denies are a plain deny-list.
	if (!gAllow.seen && !allow.seen) || coversAll(allowed) {
		switch {
		case len(denies) == 0:
			return nil, false
		case coversAll(denies):
			return nil, true
		}
		return &config.ACL{Mode: "deny", CIDRs: prefixStrings(denies)}, false
	}
	if allowed = subtract(allowed, denies); len(allowed) == 0 {
		return nil, true
	}
	return &config.ACL{Mode: "allow", CIDRs: prefixStrings(allowed)}, false
}

// mergeRanges joins runs of rules that map consecutive ports between the
// same hosts into port ranges, since rinetd has one line per port.
func mergeRanges(entries []entry) []entry {
	var out []entry
	for _, e := range entries {
		if n := len(out); n > 0 && canExtend(&out[n-1].rule, &e.rule) {
			p := &out[n-1].rule
			if p.LocalPortRange == nil {
				p.LocalPortRange = []int{p.LocalPort, p.LocalPort}
				p.TargetPortRange = []int{p.TargetPort, p.TargetPort}
				p.LocalPort, p.TargetPort = 0, 0
			}
			p.LocalPortRange[1]++
			p.TargetPortRange[1]++
			p.Remark = fmt.Sprintf("Imported from rinetd (lines %d-%d)", out[n-1].line, e.line)
			continue
		}
		out = append(out, e)
	}
	return out
}

func canExtend(p, r *config.Rule) bool {
	if p.Type != r.Type || p.LocalHost != r.LocalHost || p.TargetHost != r.TargetHost ||
		p.Status != r.Status || !reflect.DeepEqual(p.ACL, r.ACL) {
		return false
	}
	lastLocal, lastTarget, span := p.LocalPort, p.TargetPort, 1
	if p.LocalPortRange != nil {
		lastLocal, lastTarget = p.LocalPortRange[1], p.TargetPortRange[1]
		span = lastLocal - p.LocalPortRange[0] + 1
	}
	return r.LocalPort == lastLocal+1 && r.TargetPort == lastTarget+1 && span < 1024
}
