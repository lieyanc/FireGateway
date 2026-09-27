package api

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/lieyanc/FireGateway/internal/config"
	"github.com/lieyanc/FireGateway/internal/gateway"
	"github.com/lieyanc/FireGateway/internal/importer"
	"github.com/lieyanc/FireGateway/internal/metrics"
	"github.com/lieyanc/FireGateway/internal/proxy"
)

const ruleBodyLimit = 64 << 10

type runtimeView struct {
	metrics.Counters
	State     string          `json:"state"`
	Error     string          `json:"error,omitempty"`
	Listeners int             `json:"listeners"`
	Failures  []proxy.Failure `json:"failures,omitempty"`
	StartedAt *time.Time      `json:"startedAt,omitempty"`
}

// ruleView shadows the embedded id so the API always returns ids as strings,
// while the config file keeps numeric ids numeric.
type ruleView struct {
	ID string `json:"id"`
	config.Rule
	Runtime runtimeView `json:"runtime"`
}

func (s *Server) view(r config.Rule) ruleView {
	rt := s.Manager.Runtime(&r)
	up, down := s.Sampler.Rates(r.ID)
	v := ruleView{ID: string(r.ID), Rule: r, Runtime: runtimeView{
		Counters:  metrics.CountersOf(rt.Snapshot, up, down),
		State:     rt.State,
		Error:     rt.Error,
		Listeners: rt.Listeners,
		Failures:  rt.Failures,
	}}
	if !rt.StartedAt.IsZero() {
		v.Runtime.StartedAt = &rt.StartedAt
	}
	return v
}

func (s *Server) listRules(w http.ResponseWriter, _ *http.Request) {
	rules := s.Manager.List()
	items := make([]ruleView, len(rules))
	for i, r := range rules {
		items[i] = s.view(r)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getRule(w http.ResponseWriter, r *http.Request) {
	rule, ok := s.Manager.Get(config.RuleID(r.PathValue("id")))
	if !ok {
		failErr(w, gateway.ErrNotFound)
		return
	}
	writeJSON(w, http.StatusOK, s.view(rule))
}

func (s *Server) createRule(w http.ResponseWriter, r *http.Request) {
	var rule config.Rule
	if !decode(w, r, &rule, ruleBodyLimit) {
		return
	}
	created, err := s.Manager.Create(rule)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.view(created))
}

func (s *Server) updateRule(w http.ResponseWriter, r *http.Request) {
	var rule config.Rule
	if !decode(w, r, &rule, ruleBodyLimit) {
		return
	}
	updated, err := s.Manager.Update(config.RuleID(r.PathValue("id")), rule)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.view(updated))
}

func (s *Server) deleteRule(w http.ResponseWriter, r *http.Request) {
	if err := s.Manager.Delete(config.RuleID(r.PathValue("id"))); err != nil {
		failErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) enableRule(w http.ResponseWriter, r *http.Request)  { s.setActive(w, r, true) }
func (s *Server) disableRule(w http.ResponseWriter, r *http.Request) { s.setActive(w, r, false) }

func (s *Server) setActive(w http.ResponseWriter, r *http.Request, active bool) {
	rule, err := s.Manager.SetActive(config.RuleID(r.PathValue("id")), active)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.view(rule))
}

func (s *Server) restartRule(w http.ResponseWriter, r *http.Request) {
	rule, err := s.Manager.Restart(config.RuleID(r.PathValue("id")))
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.view(rule))
}

func (s *Server) batchRules(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Action string          `json:"action"`
		IDs    []config.RuleID `json:"ids"`
	}
	if !decode(w, r, &b, 1<<20) {
		return
	}
	res, err := s.Manager.Batch(b.Action, b.IDs)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) exportRules(w http.ResponseWriter, _ *http.Request) {
	name := "firegateway-rules-" + time.Now().Format("20060102-150405") + ".json"
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	writeJSON(w, http.StatusOK, map[string]any{"forward": s.Manager.List()})
}

func (s *Server) importRules(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Forward []config.Rule `json:"forward"`
		Mode    string        `json:"mode"`
	}
	if !decode(w, r, &b, 8<<20) {
		return
	}
	if b.Mode != "merge" && b.Mode != "replace" {
		failErr(w, &config.FieldError{Field: "mode", Msg: "mode must be merge or replace"})
		return
	}
	if b.Forward == nil {
		failErr(w, &config.FieldError{Field: "forward", Msg: "forward array is required"})
		return
	}
	res, err := s.Manager.Import(b.Forward, b.Mode == "replace")
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// rinetdPaths are where the one-click import looks for this host's rinetd config.
var rinetdPaths = []string{"/etc/rinetd.conf", "/usr/local/etc/rinetd.conf", "/opt/homebrew/etc/rinetd.conf"}

