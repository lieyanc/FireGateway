package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// Access is the replicated identity state: accounts, tenants and API tokens.
// It travels inside the shared RuleSet so every node authenticates the same
// users and enforces the same tenant boundaries.
type Access struct {
	SessionSecret string     `json:"sessionSecret"`
	Users         []User     `json:"users"`
	Tenants       []Tenant   `json:"tenants"`
	Tokens        []APIToken `json:"tokens"`
}

type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	Role         string    `json:"role"`               // admin | member
	TenantID     string    `json:"tenantId,omitempty"` // required for members
	PasswordHash string    `json:"passwordHash"`
	SessionGen   int64     `json:"sessionGen"` // bumped to sign out the user's sessions
	Disabled     bool      `json:"disabled,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
}

func (u *User) Admin() bool { return u.Role == RoleAdmin }

type Tenant struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	PortRanges []PortRange `json:"portRanges"`
	Quota      Quota       `json:"quota"`
	// UsageResetAt discards traffic counted before it on every node.
	UsageResetAt *time.Time `json:"usageResetAt,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
}

// PortRange is an inclusive [from, to] span of local ports.
type PortRange [2]int

func (p PortRange) Contains(lo, hi int) bool { return p[0] <= lo && hi <= p[1] }

type Quota struct {
	MaxRules     int   `json:"maxRules,omitempty"`     // 0 = unlimited
	MonthlyBytes int64 `json:"monthlyBytes,omitempty"` // up+down per period; 0 = unlimited
	ResetDay     int   `json:"resetDay,omitempty"`     // 1-28; 0 means 1
}

// Clone returns a deep copy; nil stays nil.
func (a *Access) Clone() *Access {
	if a == nil {
		return nil
	}
	b, _ := json.Marshal(a)
	var out Access
	_ = json.Unmarshal(b, &out)
	return &out
}

func (a *Access) User(id string) *User {
	for i := range a.Users {
		if a.Users[i].ID == id {
			return &a.Users[i]
		}
	}
	return nil
}

func (a *Access) UserByName(name string) *User {
	for i := range a.Users {
		if strings.EqualFold(a.Users[i].Username, name) {
			return &a.Users[i]
		}
	}
	return nil
}

func (a *Access) Tenant(id string) *Tenant {
	for i := range a.Tenants {
		if a.Tenants[i].ID == id {
			return &a.Tenants[i]
		}
	}
	return nil
}

// HasAdmin reports whether an enabled administrator with a password exists.
func (a *Access) HasAdmin() bool {
	for _, u := range a.Users {
		if u.Admin() && !u.Disabled && u.PasswordHash != "" {
			return true
		}
	}
	return false
}

// Admits reports whether every local port of r lies within one tenant range.
func (t *Tenant) Admits(r *Rule) bool {
	lo, hi := r.localPorts()
	for _, p := range t.PortRanges {
		if p.Contains(lo, hi) {
			return true
		}
	}
	return false
}

func ValidateUsername(name string) error {
	if n := utf8.RuneCountInString(name); n < 1 || n > 64 || strings.TrimSpace(name) != name {
		return &FieldError{"username", "username must be 1-64 characters without surrounding spaces"}
	}
	return nil
}

func ValidateTenant(t *Tenant) error {
	if err := ValidateID(RuleID(t.ID)); err != nil {
		return &FieldError{"id", err.(*FieldError).Msg}
	}
	if n := utf8.RuneCountInString(t.Name); n < 1 || n > 64 {
		return &FieldError{"name", "name must be 1-64 characters"}
	}
	if len(t.PortRanges) > 64 {
		return &FieldError{"portRanges", "at most 64 port ranges"}
	}
	for i, p := range t.PortRanges {
		if !validPort(p[0]) || !validPort(p[1]) || p[0] > p[1] {
			return &FieldError{fmt.Sprintf("portRanges[%d]", i), "range must be within 1-65535 with start <= end"}
		}
		for j := range i {
			if q := t.PortRanges[j]; p[0] <= q[1] && q[0] <= p[1] {
				return &FieldError{fmt.Sprintf("portRanges[%d]", i), "ranges must not overlap"}
			}
		}
	}
	q := t.Quota
	switch {
	case q.MaxRules < 0:
		return &FieldError{"quota.maxRules", "must not be negative"}
	case q.MonthlyBytes < 0:
		return &FieldError{"quota.monthlyBytes", "must not be negative"}
	case q.ResetDay < 0 || q.ResetDay > 28:
		return &FieldError{"quota.resetDay", "must be between 1 and 28"}
	}
	return nil
}

