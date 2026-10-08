package auth

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/lieyanc/FireGateway/internal/config"
)

var (
	ErrLastAdmin   = errors.New("at least one enabled administrator is required")
	ErrSelf        = errors.New("you cannot delete, disable or demote your own account")
	ErrTenantInUse = errors.New("tenant still has users or rules; move or delete them first")
	ErrNoTenant    = errors.New("tenant not found")
)

// promote converts the node-local administrator of older releases into
// shared accounts, keeping its password, tokens and session secret.
func promote(l config.AuthConfig) *config.Access {
	a := &config.Access{SessionSecret: l.SessionSecret, Users: []config.User{}, Tenants: []config.Tenant{}, Tokens: []config.APIToken{}}
	if len(a.SessionSecret) < 16 {
		a.SessionSecret = randomString(32)
	}
	if l.Username != "" && l.PasswordHash != "" {
		id := newID("u")
		a.Users = append(a.Users, config.User{ID: id, Username: l.Username, Role: config.RoleAdmin, PasswordHash: l.PasswordHash, CreatedAt: now()})
		for _, t := range l.Tokens {
			t.UserID = id
			a.Tokens = append(a.Tokens, t)
		}
	}
	return a
}

// Migrate moves the node-local administrator into the shared accounts. It
// does nothing once they exist or when no administrator is configured.
func (s *Service) Migrate() (bool, error) {
	if s.store.Access() != nil {
		return false, nil
	}
	if l := s.store.Local().Auth; l.Username == "" || l.PasswordHash == "" {
		return false, nil
	}
	migrated := false
	_, err := s.store.Update(func(c *config.Config) error {
		if c.Access == nil && c.Auth.Username != "" && c.Auth.PasswordHash != "" {
			c.Access, migrated = promote(c.Auth), true
		}
		return nil
	})
	return migrated, err
}

// LegacyPending reports whether node-local credentials remain to be dropped
// or migrated.
func (s *Service) LegacyPending() bool {
	l := s.store.Local().Auth
	return l.Username != "" || l.PasswordHash != "" || l.SessionSecret != "" || len(l.Tokens) > 0
}

// DropLegacy removes the node-local credentials once shared accounts exist.
func (s *Service) DropLegacy() error {
	a := s.store.Access()
	if a == nil || !s.LegacyPending() {
		return nil
	}
	l := s.store.Local().Auth
	if l.Username != "" && a.UserByName(l.Username) == nil {
		slog.Warn("local administrator was not migrated; the cluster accounts replace it", "username", l.Username)
	}
	for _, t := range l.Tokens {
		if !slices.ContainsFunc(a.Tokens, func(x config.APIToken) bool { return x.Hash == t.Hash }) {
			slog.Warn("API token of the local administrator was not migrated; create a new one", "tokenId", t.ID, "name", t.Name)
		}
	}
	_, err := s.store.Update(func(c *config.Config) error {
		c.Auth.Username, c.Auth.PasswordHash, c.Auth.SessionSecret, c.Auth.Tokens = "", "", "", nil
		return nil
	})
	return err
}

// Users.

type UserView struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Role      string    `json:"role"`
	TenantID  string    `json:"tenantId,omitempty"`
	Disabled  bool      `json:"disabled"`
	Tokens    int       `json:"tokens"`
	CreatedAt time.Time `json:"createdAt"`
}

func userView(a *config.Access, u *config.User) UserView {
	return UserView{u.ID, u.Username, u.Role, u.TenantID, u.Disabled, countTokens(a, u.ID), u.CreatedAt}
}

func (s *Service) Users() []UserView {
	out := []UserView{}
	if a := s.store.Access(); a != nil {
		for i := range a.Users {
			out = append(out, userView(a, &a.Users[i]))
		}
	}
	return out
}

func (s *Service) User(id string) (UserView, error) {
	a := s.store.Access()
	if a == nil || a.User(id) == nil {
		return UserView{}, ErrUserNotFound
	}
	return userView(a, a.User(id)), nil
}

