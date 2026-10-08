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
	"github.com/lieyanc/FireGateway/internal/ha"
	"github.com/lieyanc/FireGateway/internal/metrics"
	"github.com/lieyanc/FireGateway/internal/quota"
	"github.com/lieyanc/FireGateway/internal/updater"
)

type Deps struct {
	Cluster *ha.Controller
	Store   *config.Store
	Manager *gateway.Manager
	Sampler *metrics.Sampler
	Broker  *events.Broker
	Auth    *auth.Service
	Quota   *quota.Tracker
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
	if d.Cluster != nil {
		// Edits forwarded by the peer over the node link run here when this
		// node is the configuration writer.
		d.Cluster.SetMutator(s.peerMutation)
	}
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
	// h serves everyone who passed withAuth; m serves administrators and
	// tenant members, scoped to what they own; a serves administrators only.
	h := func(pattern string, fn http.HandlerFunc) { mux.HandleFunc(pattern, fn) }
	m := func(pattern string, fn http.HandlerFunc) { mux.HandleFunc(pattern, member(fn)) }
	a := func(pattern string, fn http.HandlerFunc) { mux.HandleFunc(pattern, admin(fn)) }

	// Public.
	h("GET /health", s.health)
	h("GET /ready", s.ready)
	h("GET /api/version", s.version)
	h("GET /api/auth/state", s.authState)
	h("POST /api/auth/login", s.login)

	// Authenticated (enforced by withAuth for everything else under /api).
	h("POST /api/auth/logout", s.logout)
	h("GET /api/auth/tokens", s.listTokens)
	m("GET /api/tenant", s.ownTenant)

	m("GET /api/overview", s.overview)
	a("GET /api/cluster", s.clusterStatus)
	a("GET /api/cluster/config", s.getClusterConnection)
	a("PUT /api/cluster/config", s.putClusterConnection)
	a("POST /api/cluster/test-peer", s.testPeerConnection)
	a("GET /api/cluster/snapshot", s.clusterSnapshot)
	a("POST /api/cluster/bootstrap", s.clusterBootstrap)
	a("POST /api/cluster/{action}", s.clusterAction)
	a("GET /api/cluster/transactions/{id}", s.clusterTransaction)
	a("GET /api/node", s.getNode)
	a("PUT /api/node", s.putNode)
	a("POST /api/system/restart", s.restart)

	m("GET /api/rules", s.listRules)
	m("GET /api/rules/export", s.exportRules)
	m("POST /api/rules/import/parse", s.parseImport)
	a("GET /api/rules/import/rinetd", s.localRinetd)
	m("GET /api/rules/{id}", s.getRule)
	m("POST /api/rules/{id}/restart", s.restartRule)
	a("POST /api/config/reload", s.reloadConfig)
	s.sharedRoutes(mux)

	m("GET /api/connections", s.listConns)
	m("DELETE /api/connections", s.closeRuleConns)
	m("DELETE /api/connections/{id}", s.closeConn)

	m("GET /api/metrics/realtime", s.metricsRealtime)
	m("GET /api/metrics/history", s.metricsHistory)
	m("GET /api/metrics/top", s.metricsTop)

	a("GET /api/dns", s.listDNS)
	a("POST /api/dns/refresh", s.refreshDNS)

	m("GET /api/events", s.events)
	m("GET /api/logs", s.logs)
	m("GET /api/logs/stream", s.logStream)
	m("GET /api/log-level", s.getLogLevel)
	a("PUT /api/log-level", s.setLogLevel)

	a("GET /api/settings", s.getSettings)
	a("PUT /api/settings", s.putSettings)

	a("GET /api/update/status", s.updateStatus)
	a("POST /api/update/check", s.updateCheck)
	a("POST /api/update/apply", s.updateApply)
	a("POST /api/update/dismiss", s.updateDismiss)

	a("GET /api/users", s.listUsers)
	a("GET /api/users/{id}", s.getUser)
	a("GET /api/tenants", s.listTenants)
	a("GET /api/tenants/{id}", s.getTenant)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		fail(w, http.StatusNotFound, "not_found", "endpoint "+r.Method+" "+r.URL.Path+" not found")
	})
	mux.Handle("/", s.static)

	return s.withRecover(s.withHeaders(s.withCORS(s.withAuth(s.withClusterWrites(mux)))))
}

