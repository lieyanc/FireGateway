package auth

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/lieyanc/FireGateway/internal/config"
)

func newService(t *testing.T) (*Service, *config.Store) {
	t.Helper()
	store, _, err := config.Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	return New(store), store
}

// cookieRequest returns a request carrying the cookie a response set.
func cookieRequest(t *testing.T, w *httptest.ResponseRecorder) *http.Request {
	t.Helper()
	r := httptest.NewRequest("GET", "/", nil)
	for _, c := range w.Result().Cookies() {
		r.AddCookie(c)
	}
	return r
}

// legacyCookie signs a session the way releases before multi-user did.
func legacyCookie(username, secret string) *http.Cookie {
	payload := username + "|" + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	return &http.Cookie{Name: CookieName, Value: base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + sign(secret, payload)}
}

func TestMigrationKeepsCredentialsTokensAndSessions(t *testing.T) {
	s, store := newService(t)
	hash, _ := hashPassword("password123")
	plain := tokenPrefix + "legacy-token-value-000000"
	_, err := store.Update(func(c *config.Config) error {
		c.Auth.Username, c.Auth.PasswordHash, c.Auth.SessionSecret = "root", hash, "local-secret-0123456789"
		c.Auth.Tokens = []config.APIToken{{ID: "tok_old", Name: "ci", Hash: hashToken(plain)}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	old := httptest.NewRequest("GET", "/", nil)
	old.AddCookie(legacyCookie("root", "local-secret-0123456789"))
	if p := s.Authenticate(old); p == nil || p.UserID != ActorLegacy {
		t.Fatalf("legacy session before migration: %+v", p)
	}
	if migrated, err := s.Migrate(); err != nil || !migrated {
		t.Fatalf("migrate: %v %v", migrated, err)
	}
	if err := s.DropLegacy(); err != nil {
		t.Fatal(err)
	}
	if s.LegacyPending() {
		t.Fatal("local credentials kept after migration")
	}
	p := s.Authenticate(old)
	if p == nil || !p.Admin() || p.Username != "root" {
		t.Fatalf("legacy session after migration: %+v", p)
	}
	bearer := httptest.NewRequest("GET", "/", nil)
	bearer.Header.Set("Authorization", "Bearer "+plain)
	if bp := s.Authenticate(bearer); bp == nil || bp.UserID != p.UserID {
		t.Fatalf("migrated token: %+v", bp)
	}
	if _, err := s.Login("root", "password123", "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	// A password change retires sessions in the old format too.
	if _, err := s.ChangePassword(p, "password123", "password456", ""); err != nil {
		t.Fatal(err)
	}
	if s.Authenticate(old) != nil {
		t.Fatal("legacy session survived a password change")
	}
}

func TestRecoveryResetsAnAdministrator(t *testing.T) {
	s, store := newService(t)
	tok := s.SetupToken()
	actor, err := s.CheckSetupToken(tok)
	if err != nil || actor != ActorSetup {
		t.Fatalf("setup token: %v %v", actor, err)
	}
	u, err := s.Setup(actor, "admin", "password123")
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	if err := s.SetSession(w, httptest.NewRequest("GET", "/", nil), u.ID); err != nil {
		t.Fatal(err)
	}
	session := cookieRequest(t, w)
	if s.SetupToken() != "" {
		t.Fatal("setup token still offered")
	}
	if _, err := s.CheckSetupToken(tok); err != ErrInitialized {
		t.Fatalf("second setup: %v", err)
	}

	// -reset-auth enables recovery on this node only.
	if _, err := store.Update(func(c *config.Config) error { c.Auth.Recovery = true; return nil }); err != nil {
		t.Fatal(err)
	}
	actor, err = s.CheckSetupToken(s.SetupToken())
	if err != nil || actor != ActorRecovery {
		t.Fatalf("recovery token: %v %v", actor, err)
	}
	if _, err := s.Setup(actor, "admin", "password456"); err != nil {
		t.Fatal(err)
	}
	s.EndRecovery()
	if s.SetupAvailable() {
		t.Fatal("recovery still enabled")
	}
	if s.Authenticate(session) != nil {
		t.Fatal("reset kept the old sessions")
	}
	if _, err := s.Login("admin", "password456", "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	if n := len(s.Users()); n != 1 {
		t.Fatalf("recovery duplicated the account: %d users", n)
	}
}

func TestDisabledUsersCannotSignIn(t *testing.T) {
	s, _ := newService(t)
	admin, err := s.Setup(ActorSetup, "admin", "password123")
	if err != nil {
		t.Fatal(err)
	}
	ap := &Principal{UserID: admin.ID, Role: config.RoleAdmin}
	if _, err := s.CreateTenant(TenantInput{ID: "team1", Name: "Team 1"}); err != nil {
		t.Fatal(err)
	}
	name, pass, tenant := "alice", "password123", "team1"
	u, err := s.CreateUser(UserInput{Username: &name, Password: &pass, TenantID: &tenant})
	if err != nil {
		t.Fatal(err)
	}
	disabled := true
	if _, err := s.UpdateUser(ap, u.ID, UserInput{Disabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login("alice", "password123", "1.2.3.4"); err != ErrInvalidCredentials {
		t.Fatalf("disabled login: %v", err)
	}
	if _, err := s.PrincipalFor(u.ID); err != ErrSignedOut {
		t.Fatalf("disabled actor: %v", err)
	}
	if err := s.DeleteTenant("team1"); err != ErrTenantInUse {
		t.Fatalf("delete tenant in use: %v", err)
	}
	if _, err := s.UpdateUser(ap, admin.ID, UserInput{Disabled: &disabled}); err != ErrSelf {
		t.Fatalf("self disable: %v", err)
	}
}
