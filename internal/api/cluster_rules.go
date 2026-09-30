package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/lieyanc/FireGateway/internal/ha"
)

func ruleMutation(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return false
	}
	p := r.URL.Path
	if p == "/api/rules" || p == "/api/rules/import" || p == "/api/rules/batch" {
		return true
	}
	if strings.HasPrefix(p, "/api/rules/") && !strings.HasSuffix(p, "/restart") && !strings.HasPrefix(p, "/api/rules/import/") {
		return r.Method == http.MethodPut || r.Method == http.MethodDelete || strings.HasSuffix(p, "/enable") || strings.HasSuffix(p, "/disable")
	}
	return false
}
func (s *Server) mutationRoutes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("POST /api/rules", s.createRule)
	m.HandleFunc("PUT /api/rules/{id}", s.updateRule)
	m.HandleFunc("DELETE /api/rules/{id}", s.deleteRule)
	m.HandleFunc("POST /api/rules/{id}/enable", s.enableRule)
	m.HandleFunc("POST /api/rules/{id}/disable", s.disableRule)
	m.HandleFunc("POST /api/rules/import", s.importRules)
	m.HandleFunc("POST /api/rules/batch", s.batchRules)
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fail(w, 400, "bad_request", "unsupported shared-rule mutation")
	})
	return m
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
func (s *Server) executeMutation(req ha.Mutation) ha.Reply {
	r, err := http.NewRequest(req.Method, req.Path, bytes.NewReader(req.Body))
	if err != nil {
		return ha.Reply{Status: 400, Body: json.RawMessage(`{"error":"bad_request","message":"invalid mutation path"}`)}
	}
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
	return s.Cluster.Mutate(ctx, req, s.executeMutation)
}
func (s *Server) withClusterRules(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Cluster == nil || !ruleMutation(r) {
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
		req := ha.Mutation{ID: r.Header.Get("Idempotency-Key"), Expected: strings.Trim(r.Header.Get("If-Match"), `"`), Method: r.Method, Path: r.URL.EscapedPath(), Body: b}
		writeMutation(w, s.Cluster.Mutate(r.Context(), req, s.executeMutation))
	})
}
