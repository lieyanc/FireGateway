package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/lieyanc/FireGateway/internal/auth"
	"github.com/lieyanc/FireGateway/internal/config"
	"github.com/lieyanc/FireGateway/internal/gateway"
	"github.com/lieyanc/FireGateway/internal/ha"
)

// ruleMutation reports whether a request changes the shared rules; those
// writes require If-Match.
func ruleMutation(method, p string) bool {
	if method == http.MethodGet || method == http.MethodHead {
		return false
	}
	if p == "/api/rules" || p == "/api/rules/import" || p == "/api/rules/batch" {
		return true
	}
	if strings.HasPrefix(p, "/api/rules/") && !strings.HasSuffix(p, "/restart") && !strings.HasPrefix(p, "/api/rules/import/") {
		return method == http.MethodPut || method == http.MethodDelete || strings.HasSuffix(p, "/enable") || strings.HasSuffix(p, "/disable")
	}
	return false
}

// accountMutation reports whether a request changes the shared accounts.
func accountMutation(method, p string) bool {
	if method == http.MethodGet || method == http.MethodHead {
		return false
	}
	switch {
	case p == "/api/auth/setup", p == "/api/auth/password", p == "/api/auth/tokens", strings.HasPrefix(p, "/api/auth/tokens/"):
		return true
	}
	for _, prefix := range []string{"/api/users", "/api/tenants"} {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

const migratePath = "/api/auth/migrate"

func (s *Server) mutationRoutes() http.Handler {
	m := http.NewServeMux()
	s.sharedRoutes(m)
	m.HandleFunc("POST "+migratePath, s.migrate)
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fail(w, 400, "bad_request", "unsupported shared mutation")
	})
	return m
}

// migrate moves the writer's node-local administrator into the shared
// accounts; only the internal migration loop may call it.
func (s *Server) migrate(w http.ResponseWriter, r *http.Request) {
	if p := auth.FromContext(r.Context()); p == nil || p.UserID != auth.ActorSystem {
		fail(w, http.StatusForbidden, "forbidden", "internal endpoint")
		return
	}
	migrated, err := s.Auth.Migrate()
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"migrated": migrated})
}

type replyBuffer struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (b *replyBuffer) Header() http.Header { return b.header }
func (b *replyBuffer) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}
func (b *replyBuffer) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = 200
	}
	return b.body.Write(p)
}

// Execute runs a replicated mutation on the writer as the actor that sent it.
func (s *Server) Execute(req ha.Mutation) ha.Reply {
	p, err := s.Auth.PrincipalFor(req.Actor)
	if err != nil {
		body, _ := json.Marshal(apiError{Error: "unauthorized", Message: err.Error()})
		return ha.Reply{Status: http.StatusUnauthorized, Body: body}
	}
	r, err := http.NewRequest(req.Method, req.Path, bytes.NewReader(req.Body))
	if err != nil {
		return ha.Reply{Status: 400, Body: json.RawMessage(`{"error":"bad_request","message":"invalid mutation path"}`)}
	}
	r = r.WithContext(auth.NewContext(r.Context(), p))
	r.Header.Set("Content-Type", "application/json")
	w := &replyBuffer{header: http.Header{}}
	s.mutationRoutes().ServeHTTP(w, r)
	if w.status == 0 {
		w.status = 200
	}
	body := w.body.Bytes()
	if len(body) > 0 && !json.Valid(body) {
		body = []byte(`{"error":"bad_request","message":"unsupported mutation"}`)
	}
	return ha.Reply{Status: w.status, Body: body}
}

// Version is the rule version a replicated rule write must match: the
// caller's view of the rules, so changes elsewhere do not invalidate it.
func (s *Server) Version(req ha.Mutation, committed config.RuleSet) string {
	if !ruleMutation(req.Method, req.Path) {
		return ""
	}
	return ruleVersion(actorScope(committed.Access, req.Actor), committed)
}

func actorScope(a *config.Access, actor string) gateway.Scope {
	if a == nil || actor == auth.ActorSystem {
		return gateway.Admin
	}
	if u := a.User(actor); u != nil {
		if u.Admin() {
			return gateway.Admin
		}
		return gateway.Scope{Tenant: u.TenantID}
	}
	return gateway.Scope{Tenant: "@"}
}