// UserInput is a create or partial update; nil fields are left unchanged.
type UserInput struct {
	Username *string `json:"username"`
	Password *string `json:"password"`
	Role     *string `json:"role"`
	TenantID *string `json:"tenantId"`
	Disabled *bool   `json:"disabled"`
}

func (s *Service) CreateUser(in UserInput) (UserView, error) {
	if in.Username == nil || in.Password == nil {
		return UserView{}, &config.FieldError{Field: "password", Msg: "username and password are required"}
	}
	if in.Role == nil {
		member := config.RoleMember
		in.Role = &member
	}
	u := config.User{ID: newID("u"), CreatedAt: now()}
	if err := applyUser(&u, in); err != nil {
		return UserView{}, err
	}
	var out UserView
	_, err := s.store.Update(func(c *config.Config) error {
		if c.Access == nil {
			return ErrMigrating
		}
		if c.Access.UserByName(u.Username) != nil {
			return &config.FieldError{Field: "username", Msg: fmt.Sprintf("username %q is already taken", u.Username)}
		}
		if err := checkTenant(c.Access, &u); err != nil {
			return err
		}
		c.Access.Users = append(c.Access.Users, u)
		out = userView(c.Access, &u)
		return nil
	})
	return out, err
}

func (s *Service) UpdateUser(p *Principal, id string, in UserInput) (UserView, error) {
	var out UserView
	_, err := s.store.Update(func(c *config.Config) error {
		if c.Access == nil {
			return ErrMigrating
		}
		u := c.Access.User(id)
		if u == nil {
			return ErrUserNotFound
		}
		before := *u
		if in.Username != nil {
			if o := c.Access.UserByName(*in.Username); o != nil && o.ID != id {
				return &config.FieldError{Field: "username", Msg: fmt.Sprintf("username %q is already taken", *in.Username)}
			}
		}
		if err := applyUser(u, in); err != nil {
			return err
		}
		if id == p.UserID && (u.Disabled || !u.Admin()) {
			return ErrSelf
		}
		if err := checkTenant(c.Access, u); err != nil {
			return err
		}
		if !c.Access.HasAdmin() {
			return ErrLastAdmin
		}
		if u.Disabled && !before.Disabled {
			u.SessionGen++
		}
		out = userView(c.Access, u)
		return nil
	})
	return out, err
}

// DeleteUser removes an account together with its API tokens.
func (s *Service) DeleteUser(p *Principal, id string) error {
	if id == p.UserID {
		return ErrSelf
	}
	_, err := s.store.Update(func(c *config.Config) error {
		if c.Access == nil {
			return ErrMigrating
		}
		i := slices.IndexFunc(c.Access.Users, func(u config.User) bool { return u.ID == id })
		if i < 0 {
			return ErrUserNotFound
		}
		c.Access.Users = slices.Delete(c.Access.Users, i, i+1)
		c.Access.Tokens = slices.DeleteFunc(c.Access.Tokens, func(t config.APIToken) bool { return t.UserID == id })
		if !c.Access.HasAdmin() {
			return ErrLastAdmin
		}
		return nil
	})
	return err
}

func applyUser(u *config.User, in UserInput) error {
	if in.Username != nil {
		if err := config.ValidateUsername(*in.Username); err != nil {
			return err
		}
		u.Username = *in.Username
	}
	if in.Password != nil {
		if err := validatePassword("password", *in.Password); err != nil {
			return err
		}
		hash, err := hashPassword(*in.Password)
		if err != nil {
			return err
		}
		u.PasswordHash = hash
		u.SessionGen++
	}
	if in.Role != nil {
		if *in.Role != config.RoleAdmin && *in.Role != config.RoleMember {
			return &config.FieldError{Field: "role", Msg: "role must be admin or member"}
		}
		u.Role = *in.Role
	}
	if in.TenantID != nil {
		u.TenantID = *in.TenantID
	}
	if u.Admin() {
		u.TenantID = ""
	}
	if in.Disabled != nil {
		u.Disabled = *in.Disabled
	}
	return nil
}

