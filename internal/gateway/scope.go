package gateway

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lieyanc/FireGateway/internal/config"
)

// QuotaError means a tenant reached one of its limits.
type QuotaError struct{ Msg string }

func (e *QuotaError) Error() string { return e.Msg }

// Scope is the set of rules a caller may manage: every rule for an
// administrator, or the rules owned by one tenant.
type Scope struct{ Tenant string }

// Admin may manage every rule.
var Admin = Scope{}

func (s Scope) Owns(r *config.Rule) bool { return s.Tenant == "" || r.Owner == s.Tenant }

// find returns the index of a rule the scope may see, or -1.
func (s Scope) find(c *config.Config, id config.RuleID) int {
	if i := c.RuleIndex(id); i >= 0 && s.Owns(&c.Forward[i]) {
		return i
	}
	return -1
}

// admit fixes the owner of a new or changed rule and checks it against the
// owning tenant's ports and rule limit. old is nil for new rules.
func (s Scope) admit(c *config.Config, r, old *config.Rule) error {
	if s.Tenant != "" {
		r.Owner = s.Tenant
	}
	if r.Owner == "" {
		return nil
	}
	var t *config.Tenant
	if c.Access != nil {
		t = c.Access.Tenant(r.Owner)
	}
	if t == nil {
		return &config.FieldError{Field: "owner", Msg: fmt.Sprintf("tenant %q does not exist", r.Owner)}
	}
	if !t.Admits(r) {
		return &config.FieldError{Field: "localPort", Msg: "local ports must lie within one of the tenant's ranges: " + formatRanges(t.PortRanges)}
	}
	if max := t.Quota.MaxRules; max > 0 && (old == nil || old.Owner != r.Owner) {
		n := 0
		for i := range c.Forward {
			if c.Forward[i].Owner == r.Owner && (old == nil || c.Forward[i].ID != old.ID) {
				n++
			}
		}
		if n >= max {
			return &QuotaError{fmt.Sprintf("tenant %q may own at most %d rules", t.Name, max)}
		}
	}
	return nil
}

// validate checks listener conflicts without naming rules the scope cannot see.
func (s Scope) validate(m *Manager, c *config.Config) error {
	err := m.validate(c.Forward)
	var ce *ConflictError
	if s.Tenant == "" || !errors.As(err, &ce) {
		return err
	}
	for _, id := range ce.Rules {
		if i := c.RuleIndex(id); i >= 0 && !s.Owns(&c.Forward[i]) {
			return &ConflictError{Msg: "the listen address overlaps a rule outside your tenant"}
		}
	}
	return err
}

func formatRanges(rs []config.PortRange) string {
	if len(rs) == 0 {
		return "none"
	}
	parts := make([]string, len(rs))
	for i, p := range rs {
		if p[0] == p[1] {
			parts[i] = fmt.Sprint(p[0])
		} else {
			parts[i] = fmt.Sprintf("%d-%d", p[0], p[1])
		}
	}
	return strings.Join(parts, ", ")
}
