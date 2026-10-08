// Package auth implements multi-user login against the replicated accounts:
// bcrypt passwords, HMAC-signed session cookies, hashed API tokens and
// brute-force throttling. Until the node-local administrator of older
// releases has been migrated into the shared accounts, it is used as is.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"github.com/lieyanc/FireGateway/internal/config"
)

const (
	CookieName  = "fg_session"
	tokenPrefix = "fgw_"
)

// Reserved actors of replicated mutations. Real users never start with '@'.
const (
	ActorLegacy   = "@legacy"   // the unmigrated node-local administrator
	ActorSystem   = "@system"   // internal maintenance such as migration
	ActorSetup    = "@setup"    // a verified first-time setup token
	ActorRecovery = "@recovery" // a verified setup token in recovery mode
)

var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrNotInitialized     = errors.New("admin account not set up")
	ErrInitialized        = errors.New("admin account already set up")
	ErrBadSetupToken      = errors.New("invalid setup token")
	ErrNotFound           = errors.New("token not found")
	ErrUserNotFound       = errors.New("user not found")
	ErrSignedOut          = errors.New("session is no longer valid; sign in again")
	// ErrMigrating means accounts still live in the node config and cannot be
	// changed until the cluster has migrated them into the shared state.
	ErrMigrating = errors.New("accounts are being migrated to the cluster; retry shortly")
)

// RateLimitError means the client must wait before trying again.
type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("too many failed attempts, retry in %s", e.RetryAfter.Round(time.Second))
}

// Method tells how a request was authenticated.
type Method int

const (
	None Method = iota
	Cookie
	Bearer
	Peer // replayed from the originating node of a cluster write
)

// Principal is an authenticated caller.
type Principal struct {
	UserID   string // a user id or a reserved actor
	Username string
	Role     string
	TenantID string
	Method   Method
}

func (p *Principal) Admin() bool { return p != nil && p.Role == config.RoleAdmin }

type ctxKey struct{}

func NewContext(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the caller, or nil for anonymous requests.
func FromContext(ctx context.Context) *Principal {
	p, _ := ctx.Value(ctxKey{}).(*Principal)
	return p
}

type Service struct {
	store    *config.Store
	throttle *throttle

	mu         sync.Mutex
	setupToken string
}

func New(store *config.Store) *Service {
	return &Service{store: store, throttle: newThrottle(), setupToken: randomString(12)}
}

func (s *Service) token() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setupToken
}

// Initialized reports whether an administrator can sign in.
func (s *Service) Initialized() bool {
	if a := s.store.Access(); a != nil {
		return a.HasAdmin()
	}
	l := s.store.Local().Auth
	return l.Username != "" && l.PasswordHash != ""
}

// SetupAvailable reports whether the setup token may create an administrator:
// before the first one exists, or after -reset-auth on this node.
func (s *Service) SetupAvailable() bool {
	return s.store.Local().Auth.Recovery || !s.Initialized()
}

// SetupToken is the one-time secret required to create an administrator;
// empty when setup is unavailable.
func (s *Service) SetupToken() string {
	if !s.SetupAvailable() {
		return ""
	}
	return s.token()
}

// CheckSetupToken verifies a setup token on the node that printed it and
// returns the actor that may perform the setup anywhere in the cluster.
func (s *Service) CheckSetupToken(token string) (string, error) {
	if !s.SetupAvailable() {
		return "", ErrInitialized
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(token)), []byte(s.token())) != 1 {
		return "", ErrBadSetupToken
	}
	if s.store.Local().Auth.Recovery {
		return ActorRecovery, nil
	}
	return ActorSetup, nil
}

// EndRecovery clears the local recovery flag after a successful setup.
func (s *Service) EndRecovery() {
	if !s.store.Local().Auth.Recovery {
		return
	}
	s.mu.Lock()
	s.setupToken = randomString(12)
	s.mu.Unlock()
	s.store.Update(func(c *config.Config) error { c.Auth.Recovery = false; return nil })
}

func validatePassword(field, pass string) error {
	if n := utf8.RuneCountInString(pass); n < 8 || n > 128 {
		return &config.FieldError{Field: field, Msg: "password must be 8-128 characters"}
	}
	return nil
}

func hashPassword(pass string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	return string(h), err
}

