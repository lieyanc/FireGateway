// Package api serves the REST API, live event streams and the embedded web UI.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lieyanc/FireGateway/internal/auth"
	"github.com/lieyanc/FireGateway/internal/config"
	"github.com/lieyanc/FireGateway/internal/events"
	"github.com/lieyanc/FireGateway/internal/gateway"
	"github.com/lieyanc/FireGateway/internal/metrics"
	"github.com/lieyanc/FireGateway/internal/updater"
)

type Deps struct {
	Store   *config.Store
	Manager *gateway.Manager
	Sampler *metrics.Sampler
	Broker  *events.Broker
	Auth    *auth.Service
	Updater *updater.Updater
	Started time.Time
	// Restart re-execs the process; nil where unsupported.
	Restart func() error
	Web     fs.FS
}

type Server struct {
	Deps
	boot   *config.Config // config the process started with, for restart detection
	static *staticFS
	srv    *http.Server
}

// Start listens on the configured address and serves in the background.
func Start(d Deps) (*Server, error) {
	s := &Server{Deps: d, boot: d.Store.Get()}
	s.static = newStaticFS(d.Web)
	c := s.boot.API
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s.srv = &http.Server{
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug),
	}
	go s.srv.Serve(ln)
	slog.Info("web UI and API started", "addr", "http://"+addr)
	return s, nil
}

func (s *Server) Close() error { return s.srv.Close() }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	h := func(pattern string, fn http.HandlerFunc) { mux.HandleFunc(pattern, fn) }

	// Public.
	h("GET /health", s.health)
	h("GET /api/version", s.version)
	h("GET /api/auth/state", s.authState)
	h("POST /api/auth/setup", s.setup)
	h("POST /api/auth/login", s.login)

	// Authenticated (enforced by withAuth for everything else under /api).
	h("POST /api/auth/logout", s.logout)
	h("POST /api/auth/password", s.changePassword)
	h("GET /api/auth/tokens", s.listTokens)
	h("POST /api/auth/tokens", s.createToken)
	h("DELETE /api/auth/tokens/{id}", s.deleteToken)

	h("GET /api/overview", s.overview)
	h("POST /api/system/restart", s.restart)

	h("GET /api/rules", s.listRules)
	h("POST /api/rules", s.createRule)
	h("GET /api/rules/export", s.exportRules)
	h("POST /api/rules/import", s.importRules)
	h("POST /api/rules/import/parse", s.parseImport)
	h("GET /api/rules/import/rinetd", s.localRinetd)
	h("POST /api/rules/batch", s.batchRules)
	h("GET /api/rules/{id}", s.getRule)
	h("PUT /api/rules/{id}", s.updateRule)
	h("DELETE /api/rules/{id}", s.deleteRule)
	h("POST /api/rules/{id}/enable", s.enableRule)
	h("POST /api/rules/{id}/disable", s.disableRule)
	h("POST /api/rules/{id}/restart", s.restartRule)
	h("POST /api/config/reload", s.reloadConfig)

	h("GET /api/connections", s.listConns)
	h("DELETE /api/connections", s.closeRuleConns)
	h("DELETE /api/connections/{id}", s.closeConn)

	h("GET /api/metrics/realtime", s.metricsRealtime)
	h("GET /api/metrics/history", s.metricsHistory)
	h("GET /api/metrics/top", s.metricsTop)

	h("GET /api/events", s.events)
	h("GET /api/logs", s.logs)
	h("GET /api/logs/stream", s.logStream)
	h("GET /api/log-level", s.getLogLevel)
	h("PUT /api/log-level", s.setLogLevel)

	h("GET /api/settings", s.getSettings)
	h("PUT /api/settings", s.putSettings)

	h("GET /api/update/status", s.updateStatus)
	h("POST /api/update/check", s.updateCheck)
	h("POST /api/update/apply", s.updateApply)
	h("POST /api/update/dismiss", s.updateDismiss)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		fail(w, http.StatusNotFound, "not_found", "endpoint "+r.Method+" "+r.URL.Path+" not found")
	})
	mux.Handle("/", s.static)

	return s.withRecover(s.withHeaders(s.withCORS(s.withAuth(mux))))
}

var publicPaths = map[string]bool{
	"/api/version":    true,
	"/api/auth/state": true,
	"/api/auth/setup": true,
	"/api/auth/login": true,
}

func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		_, method := s.Auth.Authenticate(r)
		// Browsers attach cookies to cross-site requests; reject those for
		// state-changing calls. Bearer tokens are not ambient, so exempt.
		if method != auth.Bearer && unsafeMethod(r.Method) && crossSite(r) {
			fail(w, http.StatusForbidden, "forbidden", "cross-site request rejected")
			return
		}
		if method == auth.None && !publicPaths[r.URL.Path] {
			fail(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func unsafeMethod(m string) bool {
	return m != http.MethodGet && m != http.MethodHead && m != http.MethodOptions
}

func crossSite(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	return err != nil || !strings.EqualFold(u.Host, r.Host)
}

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.boot.API.EnableCors && strings.HasPrefix(r.URL.Path, "/api/") {
			// Credentials are not allowed cross-origin, so only API tokens work.
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				slog.Error("panic in HTTP handler", "path", r.URL.Path, "panic", v)
				fail(w, http.StatusInternalServerError, "internal", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// Response helpers.

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

type apiError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

func fail(w http.ResponseWriter, code int, errCode, msg string) {
	writeJSON(w, code, apiError{Error: errCode, Message: msg})
}

// failErr maps domain errors to HTTP responses.
func failErr(w http.ResponseWriter, err error) {
	var (
		fe *config.FieldError
		ce *gateway.ConflictError
		rl *auth.RateLimitError
	)
	switch {
	case errors.As(err, &fe):
		writeJSON(w, http.StatusBadRequest, apiError{"validation", fe.Msg, fe.Field})
	case errors.As(err, &ce):
		fail(w, http.StatusConflict, "conflict", ce.Msg)
	case errors.As(err, &rl):
		w.Header().Set("Retry-After", strconv.Itoa(int(rl.RetryAfter.Seconds())+1))
		fail(w, http.StatusTooManyRequests, "rate_limited", rl.Error())
	case errors.Is(err, gateway.ErrNotFound), errors.Is(err, auth.ErrNotFound):
		fail(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, auth.ErrInvalidCredentials):
		fail(w, http.StatusUnauthorized, "unauthorized", err.Error())
	case errors.Is(err, auth.ErrBadSetupToken):
		fail(w, http.StatusForbidden, "forbidden", err.Error())
	case errors.Is(err, auth.ErrInitialized):
		fail(w, http.StatusConflict, "already_initialized", err.Error())
	case errors.Is(err, auth.ErrNotInitialized):
		fail(w, http.StatusConflict, "not_initialized", err.Error())
	default:
		slog.Error("request failed", "err", err)
		fail(w, http.StatusInternalServerError, "internal", err.Error())
	}
}

// decode reads a JSON body of at most limit bytes into v.
func decode(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			fail(w, http.StatusBadRequest, "bad_request", "request body is required")
		} else {
			fail(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		}
		return false
	}
	return true
}