// importedRule returns ids as strings like ruleView; empty ids are assigned on import.
type importedRule struct {
	ID string `json:"id"`
	config.Rule
}

type importPreview struct {
	Path     string             `json:"path,omitempty"`
	Format   string             `json:"format"`
	Rules    []importedRule     `json:"rules"`
	Warnings []importer.Warning `json:"warnings"`
}

func previewOf(res *importer.Result, path string) importPreview {
	p := importPreview{Path: path, Format: res.Format, Rules: make([]importedRule, len(res.Rules)), Warnings: res.Warnings}
	for i, r := range res.Rules {
		p.Rules[i] = importedRule{ID: string(r.ID), Rule: r}
	}
	return p
}

// parseImport converts rinetd or FireProxy text into rules without applying
// them; the client reviews the result and submits it to importRules.
func (s *Server) parseImport(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Format string `json:"format"`
		Text   string `json:"text"`
	}
	if !decode(w, r, &b, 4<<20) {
		return
	}
	res, err := importer.Parse(b.Format, b.Text)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, previewOf(res, ""))
}

// localRinetd reads and converts the rinetd config on this host.
func (s *Server) localRinetd(w http.ResponseWriter, _ *http.Request) {
	for _, path := range rinetdPaths {
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			fail(w, http.StatusInternalServerError, "read_failed", err.Error())
			return
		}
		res, err := importer.Parse(importer.FormatRinetd, string(data))
		if err != nil {
			failErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, previewOf(res, path))
		return
	}
	fail(w, http.StatusNotFound, "not_found", "no rinetd config found (looked for "+strings.Join(rinetdPaths, ", ")+")")
}

func (s *Server) reloadConfig(w http.ResponseWriter, _ *http.Request) {
	res, cfg, err := s.Manager.Reload()
	if err != nil {
		failErr(w, err)
		return
	}
	applyLogLevel(cfg.Logging.Level)
	writeJSON(w, http.StatusOK, res)
}

// Connections.

type connView struct {
	ID         string    `json:"id"`
	RuleID     string    `json:"ruleId"`
	RuleName   string    `json:"ruleName"`
	Type       string    `json:"type"`
	Listen     string    `json:"listen"`
	Target     string    `json:"target"`
	Client     string    `json:"client"`
	StartedAt  time.Time `json:"startedAt"`
	LastActive time.Time `json:"lastActive"`
	BytesUp    int64     `json:"bytesUp"`
	BytesDown  int64     `json:"bytesDown"`
}

func (s *Server) listConns(w http.ResponseWriter, r *http.Request) {
	conns := s.Manager.Connections(config.RuleID(r.URL.Query().Get("rule")))
	names := make(map[config.RuleID]string)
	for _, rule := range s.Manager.List() {
		names[rule.ID] = rule.Name
	}
	items := make([]connView, len(conns))
	for i, c := range conns {
		items[i] = connView{
			ID: strconv.FormatUint(c.ID, 10), RuleID: string(c.RuleID), RuleName: names[c.RuleID],
			Type: c.Type, Listen: c.Listen, Target: c.Target, Client: c.Client,
			StartedAt: c.StartedAt, LastActive: c.LastActive,
			BytesUp: c.BytesUp, BytesDown: c.BytesDown,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) closeConn(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil || !s.Manager.CloseConn(id) {
		fail(w, http.StatusNotFound, "not_found", "connection not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) closeRuleConns(w http.ResponseWriter, r *http.Request) {
	rule := config.RuleID(r.URL.Query().Get("rule"))
	if rule == "" {
		failErr(w, &config.FieldError{Field: "rule", Msg: "rule query parameter is required"})
		return
	}
	n, err := s.Manager.CloseRuleConns(rule)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"closed": n})
}

// Metrics.

func (s *Server) metricsRealtime(w http.ResponseWriter, r *http.Request) {
	pts := s.Sampler.Realtime(config.RuleID(r.URL.Query().Get("rule")))
	writeJSON(w, http.StatusOK, map[string]any{"step": 1, "points": pts})
}

func (s *Server) metricsHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	step, pts, ok := s.Sampler.History(config.RuleID(q.Get("rule")), q.Get("range"))
	if !ok {
		failErr(w, &config.FieldError{Field: "range", Msg: "range must be 1h, 24h, 7d or 30d"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"step": step, "points": pts})
}

func (s *Server) metricsTop(w http.ResponseWriter, r *http.Request) {
	items, ok := s.Sampler.Top(r.URL.Query().Get("range"))
	if !ok {
		failErr(w, &config.FieldError{Field: "range", Msg: "range must be 1h, 24h, 7d or 30d"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