// Validate checks referential integrity of the access state and the rules
// that refer to it. Port placement is enforced on tenant writes, not here, so
// an administrator may narrow ranges without invalidating stored rules.
func (a *Access) Validate(rules []Rule) error {
	if len(a.SessionSecret) < 16 {
		return fmt.Errorf("access: session secret is missing")
	}
	tenants := map[string]bool{}
	for i := range a.Tenants {
		t := &a.Tenants[i]
		if err := ValidateTenant(t); err != nil {
			return fmt.Errorf("tenant %s: %w", t.ID, err)
		}
		if tenants[t.ID] {
			return fmt.Errorf("duplicate tenant id %s", t.ID)
		}
		tenants[t.ID] = true
		for j := range i {
			for _, p := range t.PortRanges {
				for _, q := range a.Tenants[j].PortRanges {
					if p[0] <= q[1] && q[0] <= p[1] {
						return &FieldError{"portRanges", fmt.Sprintf("ports %d-%d overlap tenant %q", p[0], p[1], a.Tenants[j].Name)}
					}
				}
			}
		}
	}
	users, names := map[string]bool{}, map[string]bool{}
	for _, u := range a.Users {
		if err := ValidateID(RuleID(u.ID)); err != nil || users[u.ID] {
			return fmt.Errorf("invalid or duplicate user id %q", u.ID)
		}
		users[u.ID] = true
		if err := ValidateUsername(u.Username); err != nil {
			return err
		}
		if lower := strings.ToLower(u.Username); names[lower] {
			return &FieldError{"username", fmt.Sprintf("username %q is already taken", u.Username)}
		} else {
			names[lower] = true
		}
		switch u.Role {
		case RoleAdmin:
			if u.TenantID != "" {
				return &FieldError{"tenantId", "administrators do not belong to a tenant"}
			}
		case RoleMember:
			if !tenants[u.TenantID] {
				return &FieldError{"tenantId", fmt.Sprintf("user %q needs an existing tenant", u.Username)}
			}
		default:
			return &FieldError{"role", "role must be admin or member"}
		}
	}
	for _, t := range a.Tokens {
		if !users[t.UserID] {
			return fmt.Errorf("token %s belongs to an unknown user", t.ID)
		}
	}
	for _, r := range rules {
		if r.Owner != "" && !tenants[r.Owner] {
			return fmt.Errorf("rule %s belongs to unknown tenant %q", r.ID, r.Owner)
		}
	}
	return nil
}

// TenantUsage is the traffic one node counted for a tenant since the start
// of its current quota period.
type TenantUsage struct {
	Since time.Time `json:"since"`
	Bytes int64     `json:"bytes"`
}

// PeriodStart returns when the tenant's current quota period began: 00:00
// UTC on the reset day, or the last manual reset if that is later.
func (t *Tenant) PeriodStart(now time.Time) time.Time {
	day := t.Quota.ResetDay
	if day == 0 {
		day = 1
	}
	now = now.UTC()
	start := time.Date(now.Year(), now.Month(), day, 0, 0, 0, 0, time.UTC)
	if now.Before(start) {
		start = start.AddDate(0, -1, 0)
	}
	if t.UsageResetAt != nil && t.UsageResetAt.After(start) {
		start = t.UsageResetAt.UTC()
	}
	return start
}