// ruleVersion digests the rules a scope can see; a tenant's version also
// covers its own limits.
func ruleVersion(scope gateway.Scope, set config.RuleSet) string {
	view := struct {
		Rules  []config.Rule  `json:"rules"`
		Tenant *config.Tenant `json:"tenant,omitempty"`
	}{Rules: []config.Rule{}}
	for _, r := range set.Rules {
		if scope.Owns(&r) {
			view.Rules = append(view.Rules, r)
		}
	}
	if scope.Tenant != "" && set.Access != nil {
		view.Tenant = set.Access.Tenant(scope.Tenant)
	}
	b, _ := json.Marshal(view)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

func writeMutation(w http.ResponseWriter, reply ha.Reply) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if reply.Version != "" {
		w.Header().Set("ETag", `"`+reply.Version+`"`)
	}
	if reply.Durability != "" {
		w.Header().Set("X-Rule-Durability", reply.Durability)
	}
	w.WriteHeader(reply.Status)
	_, _ = w.Write(reply.Body)
}

func (s *Server) peerMutation(ctx context.Context, req ha.Mutation) ha.Reply {
	return s.Cluster.Mutate(ctx, req, s)
}

// withClusterWrites sends writes to replicated state through the cluster
// writer, acting as the caller authenticated on this node.
func (s *Server) withClusterWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		rules := ruleMutation(r.Method, path)
		// Before pairing there are no shared accounts to write; the first
		// administrator is created locally and migrated after pairing.
		if s.Cluster == nil || !(rules || accountMutation(r.Method, path)) ||
			path == "/api/auth/setup" && s.Store.Access() == nil && !s.Cluster.Status().Paired {
			next.ServeHTTP(w, r)
			return
		}
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			fail(w, 413, "bad_request", "mutation exceeds 1 MiB")
			return
		}
		if len(b) > 0 && !json.Valid(b) {
			fail(w, 400, "bad_request", "invalid JSON")
			return
		}
		var actor string
		if path == "/api/auth/setup" {
			// The setup token only exists on the node that printed it, so it is
			// checked here and never leaves this node.
			var c credentials
			_ = json.Unmarshal(b, &c)
			if actor, err = s.Auth.CheckSetupToken(c.SetupToken); err != nil {
				failErr(w, err)
				return
			}
			c.SetupToken = ""
			b, _ = json.Marshal(c)
		} else {
			actor = auth.FromContext(r.Context()).UserID
		}
		id := r.Header.Get("Idempotency-Key")
		if id == "" && !rules {
			id = newKey()
		}
		req := ha.Mutation{ID: id, Expected: strings.Trim(r.Header.Get("If-Match"), `"`), Method: r.Method, Path: r.URL.EscapedPath(), Body: b, Actor: actor}
		reply := s.Cluster.Mutate(r.Context(), req, s)
		if reply.Status == http.StatusOK && (path == "/api/auth/setup" || path == "/api/auth/password") {
			var u struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(reply.Body, &u) == nil && u.ID != "" {
				_ = s.Auth.SetSession(w, r, u.ID)
			}
			if path == "/api/auth/setup" {
				s.Auth.EndRecovery()
			}
		}
		writeMutation(w, reply)
	})
}

func newKey() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// MigrateAccounts moves the node-local administrator of older releases into
// the shared accounts through the cluster writer, then drops the local copy
// once the shared accounts exist.
func (s *Server) MigrateAccounts(ctx context.Context) {
	wait := 5 * time.Second
	for {
		if s.Store.Access() != nil {
			if err := s.Auth.DropLegacy(); err != nil {
				slog.Warn("failed to remove the migrated admin account from the node config", "err", err)
			}
			return
		}
		if s.Auth.LegacyPending() {
			req := ha.Mutation{ID: newKey(), Method: http.MethodPost, Path: migratePath, Actor: auth.ActorSystem}
			reply := s.Cluster.Mutate(ctx, req, s)
			var res struct {
				Migrated bool `json:"migrated"`
			}
			switch {
			case reply.Status != http.StatusOK:
				slog.Debug("account migration deferred", "status", reply.Status, "reply", string(reply.Body))
			case json.Unmarshal(reply.Body, &res) == nil && res.Migrated:
				slog.Info("admin account migrated to shared cluster accounts")
				continue
			default:
				// The writer has no administrator to migrate; wait for setup
				// or a writer change instead of retrying at full speed.
				wait = 5 * time.Minute
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}