// Setup creates the first administrator, or in recovery mode creates or
// resets one, for an actor returned by CheckSetupToken.
func (s *Service) Setup(actor, username, pass string) (config.User, error) {
	if actor != ActorSetup && actor != ActorRecovery {
		return config.User{}, ErrBadSetupToken
	}
	if err := config.ValidateUsername(username); err != nil {
		return config.User{}, err
	}
	if err := validatePassword("password", pass); err != nil {
		return config.User{}, err
	}
	hash, err := hashPassword(pass)
	if err != nil {
		return config.User{}, err
	}
	var out config.User
	_, err = s.store.Update(func(c *config.Config) error {
		a := c.Access
		if a == nil {
			a = promote(c.Auth)
		}
		if actor == ActorSetup && a.HasAdmin() {
			return ErrInitialized
		}
		u := a.UserByName(username)
		if u == nil {
			a.Users = append(a.Users, config.User{ID: newID("u"), CreatedAt: now()})
			u = &a.Users[len(a.Users)-1]
		}
		u.Username, u.Role, u.TenantID, u.Disabled = username, config.RoleAdmin, "", false
		u.PasswordHash = hash
		u.SessionGen++
		out = *u
		c.Access = a
		return nil
	})
	return out, err
}

// SetupLocal creates the node-local administrator of a cluster node that is
// not paired yet, so the shared accounts cannot be written. The cluster
// migrates it into the shared accounts once the nodes are paired.
func (s *Service) SetupLocal(actor, username, pass string) (config.User, error) {
	if s.store.Access() != nil {
		return s.Setup(actor, username, pass)
	}
	if actor != ActorSetup && actor != ActorRecovery {
		return config.User{}, ErrBadSetupToken
	}
	if err := config.ValidateUsername(username); err != nil {
		return config.User{}, err
	}
	if err := validatePassword("password", pass); err != nil {
		return config.User{}, err
	}
	hash, err := hashPassword(pass)
	if err != nil {
		return config.User{}, err
	}
	_, err = s.store.Update(func(c *config.Config) error {
		if actor == ActorSetup && c.Auth.Username != "" && c.Auth.PasswordHash != "" {
			return ErrInitialized
		}
		c.Auth.Username, c.Auth.PasswordHash = username, hash
		// A new secret signs out sessions of a replaced administrator.
		c.Auth.SessionSecret = randomString(32)
		return nil
	})
	return config.User{ID: ActorLegacy, Username: username, Role: config.RoleAdmin}, err
}

// Login checks credentials, throttling per client IP.
func (s *Service) Login(username, pass, ip string) (*Principal, error) {
	if !s.Initialized() {
		return nil, ErrNotInitialized
	}
	if wait := s.throttle.blocked(ip); wait > 0 {
		return nil, &RateLimitError{wait}
	}
	var p *Principal
	hash := dummyHash
	if a := s.store.Access(); a != nil {
		if u := a.UserByName(username); u != nil && !u.Disabled {
			hash, p = u.PasswordHash, principal(u)
		}
	} else if l := s.store.Local().Auth; subtle.ConstantTimeCompare([]byte(username), []byte(l.Username)) == 1 {
		hash, p = l.PasswordHash, legacyPrincipal(l)
	}
	// Always run bcrypt so response time does not reveal the username.
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(pass)) != nil || p == nil {
		s.throttle.fail(ip)
		return nil, ErrInvalidCredentials
	}
	s.throttle.reset(ip)
	p.Method = Cookie
	return p, nil
}

// dummyHash keeps unknown usernames as slow as real ones.
var dummyHash, _ = hashPassword(randomString(16))

func principal(u *config.User) *Principal {
	return &Principal{UserID: u.ID, Username: u.Username, Role: u.Role, TenantID: u.TenantID}
}

func legacyPrincipal(l config.AuthConfig) *Principal {
	return &Principal{UserID: ActorLegacy, Username: l.Username, Role: config.RoleAdmin}
}

// PrincipalFor rebuilds the caller of a replicated mutation from its actor.
func (s *Service) PrincipalFor(actor string) (*Principal, error) {
	a := s.store.Access()
	switch actor {
	case ActorSystem:
		return &Principal{UserID: actor, Username: "system", Role: config.RoleAdmin, Method: Peer}, nil
	case ActorSetup, ActorRecovery:
		return &Principal{UserID: actor, Method: Peer}, nil
	case ActorLegacy:
		if a == nil {
			return &Principal{UserID: actor, Username: "admin", Role: config.RoleAdmin, Method: Peer}, nil
		}
		return nil, ErrSignedOut
	}
	if a == nil {
		return nil, ErrSignedOut
	}
	u := a.User(actor)
	if u == nil || u.Disabled {
		return nil, ErrSignedOut
	}
	p := principal(u)
	p.Method = Peer
	return p, nil
}

