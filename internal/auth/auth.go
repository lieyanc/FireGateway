// Package auth implements the single-admin login: bcrypt password, HMAC-signed
// session cookies, hashed API tokens and brute-force throttling.
package auth

import (
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

var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrNotInitialized     = errors.New("admin account not set up")
	ErrInitialized        = errors.New("admin account already set up")
	ErrBadSetupToken      = errors.New("invalid setup token")
	ErrNotFound           = errors.New("token not found")
)

// RateLimitError means the client must wait before trying again.
type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("too many failed attempts, retry in %s", e.RetryAfter.Round(time.Second))
}

type Service struct {
	store      *config.Store
	setupToken string
	throttle   *throttle
}

func New(store *config.Store) *Service {
	s := &Service{store: store, throttle: newThrottle()}
	if !s.Initialized() {
		s.setupToken = randomString(12)
	} else if store.Get().Auth.SessionSecret == "" {
		// Hand-edited credentials: sessions need a signing secret.
		store.Update(func(c *config.Config) error { c.Auth.SessionSecret = randomString(32); return nil })
	}
	return s
}

func (s *Service) Initialized() bool {
	a := s.store.Get().Auth
	return a.Username != "" && a.PasswordHash != ""
}

// SetupToken is the one-time secret required to create the admin account;
// empty once initialized.
func (s *Service) SetupToken() string {
	if s.Initialized() {
		return ""
	}
	return s.setupToken
}

func validateCredentials(user, pass string) error {
	if n := utf8.RuneCountInString(user); n < 1 || n > 64 || strings.TrimSpace(user) != user {
		return &config.FieldError{Field: "username", Msg: "username must be 1-64 characters without surrounding spaces"}
	}
	if n := utf8.RuneCountInString(pass); n < 8 || n > 128 {
		return &config.FieldError{Field: "password", Msg: "password must be 8-128 characters"}
	}
	return nil
}

func (s *Service) Setup(token, user, pass string) error {
	if s.Initialized() {
		return ErrInitialized
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(token)), []byte(s.setupToken)) != 1 {
		return ErrBadSetupToken
	}
	if err := validateCredentials(user, pass); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.store.Update(func(c *config.Config) error {
		if c.Auth.Username != "" && c.Auth.PasswordHash != "" {
			return ErrInitialized
		}
		c.Auth.Username, c.Auth.PasswordHash = user, string(hash)
		c.Auth.SessionSecret = randomString(32)
		return nil
	})
	return err
}

// Login checks credentials, throttling per client IP.
func (s *Service) Login(user, pass, ip string) error {
	if !s.Initialized() {
		return ErrNotInitialized
	}
	if wait := s.throttle.blocked(ip); wait > 0 {
		return &RateLimitError{wait}
	}
	a := s.store.Get().Auth
	// Always run bcrypt so response time does not reveal the username.
	pwErr := bcrypt.CompareHashAndPassword([]byte(a.PasswordHash), []byte(pass))
	if pwErr != nil || subtle.ConstantTimeCompare([]byte(user), []byte(a.Username)) != 1 {
		s.throttle.fail(ip)
		return ErrInvalidCredentials
	}
	s.throttle.reset(ip)
	return nil
}

// ChangePassword verifies the current password and sets new credentials.
// Rotating the session secret signs out every other session.
func (s *Service) ChangePassword(current, next, user string) error {
	a := s.store.Get().Auth
	if bcrypt.CompareHashAndPassword([]byte(a.PasswordHash), []byte(current)) != nil {
		return &config.FieldError{Field: "currentPassword", Msg: "current password is incorrect"}
	}
	if user == "" {
		user = a.Username
	}
	if err := validateCredentials(user, next); err != nil {
		if fe, ok := err.(*config.FieldError); ok && fe.Field == "password" {
			fe.Field = "newPassword"
		}
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(next), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.store.Update(func(c *config.Config) error {
		c.Auth.Username, c.Auth.PasswordHash = user, string(hash)
		c.Auth.SessionSecret = randomString(32)
		return nil
	})
	return err
}

// Sessions: value = base64url(user|expiry) "." base64url(hmac).

func (s *Service) sign(payload string) string {
	m := hmac.New(sha256.New, []byte(s.store.Get().Auth.SessionSecret))
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// SetSession issues a session cookie for the admin.
func (s *Service) SetSession(w http.ResponseWriter, r *http.Request) {
	a := s.store.Get().Auth
	exp := time.Now().Add(time.Duration(a.SessionTTL) * time.Second)
	payload := a.Username + "|" + strconv.FormatInt(exp.Unix(), 10)
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + s.sign(payload),
		Path:     "/",
		Expires:  exp,
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
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

// Method tells how a request was authenticated.
type Method int

const (
	None Method = iota
	Cookie
	Bearer
)

// Authenticate returns the admin username and method, or None.
func (s *Service) Authenticate(r *http.Request) (string, Method) {
	a := s.store.Get().Auth
	if a.Username == "" || a.SessionSecret == "" {
		return "", None
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		if s.checkToken(strings.TrimSpace(h[len("Bearer "):])) {
			return a.Username, Bearer
		}
		return "", None
	}
	c, err := r.Cookie(CookieName)
	if err != nil {
		return "", None
	}
	enc, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		return "", None
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return "", None
	}
	payload := string(raw)
	if !hmac.Equal([]byte(sig), []byte(s.sign(payload))) {
		return "", None
	}
	user, expStr, _ := strings.Cut(payload, "|")
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().Unix() > exp || user != a.Username {
		return "", None
	}
	return user, Cookie
}

// API tokens.

type TokenView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Prefix    string    `json:"prefix"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *Service) Tokens() []TokenView {
	ts := s.store.Get().Auth.Tokens
	out := make([]TokenView, len(ts))
	for i, t := range ts {
		out[i] = TokenView{t.ID, t.Name, t.Prefix, t.CreatedAt}
	}
	return out
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// CreateToken returns the plain token, which is not stored.
func (s *Service) CreateToken(name string) (string, TokenView, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 64 {
		return "", TokenView{}, &config.FieldError{Field: "name", Msg: "name must be 1-64 characters"}
	}
	plain := tokenPrefix + randomString(24)
	t := config.APIToken{
		ID: "tok_" + randomString(6), Name: name, Prefix: plain[:len(tokenPrefix)+6],
		Hash: hashToken(plain), CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	_, err := s.store.Update(func(c *config.Config) error {
		c.Auth.Tokens = append(c.Auth.Tokens, t)
		return nil
	})
	return plain, TokenView{t.ID, t.Name, t.Prefix, t.CreatedAt}, err
}

func (s *Service) DeleteToken(id string) error {
	_, err := s.store.Update(func(c *config.Config) error {
		i := slices.IndexFunc(c.Auth.Tokens, func(t config.APIToken) bool { return t.ID == id })
		if i < 0 {
			return ErrNotFound
		}
		c.Auth.Tokens = slices.Delete(c.Auth.Tokens, i, i+1)
		return nil
	})
	return err
}

func (s *Service) checkToken(plain string) bool {
	if !strings.HasPrefix(plain, tokenPrefix) {
		return false
	}
	h := []byte(hashToken(plain))
	ok := false
	for _, t := range s.store.Get().Auth.Tokens {
		if subtle.ConstantTimeCompare(h, []byte(t.Hash)) == 1 {
			ok = true
		}
	}
	return ok
}

func randomString(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)[:n]
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