func checkTenant(a *config.Access, u *config.User) error {
	if !u.Admin() && a.Tenant(u.TenantID) == nil {
		return &config.FieldError{Field: "tenantId", Msg: "members need an existing tenant"}
	}
	return nil
}

// Tenants.

type TenantInput struct {
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	PortRanges []config.PortRange `json:"portRanges"`
	Quota      config.Quota       `json:"quota"`
}

func (in TenantInput) apply(t *config.Tenant) error {
	t.Name, t.PortRanges, t.Quota = in.Name, in.PortRanges, in.Quota
	if t.PortRanges == nil {
		t.PortRanges = []config.PortRange{}
	}
	slices.SortFunc(t.PortRanges, func(a, b config.PortRange) int { return a[0] - b[0] })
	return config.ValidateTenant(t)
}

func (s *Service) Tenants() []config.Tenant {
	if a := s.store.Access(); a != nil {
		return a.Tenants
	}
	return []config.Tenant{}
}

func (s *Service) Tenant(id string) (config.Tenant, error) {
	if a := s.store.Access(); a != nil {
		if t := a.Tenant(id); t != nil {
			return *t, nil
		}
	}
	return config.Tenant{}, ErrNoTenant
}

func (s *Service) CreateTenant(in TenantInput) (config.Tenant, error) {
	t := config.Tenant{ID: in.ID, CreatedAt: now()}
	if t.ID == "" {
		t.ID = newID("t")
	}
	if err := in.apply(&t); err != nil {
		return t, err
	}
	_, err := s.store.Update(func(c *config.Config) error {
		if c.Access == nil {
			return ErrMigrating
		}
		if c.Access.Tenant(t.ID) != nil {
			return &config.FieldError{Field: "id", Msg: fmt.Sprintf("tenant id %q already exists", t.ID)}
		}
		c.Access.Tenants = append(c.Access.Tenants, t)
		return nil
	})
	return t, err
}

// UpdateTenant changes a tenant's name, ports and quota. Its rules must
// stay inside the new port ranges.
func (s *Service) UpdateTenant(id string, in TenantInput) (config.Tenant, error) {
	var out config.Tenant
	_, err := s.store.Update(func(c *config.Config) error {
		if c.Access == nil {
			return ErrMigrating
		}
		t := c.Access.Tenant(id)
		if t == nil {
			return ErrNoTenant
		}
		if err := in.apply(t); err != nil {
			return err
		}
		for i := range c.Forward {
			if r := &c.Forward[i]; r.Owner == id && !t.Admits(r) {
				return &config.FieldError{Field: "portRanges", Msg: fmt.Sprintf("rule %q listens outside the new port ranges", r.ID)}
			}
		}
		out = *t
		return nil
	})
	return out, err
}

func (s *Service) DeleteTenant(id string) error {
	_, err := s.store.Update(func(c *config.Config) error {
		if c.Access == nil {
			return ErrMigrating
		}
		i := slices.IndexFunc(c.Access.Tenants, func(t config.Tenant) bool { return t.ID == id })
		if i < 0 {
			return ErrNoTenant
		}
		if slices.ContainsFunc(c.Access.Users, func(u config.User) bool { return u.TenantID == id }) ||
			slices.ContainsFunc(c.Forward, func(r config.Rule) bool { return r.Owner == id }) {
			return ErrTenantInUse
		}
		c.Access.Tenants = slices.Delete(c.Access.Tenants, i, i+1)
		return nil
	})
	return err
}

// ResetUsage restarts a tenant's traffic count on every node.
func (s *Service) ResetUsage(id string) (config.Tenant, error) {
	var out config.Tenant
	_, err := s.store.Update(func(c *config.Config) error {
		if c.Access == nil {
			return ErrMigrating
		}
		t := c.Access.Tenant(id)
		if t == nil {
			return ErrNoTenant
		}
		at := time.Now().UTC()
		t.UsageResetAt = &at
		out = *t
		return nil
	})
	return out, err
}
