package api

import (
	"log/slog"
	"net/http"

	"github.com/lieyanc/FireGateway/internal/auth"
)

func (s *Server) authState(w http.ResponseWriter, r *http.Request) {
	user, m := s.Auth.Authenticate(r)
	out := map[string]any{"initialized": s.Auth.Initialized(), "authenticated": m != auth.None}
	if m != auth.None {
		out["username"] = user
	}
	writeJSON(w, http.StatusOK, out)
}

type credentials struct {
	SetupToken string `json:"setupToken"`
	Username   string `json:"username"`
	Password   string `json:"password"`
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	var b credentials
	if !decode(w, r, &b, 4<<10) {
		return
	}
	if err := s.Auth.Setup(b.SetupToken, b.Username, b.Password); err != nil {
		failErr(w, err)
		return
	}
	slog.Info("admin account created", "username", b.Username, "client", auth.ClientIP(r))
	s.Auth.SetSession(w, r)
	writeJSON(w, http.StatusOK, map[string]string{"username": b.Username})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var b credentials
	if !decode(w, r, &b, 4<<10) {
		return
	}
	ip := auth.ClientIP(r)
	if err := s.Auth.Login(b.Username, b.Password, ip); err != nil {
		slog.Warn("login failed", "username", b.Username, "client", ip, "err", err)
		failErr(w, err)
		return
	}
	slog.Info("login succeeded", "username", b.Username, "client", ip)
	s.Auth.SetSession(w, r)
	writeJSON(w, http.StatusOK, map[string]string{"username": b.Username})
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
	if err := s.Auth.ChangePassword(b.CurrentPassword, b.NewPassword, b.Username); err != nil {
		failErr(w, err)
		return
	}
	user := s.Store.Get().Auth.Username
	slog.Info("admin credentials changed", "username", user, "client", auth.ClientIP(r))
	s.Auth.SetSession(w, r)
	writeJSON(w, http.StatusOK, map[string]string{"username": user})
}

func (s *Server) listTokens(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": s.Auth.Tokens()})
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &b, 4<<10) {
		return
	}
	plain, item, err := s.Auth.CreateToken(b.Name)
	if err != nil {
		failErr(w, err)
		return
	}
	slog.Info("API token created", "tokenId", item.ID, "name", item.Name)
	writeJSON(w, http.StatusCreated, map[string]any{"token": plain, "item": item})
}

func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Auth.DeleteToken(id); err != nil {
		failErr(w, err)
		return
	}
	slog.Info("API token revoked", "tokenId", id)
	w.WriteHeader(http.StatusNoContent)
}
