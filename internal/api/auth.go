package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/lieyanc/FireGateway/internal/auth"
	"github.com/lieyanc/FireGateway/internal/config"
)

func (s *Server) authState(w http.ResponseWriter, r *http.Request) {
	p := auth.FromContext(r.Context())
	out := map[string]any{"initialized": s.Auth.Initialized(), "setupAvailable": s.Auth.SetupAvailable(), "authenticated": p != nil}
	if p != nil {
		out["userId"], out["username"], out["role"] = p.UserID, p.Username, p.Role
		if p.TenantID != "" {
			out["tenantId"] = p.TenantID
			if a := s.Store.Access(); a != nil {
				if t := a.Tenant(p.TenantID); t != nil {
					out["tenantName"] = t.Name
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type credentials struct {
	SetupToken string `json:"setupToken,omitempty"`
	Username   string `json:"username"`
	Password   string `json:"password"`
}

type account struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

// setup creates an administrator with the setup token. Replicated setups
// arrive with the actor the originating node derived from the token.
func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	var b credentials
	if !decode(w, r, &b, 4<<10) {
		return
	}
	p := auth.FromContext(r.Context())
	replicated := p != nil && p.Method == auth.Peer
	var actor string
	if replicated {
		actor = p.UserID
	} else {
		var err error
		if actor, err = s.Auth.CheckSetupToken(b.SetupToken); err != nil {
			failErr(w, err)
			return
		}
	}
	setup := s.Auth.Setup
	if s.Cluster != nil && !replicated {
		// Only an unpaired cluster node gets here; see withClusterWrites.
		setup = s.Auth.SetupLocal
	}
	u, err := setup(actor, b.Username, b.Password)
	if err != nil {
		failErr(w, err)
		return
	}
	slog.Info("admin account set up", "username", u.Username, "recovery", actor == auth.ActorRecovery, "client", auth.ClientIP(r))
	if !replicated {
		s.Auth.EndRecovery()
		if err := s.Auth.SetSession(w, r, u.ID); err != nil {
			failErr(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, account{u.ID, u.Username})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var b credentials
	if !decode(w, r, &b, 4<<10) {
		return
	}
	ip := auth.ClientIP(r)
	p, err := s.Auth.Login(b.Username, b.Password, ip)
	if err != nil {
		slog.Warn("login failed", "username", b.Username, "client", ip, "err", err)
		failErr(w, err)
		return
	}
	if err := s.Auth.SetSession(w, r, p.UserID); err != nil {
		failErr(w, err)
		return
	}
	slog.Info("login succeeded", "username", p.Username, "client", ip)
	writeJSON(w, http.StatusOK, account{p.UserID, p.Username})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	auth.ClearSession(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var b struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
		Username        string `json:"username"`
	}
	if !decode(w, r, &b, 4<<10) {
		return
	}
	p := auth.FromContext(r.Context())
	u, err := s.Auth.ChangePassword(p, b.CurrentPassword, b.NewPassword, b.Username)
	if err != nil {
		failErr(w, err)
		return
	}
	slog.Info("credentials changed", "username", u.Username, "client", auth.ClientIP(r))
	if p.Method != auth.Peer {
		if err := s.Auth.SetSession(w, r, u.ID); err != nil {
			failErr(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, account{u.ID, u.Username})
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": s.Auth.Tokens(auth.FromContext(r.Context()))})
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &b, 4<<10) {
		return
	}
	p := auth.FromContext(r.Context())
	plain, item, err := s.Auth.CreateToken(p, b.Name)
	if err != nil {
		failErr(w, err)
		return
	}
	slog.Info("API token created", "tokenId", item.ID, "name", item.Name, "username", p.Username)
	writeJSON(w, http.StatusCreated, map[string]any{"token": plain, "item": item})
}

func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p := auth.FromContext(r.Context())
	if err := s.Auth.DeleteToken(p, id); err != nil {
		failErr(w, err)
		return
	}
	slog.Info("API token revoked", "tokenId", id, "username", p.Username)
	w.WriteHeader(http.StatusNoContent)
}

// Users and tenants (administrators only).

func (s *Server) listUsers(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": s.Auth.Users()})
}

func (s *Server) getUser(w http.ResponseWriter, r *http.Request) {
	u, err := s.Auth.User(r.PathValue("id"))
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var b auth.UserInput
	if !decode(w, r, &b, 4<<10) {
		return
	}
	u, err := s.Auth.CreateUser(b)
	if err != nil {
		failErr(w, err)
		return
	}
	slog.Info("user created", "userId", u.ID, "username", u.Username, "role", u.Role, "tenantId", u.TenantID, "by", auth.FromContext(r.Context()).Username)
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	var b auth.UserInput
	if !decode(w, r, &b, 4<<10) {
		return
	}
	p := auth.FromContext(r.Context())
	u, err := s.Auth.UpdateUser(p, r.PathValue("id"), b)
	if err != nil {
		failErr(w, err)
		return
	}
	slog.Info("user updated", "userId", u.ID, "username", u.Username, "role", u.Role, "tenantId", u.TenantID, "disabled", u.Disabled, "by", p.Username)
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p := auth.FromContext(r.Context())
	if err := s.Auth.DeleteUser(p, id); err != nil {
		failErr(w, err)
		return
	}
	slog.Info("user deleted", "userId", id, "by", p.Username)
	w.WriteHeader(http.StatusNoContent)
}

type tenantView struct {
	config.Tenant
	Rules     int                `json:"rules"`
	Users     int                `json:"users"`
	Usage     config.TenantUsage `json:"usage"`
	Suspended bool               `json:"suspended"`
}

func (s *Server) tenantView(t config.Tenant) tenantView {
	v := tenantView{Tenant: t, Suspended: s.Manager.Suspended(t.ID)}
	for _, r := range s.Manager.List() {
		if r.Owner == t.ID {
			v.Rules++
		}
	}
	if a := s.Store.Access(); a != nil {
		for _, u := range a.Users {
			if u.TenantID == t.ID {
				v.Users++
			}
		}
	}
	if s.Quota != nil {
		v.Usage = s.Quota.Used(&t)
	} else {
		v.Usage.Since = t.PeriodStart(time.Now())
	}
	return v
}

func (s *Server) listTenants(w http.ResponseWriter, _ *http.Request) {
	ts := s.Auth.Tenants()
	items := make([]tenantView, len(ts))
	for i, t := range ts {
		items[i] = s.tenantView(t)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getTenant(w http.ResponseWriter, r *http.Request) {
	t, err := s.Auth.Tenant(r.PathValue("id"))
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.tenantView(t))
}

// ownTenant shows a member its tenant's ports, limits and usage.
func (s *Server) ownTenant(w http.ResponseWriter, r *http.Request) {
	p := auth.FromContext(r.Context())
	if p.TenantID == "" {
		fail(w, http.StatusNotFound, "not_found", "administrators do not belong to a tenant")
		return
	}
	t, err := s.Auth.Tenant(p.TenantID)
	if err != nil {
		failErr(w, err)
		return
	}
	v := s.tenantView(t)
	v.Users = 0 // other members are not listed to tenants
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) createTenant(w http.ResponseWriter, r *http.Request) {
	var b auth.TenantInput
	if !decode(w, r, &b, 16<<10) {
		return
	}
	t, err := s.Auth.CreateTenant(b)
	if err != nil {
		failErr(w, err)
		return
	}
	slog.Info("tenant created", "tenantId", t.ID, "name", t.Name, "by", auth.FromContext(r.Context()).Username)
	writeJSON(w, http.StatusCreated, s.tenantView(t))
}

func (s *Server) updateTenant(w http.ResponseWriter, r *http.Request) {
	var b auth.TenantInput
	if !decode(w, r, &b, 16<<10) {
		return
	}
	t, err := s.Auth.UpdateTenant(r.PathValue("id"), b)
	if err != nil {
		failErr(w, err)
		return
	}
	slog.Info("tenant updated", "tenantId", t.ID, "name", t.Name, "by", auth.FromContext(r.Context()).Username)
	writeJSON(w, http.StatusOK, s.tenantView(t))
}

func (s *Server) deleteTenant(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Auth.DeleteTenant(id); err != nil {
		failErr(w, err)
		return
	}
	slog.Info("tenant deleted", "tenantId", id, "by", auth.FromContext(r.Context()).Username)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) resetTenantUsage(w http.ResponseWriter, r *http.Request) {
	t, err := s.Auth.ResetUsage(r.PathValue("id"))
	if err != nil {
		failErr(w, err)
		return
	}
	slog.Info("tenant traffic usage reset", "tenantId", t.ID, "by", auth.FromContext(r.Context()).Username)
	writeJSON(w, http.StatusOK, s.tenantView(t))
}
