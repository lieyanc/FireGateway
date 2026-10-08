package config

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
)

type Rule struct {
	ID              RuleID  `json:"id"`
	Name            string  `json:"name,omitempty"`
	Type            string  `json:"type"`
	Status          string  `json:"status"`
	LocalHost       string  `json:"localHost"`
	LocalPort       int     `json:"localPort,omitempty"`
	TargetHost      string  `json:"targetHost"`
	TargetPort      int     `json:"targetPort,omitempty"`
	LocalPortRange  []int   `json:"localPortRange,omitempty"`
	TargetPortRange []int   `json:"targetPortRange,omitempty"`
	Remark          string  `json:"remark,omitempty"`
	ACL             *ACL    `json:"acl,omitempty"`
	Limits          *Limits `json:"limits,omitempty"`
	Owner           string  `json:"owner,omitempty"` // tenant id; empty = administrator rule
}

type ACL struct {
	Mode  string   `json:"mode"` // allow | deny
	CIDRs []string `json:"cidrs"`
}

type Limits struct {
	MaxConnections      int   `json:"maxConnections,omitempty"`
	MaxConnectionsPerIP int   `json:"maxConnectionsPerIp,omitempty"`
	Bandwidth           int64 `json:"bandwidth,omitempty"` // bytes/s per direction
}

const (
	StatusActive   = "active"
	StatusInactive = "inactive"
)

func (r *Rule) Active() bool { return r.Status == StatusActive }

// RuleID accepts both numeric and string ids, as the legacy config did, and
// writes purely numeric ids back as numbers so hand-written files keep their shape.
type RuleID string

func (id *RuleID) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*id = RuleID(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("id must be a string or number: %s", b)
	}
	*id = RuleID(n.String())
	return nil
}

func (id RuleID) MarshalJSON() ([]byte, error) {
	s := string(id)
	if isCanonicalInt(s) {
		return []byte(s), nil
	}
	return json.Marshal(s)
}