// ChangePassword verifies the caller's current password and sets new
// credentials, signing out the user's other sessions.
func (s *Service) ChangePassword(p *Principal, current, next, username string) (config.User, error) {
	if err := validatePassword("newPassword", next); err != nil {
		return config.User{}, err
	}
	if username != "" {
		if err := config.ValidateUsername(username); err != nil {
			return config.User{}, err
		}
	}
	hash, err := hashPassword(next)
	if err != nil {
		return config.User{}, err
	}
	var out config.User
	_, err = s.store.Update(func(c *config.Config) error {
		u, err := self(c, p)
		if err != nil {
			return err
		}
		if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(current)) != nil {
			return &config.FieldError{Field: "currentPassword", Msg: "current password is incorrect"}
		}
		if username != "" {
			u.Username = username
		}
		u.PasswordHash = hash
		u.SessionGen++
		out = *u
		return nil
	})
	return out, err
}

// self returns the caller's account inside a store update.
func self(c *config.Config, p *Principal) (*config.User, error) {
	if c.Access == nil {
		return nil, ErrMigrating
	}
	u := c.Access.User(p.UserID)
	if u == nil || u.Disabled {
		return nil, ErrSignedOut
	}
	return u, nil
}

// Sessions: value = base64url(userId|sessionGen|expiry) "." base64url(hmac).
// Before migration the payload is username|expiry under the node secret.