// sharedRoutes registers the writes to replicated state. In a cluster they
// run on the writer through Execute, so both muxes share this table.
func (s *Server) sharedRoutes(mux *http.ServeMux) {
	h := func(pattern string, fn http.HandlerFunc) { mux.HandleFunc(pattern, fn) }
	m := func(pattern string, fn http.HandlerFunc) { mux.HandleFunc(pattern, member(fn)) }
	a := func(pattern string, fn http.HandlerFunc) { mux.HandleFunc(pattern, admin(fn)) }

	h("POST /api/auth/setup", s.setup)
	h("POST /api/auth/password", s.changePassword)
	h("POST /api/auth/tokens", s.createToken)
	h("DELETE /api/auth/tokens/{id}", s.deleteToken)

	m("POST /api/rules", s.createRule)
	m("PUT /api/rules/{id}", s.updateRule)
	m("DELETE /api/rules/{id}", s.deleteRule)
	m("POST /api/rules/{id}/enable", s.enableRule)
	m("POST /api/rules/{id}/disable", s.disableRule)
	m("POST /api/rules/import", s.importRules)
	m("POST /api/rules/batch", s.batchRules)

	a("POST /api/users", s.createUser)
	a("PUT /api/users/{id}", s.updateUser)
	a("DELETE /api/users/{id}", s.deleteUser)
	a("POST /api/tenants", s.createTenant)
	a("PUT /api/tenants/{id}", s.updateTenant)
	a("DELETE /api/tenants/{id}", s.deleteTenant)
	a("POST /api/tenants/{id}/usage/reset", s.resetTenantUsage)
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
		p := s.Auth.Authenticate(r)
		// Browsers attach cookies to cross-site requests; reject those for
		// state-changing calls. Bearer tokens are not ambient, so exempt.
		if (p == nil || p.Method != auth.Bearer) && unsafeMethod(r.Method) && crossSite(r) {
			fail(w, http.StatusForbidden, "forbidden", "cross-site request rejected")
			return
		}
		if p == nil && !publicPaths[r.URL.Path] {
			fail(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		if p != nil {
			r = r.WithContext(auth.NewContext(r.Context(), p))
		}
		next.ServeHTTP(w, r)
	})
}

// admin restricts a handler to administrators.
func admin(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !auth.FromContext(r.Context()).Admin() {
			fail(w, http.StatusForbidden, "forbidden", "administrator access required")
			return
		}
		fn(w, r)
	}
}

// member admits administrators and tenant members; handlers limit members
// to their tenant through scopeOf.
func member(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if p := auth.FromContext(r.Context()); p == nil || (!p.Admin() && p.TenantID == "") {
			fail(w, http.StatusForbidden, "forbidden", "a tenant or administrator account is required")
			return
		}
		fn(w, r)
	}
}

// scopeOf returns the rules the caller may see and manage.
func scopeOf(r *http.Request) gateway.Scope {
	p := auth.FromContext(r.Context())
	if p.Admin() {
		return gateway.Admin
	}
	if p == nil || p.TenantID == "" {
		return gateway.Scope{Tenant: "@"} // matches no rule
	}
	return gateway.Scope{Tenant: p.TenantID}
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
			w.Header().Set("Access-Control-Expose-Headers", "ETag, X-Rule-Durability")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, If-Match, Idempotency-Key")
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
		fe      *config.FieldError
		ce      *gateway.ConflictError
		qe      *gateway.QuotaError
		rl      *auth.RateLimitError
		pending *ha.PendingError
	)
	switch {
	case errors.As(err, &pending):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "sync_pending", "message": pending.Error(), "updateId": pending.ID})
	case errors.As(err, &fe):
		writeJSON(w, http.StatusBadRequest, apiError{"validation", fe.Msg, fe.Field})
	case errors.Is(err, config.ErrRevisionConflict):
		fail(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, config.ErrClusterUnavailable):
		fail(w, http.StatusServiceUnavailable, "unavailable", err.Error())
	case errors.As(err, &ce):
		fail(w, http.StatusConflict, "conflict", ce.Msg)
	case errors.As(err, &qe):
		fail(w, http.StatusConflict, "quota_exceeded", qe.Msg)
	case errors.Is(err, auth.ErrMigrating):
		w.Header().Set("Retry-After", "5")
		fail(w, http.StatusServiceUnavailable, "unavailable", err.Error())
	case errors.Is(err, auth.ErrSignedOut):
		fail(w, http.StatusUnauthorized, "unauthorized", err.Error())
	case errors.Is(err, auth.ErrUserNotFound), errors.Is(err, auth.ErrNoTenant):
		fail(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, auth.ErrLastAdmin), errors.Is(err, auth.ErrSelf), errors.Is(err, auth.ErrTenantInUse):
		fail(w, http.StatusConflict, "conflict", err.Error())
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