func isCanonicalInt(s string) bool {
	if s == "" || len(s) > 15 || (s[0] == '0' && len(s) > 1) {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Mapping is one local port forwarded to one target port.
type Mapping struct {
	Local, Target int
}

// Mappings expands a rule into its port pairs; ranges take precedence over single ports.
func (r *Rule) Mappings() ([]Mapping, error) {
	if r.LocalPortRange != nil || r.TargetPortRange != nil {
		lr, tr := r.LocalPortRange, r.TargetPortRange
		if len(lr) != 2 || len(tr) != 2 || lr[0] > lr[1] || tr[0] > tr[1] || lr[1]-lr[0] != tr[1]-tr[0] {
			return nil, &FieldError{"localPortRange", fmt.Sprintf("invalid port range local=%v target=%v", lr, tr)}
		}
		if !validPort(lr[0]) || !validPort(lr[1]) {
			return nil, &FieldError{"localPortRange", fmt.Sprintf("port out of range: %v", lr)}
		}
		if !validPort(tr[0]) || !validPort(tr[1]) {
			return nil, &FieldError{"targetPortRange", fmt.Sprintf("port out of range: %v", tr)}
		}
		m := make([]Mapping, 0, lr[1]-lr[0]+1)
		for i := 0; i <= lr[1]-lr[0]; i++ {
			m = append(m, Mapping{lr[0] + i, tr[0] + i})
		}
		return m, nil
	}
	if !validPort(r.LocalPort) {
		return nil, &FieldError{"localPort", "localPort must be 1-65535"}
	}
	if !validPort(r.TargetPort) {
		return nil, &FieldError{"targetPort", "targetPort must be 1-65535"}
	}
	return []Mapping{{r.LocalPort, r.TargetPort}}, nil
}

func validPort(p int) bool { return p > 0 && p < 65536 }

// FieldError is a validation failure tied to a JSON field path.
type FieldError struct {
	Field, Msg string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Msg }

const maxRangePorts = 1024

// Validate checks a rule strictly, as required for rules submitted via the API.
func (r *Rule) Validate() error {
	if err := ValidateID(r.ID); err != nil {
		return err
	}
	if r.Owner != "" {
		if err := ValidateID(RuleID(r.Owner)); err != nil {
			return &FieldError{"owner", "invalid tenant id"}
		}
	}
	if len(r.Name) > 128 {
		return &FieldError{"name", "name must be at most 128 characters"}
	}
	if len(r.Remark) > 1024 {
		return &FieldError{"remark", "remark must be at most 1024 characters"}
	}
	if r.Type != "tcp" && r.Type != "udp" {
		return &FieldError{"type", "type must be tcp or udp"}
	}
	if r.Status != StatusActive && r.Status != StatusInactive {
		return &FieldError{"status", "status must be active or inactive"}
	}
	if r.LocalHost != "" && !validHost(r.LocalHost) {
		return &FieldError{"localHost", "invalid listen address"}
	}
	if r.TargetHost == "" || !validHost(r.TargetHost) {
		return &FieldError{"targetHost", "invalid target host"}
	}
	m, err := r.Mappings()
	if err != nil {
		return err
	}
	if len(m) > maxRangePorts {
		return &FieldError{"localPortRange", fmt.Sprintf("a range may span at most %d ports", maxRangePorts)}
	}
	if r.ACL != nil {
		if r.ACL.Mode != "allow" && r.ACL.Mode != "deny" {
			return &FieldError{"acl.mode", "mode must be allow or deny"}
		}
		for i, c := range r.ACL.CIDRs {
			if _, err := ParsePrefix(c); err != nil {
				return &FieldError{fmt.Sprintf("acl.cidrs[%d]", i), err.Error()}
			}
		}
	}
	if l := r.Limits; l != nil {
		switch {
		case l.MaxConnections < 0:
			return &FieldError{"limits.maxConnections", "must not be negative"}
		case l.MaxConnectionsPerIP < 0:
			return &FieldError{"limits.maxConnectionsPerIp", "must not be negative"}
		case l.Bandwidth < 0:
			return &FieldError{"limits.bandwidth", "must not be negative"}
		}
	}
	return nil
}

func ValidateID(id RuleID) error {
	if id == "" || len(id) > 64 {
		return &FieldError{"id", "id must be 1-64 characters"}
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return &FieldError{"id", "id may only contain letters, digits, '-', '_' and '.'"}
		}
	}
	return nil
}

// Normalize trims user input and drops empty optional sections.
func (r *Rule) Normalize() {
	r.Name = strings.TrimSpace(r.Name)
	r.LocalHost = strings.TrimSpace(r.LocalHost)
	r.TargetHost = strings.TrimSpace(r.TargetHost)
	r.Remark = strings.TrimSpace(r.Remark)
	if r.Status == "" {
		r.Status = StatusActive
	}
	if r.LocalPortRange != nil || r.TargetPortRange != nil {
		r.LocalPort, r.TargetPort = 0, 0
	}
	if r.ACL != nil {
		cidrs := r.ACL.CIDRs[:0]
		for _, c := range r.ACL.CIDRs {
			if c = strings.TrimSpace(c); c != "" {
				cidrs = append(cidrs, c)
			}
		}
		r.ACL.CIDRs = cidrs
		if len(cidrs) == 0 {
			r.ACL = nil
		}
	}
	if l := r.Limits; l != nil && *l == (Limits{}) {
		r.Limits = nil
	}
}

func validHost(h string) bool {
	if _, err := netip.ParseAddr(strings.Trim(h, "[]")); err == nil {
		return true
	}
	if len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return false
			}
		}
	}
	return true
}

// ParsePrefix accepts a CIDR or a bare IP (treated as a single-host prefix).
func ParsePrefix(s string) (netip.Prefix, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return p, fmt.Errorf("invalid CIDR %q", s)
		}
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("invalid IP %q", s)
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// BindKey identifies what a rule listens on; rules with equal keys can be
// updated in place without re-binding.
func (r *Rule) BindKey() string {
	return fmt.Sprintf("%s|%s|%d|%v", r.Type, r.LocalHost, r.LocalPort, r.LocalPortRange)
}

// localPorts returns the inclusive local port span of a rule.
func (r *Rule) localPorts() (lo, hi int) {
	if len(r.LocalPortRange) == 2 {
		return r.LocalPortRange[0], r.LocalPortRange[1]
	}
	return r.LocalPort, r.LocalPort
}

// ListenOverlaps reports whether two rules would bind the same socket.
func (r *Rule) ListenOverlaps(o *Rule) bool {
	if r.Type != o.Type {
		return false
	}
	alo, ahi := r.localPorts()
	blo, bhi := o.localPorts()
	if ahi < blo || bhi < alo {
		return false
	}
	return r.LocalHost == o.LocalHost || isWildcard(r.LocalHost) || isWildcard(o.LocalHost)
}

func isWildcard(h string) bool {
	return h == "" || h == "0.0.0.0" || h == "::" || h == "[::]"
}