func sign(secret, payload string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// SetSession issues a session cookie for a user, or for the legacy admin.
func (s *Service) SetSession(w http.ResponseWriter, r *http.Request, userID string) error {
	local := s.store.Local().Auth
	exp := time.Now().Add(time.Duration(local.SessionTTL) * time.Second)
	expiry := strconv.FormatInt(exp.Unix(), 10)
	var payload, secret string
	if a := s.store.Access(); a != nil {
		u := a.User(userID)
		if u == nil || u.Disabled {
			return ErrSignedOut
		}
		payload, secret = u.ID+"|"+strconv.FormatInt(u.SessionGen, 10)+"|"+expiry, a.SessionSecret
	} else {
		if userID != ActorLegacy || local.SessionSecret == "" {
			return ErrSignedOut
		}
		payload, secret = local.Username+"|"+expiry, local.SessionSecret
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + sign(secret, payload),
		Path:     "/",
		Expires:  exp,
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func ClearSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// Authenticate returns the caller, or nil.
func (s *Service) Authenticate(r *http.Request) *Principal {
	a := s.store.Access()
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return s.checkToken(a, strings.TrimSpace(h[len("Bearer "):]))
	}
	c, err := r.Cookie(CookieName)
	if err != nil {
		return nil
	}
	enc, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return nil
	}
	payload := string(raw)
	parts := strings.Split(payload, "|")
	exp, err := strconv.ParseInt(parts[len(parts)-1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return nil
	}
	var p *Principal
	switch {
	case a == nil:
		l := s.store.Local().Auth
		if len(parts) != 2 || l.SessionSecret == "" || !hmac.Equal([]byte(sig), []byte(sign(l.SessionSecret, payload))) || parts[0] != l.Username || l.PasswordHash == "" {
			return nil
		}
		p = legacyPrincipal(l)
	case !hmac.Equal([]byte(sig), []byte(sign(a.SessionSecret, payload))):
		return nil
	case len(parts) == 3:
		u := a.User(parts[0])
		gen, err := strconv.ParseInt(parts[1], 10, 64)
		// A newer generation may arrive before this node has applied the
		// password change that issued it, so only older ones are rejected.
		if u == nil || u.Disabled || err != nil || gen < u.SessionGen {
			return nil
		}
		p = principal(u)
	case len(parts) == 2:
		// A session of the migrated administrator, signed with the secret
		// the shared accounts inherited.
		u := a.UserByName(parts[0])
		if u == nil || u.Disabled || u.Username != parts[0] || u.SessionGen != 0 || !u.Admin() {
			return nil
		}
		p = principal(u)
	default:
		return nil
	}
	p.Method = Cookie
	return p
}

// API tokens.

type TokenView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Prefix    string    `json:"prefix"`
	CreatedAt time.Time `json:"createdAt"`
}

func view(t config.APIToken) TokenView { return TokenView{t.ID, t.Name, t.Prefix, t.CreatedAt} }

// Tokens lists the caller's own tokens.
func (s *Service) Tokens(p *Principal) []TokenView {
	var ts []config.APIToken
	if a := s.store.Access(); a != nil {
		ts = a.Tokens
	} else if p.UserID == ActorLegacy {
		ts = s.store.Local().Auth.Tokens
	}
	out := []TokenView{}
	for _, t := range ts {
		if t.UserID == p.UserID || p.UserID == ActorLegacy {
			out = append(out, view(t))
		}
	}
	return out
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// CreateToken returns the plain token, which is not stored.
func (s *Service) CreateToken(p *Principal, name string) (string, TokenView, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 64 {
		return "", TokenView{}, &config.FieldError{Field: "name", Msg: "name must be 1-64 characters"}
	}
	plain := tokenPrefix + randomString(24)
	t := config.APIToken{
		ID: "tok_" + randomString(6), Name: name, Prefix: plain[:len(tokenPrefix)+6],
		Hash: hashToken(plain), CreatedAt: now(), UserID: p.UserID,
	}
	_, err := s.store.Update(func(c *config.Config) error {
		if _, err := self(c, p); err != nil {
			return err
		}
		if n := countTokens(c.Access, p.UserID); n >= maxTokensPerUser {
			return &config.FieldError{Field: "name", Msg: fmt.Sprintf("at most %d tokens per user", maxTokensPerUser)}
		}
		c.Access.Tokens = append(c.Access.Tokens, t)
		return nil
	})
	return plain, view(t), err
}

const maxTokensPerUser = 50

func countTokens(a *config.Access, user string) (n int) {
	for _, t := range a.Tokens {
		if t.UserID == user {
			n++
		}
	}
	return n
}

// DeleteToken revokes one of the caller's tokens.
func (s *Service) DeleteToken(p *Principal, id string) error {
	_, err := s.store.Update(func(c *config.Config) error {
		if _, err := self(c, p); err != nil {
			return err
		}
		i := slices.IndexFunc(c.Access.Tokens, func(t config.APIToken) bool { return t.ID == id && t.UserID == p.UserID })
		if i < 0 {
			return ErrNotFound
		}
		c.Access.Tokens = slices.Delete(c.Access.Tokens, i, i+1)
		return nil
	})
	return err
}

func (s *Service) checkToken(a *config.Access, plain string) *Principal {
	if !strings.HasPrefix(plain, tokenPrefix) {
		return nil
	}
	h := []byte(hashToken(plain))
	if a == nil {
		l := s.store.Local().Auth
		if l.Username == "" || l.PasswordHash == "" {
			return nil
		}
		for _, t := range l.Tokens {
			if subtle.ConstantTimeCompare(h, []byte(t.Hash)) == 1 {
				p := legacyPrincipal(l)
				p.Method = Bearer
				return p
			}
		}
		return nil
	}
	for _, t := range a.Tokens {
		if subtle.ConstantTimeCompare(h, []byte(t.Hash)) == 1 {
			if u := a.User(t.UserID); u != nil && !u.Disabled {
				p := principal(u)
				p.Method = Bearer
				return p
			}
		}
	}
	return nil
}

func now() time.Time { return time.Now().UTC().Truncate(time.Second) }

func randomString(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)[:n]
}

// newID returns a short random identifier valid as a user or tenant id.
func newID(prefix string) string {
	b := make([]byte, 5)
	rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// ClientIP returns the peer IP. X-Forwarded-For / X-Real-IP are honored only
// from a loopback peer (a reverse proxy on the same host) so remote clients
// cannot spoof their address to dodge throttling.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			return strings.TrimSpace(first)
		}
		if xr := r.Header.Get("X-Real-IP"); xr != "" {
			return strings.TrimSpace(xr)
		}
	}
	return host
}

// throttle blocks an IP after repeated login failures, doubling the lockout.
type throttle struct {
	mu sync.Mutex
	m  map[string]*attempts
}

type attempts struct {
	fails int
	last  time.Time
	until time.Time
}

const (
	freeAttempts = 5
	baseLockout  = time.Minute
	maxLockout   = 15 * time.Minute
	failWindow   = 15 * time.Minute
)

func newThrottle() *throttle { return &throttle{m: make(map[string]*attempts)} }

func (t *throttle) blocked(ip string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	if a := t.m[ip]; a != nil {
		return time.Until(a.until)
	}
	return 0
}

func (t *throttle) fail(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	for k, a := range t.m { // opportunistic cleanup
		if now.Sub(a.last) > failWindow && now.After(a.until) {
			delete(t.m, k)
		}
	}
	a := t.m[ip]
	if a == nil {
		a = &attempts{}
		t.m[ip] = a
	}
	a.fails++
	a.last = now
	if over := a.fails - freeAttempts; over >= 0 {
		a.until = now.Add(min(baseLockout<<min(over, 10), maxLockout))
	}
}

func (t *throttle) reset(ip string) {
	t.mu.Lock()
	delete(t.m, ip)
	t.mu.Unlock()
}
