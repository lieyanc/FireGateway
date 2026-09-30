package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/lieyanc/FireGateway/internal/config"
	"github.com/lieyanc/FireGateway/internal/gateway"
	"github.com/lieyanc/FireGateway/internal/logx"
	"github.com/lieyanc/FireGateway/internal/metrics"
	"github.com/lieyanc/FireGateway/internal/version"
)

// health keeps the legacy response shape for existing monitors.
func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	up := int64(time.Since(s.Started).Seconds())
	listeners := 0
	for _, rn := range s.Manager.Runners() {
		listeners += rn.Listeners()
	}
	status, code := "healthy", http.StatusOK
	if listeners == 0 || !s.Manager.Serving() {
		status, code = "unhealthy", http.StatusServiceUnavailable
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	writeJSON(w, code, map[string]any{
		"status":    status,
		"timestamp": time.Now().UTC(),
		"uptime":    map[string]any{"seconds": up, "human": FormatUptime(up)},
		"memory":    map[string]uint64{"sys": m.Sys >> 20, "heapAlloc": m.HeapAlloc >> 20, "heapInuse": m.HeapInuse >> 20},
		"system": map[string]any{"cpuCount": runtime.NumCPU(), "goroutines": runtime.NumGoroutine(),
			"platform": runtime.GOOS, "goVersion": runtime.Version()},
		"checks":        map[string]bool{"proxies": listeners > 0},
		"activeProxies": listeners,
	})
}

func (s *Server) version(w http.ResponseWriter, _ *http.Request) {
	u := s.Store.Get().Update
	writeJSON(w, http.StatusOK, map[string]string{
		"version":       version.Version,
		"commit":        version.Commit,
		"buildTime":     version.BuildTime,
		"updateChannel": u.Channel,
		"updateRepo":    u.Repo,
		"updateSource":  u.Source,
	})
}

func (s *Server) overview(w http.ResponseWriter, _ *http.Request) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	rules := map[string]int{"total": 0, "active": 0, "running": 0, "partial": 0, "error": 0}
	var totals metrics.Counters
	for _, r := range s.Manager.List() {
		rules["total"]++
		if r.Active() {
			rules["active"]++
		}
		rt := s.Manager.Runtime(&r)
		if rt.State != gateway.StateStopped {
			rules[rt.State]++
		}
		totals.Add(metrics.CountersOf(rt.Snapshot, 0, 0))
	}
	totals.RateUp, totals.RateDown = s.Sampler.GlobalRates()
	writeJSON(w, http.StatusOK, map[string]any{
		"version": version.Version, "commit": version.Commit, "buildTime": version.BuildTime,
		"startedAt": s.Started.UTC(), "uptime": int64(time.Since(s.Started).Seconds()),
		"goVersion": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH,
		"cpus": runtime.NumCPU(), "goroutines": runtime.NumGoroutine(),
		"memory":     map[string]uint64{"sys": m.Sys, "heapAlloc": m.HeapAlloc, "heapInuse": m.HeapInuse},
		"rules":      rules,
		"totals":     totals,
		"configPath": s.Store.Path(),
	})
}

func (s *Server) restart(w http.ResponseWriter, r *http.Request) {
	if s.Restart == nil || runtime.GOOS == "windows" {
		fail(w, http.StatusNotImplemented, "not_supported", "restart is not supported on this platform")
		return
	}
	slog.Info("restart requested via API", "client", r.RemoteAddr)
	writeJSON(w, http.StatusAccepted, struct{}{})
	go func() {
		time.Sleep(300 * time.Millisecond) // let the response flush
		if err := s.Restart(); err != nil {
			slog.Error("restart failed", "err", err)
		}
	}()
}

// Logs.

func applyLogLevel(level string) {
	if l, ok := logx.ParseLevel(level); ok {
		logx.Level.Set(l)
	}
}

func (s *Server) getLogLevel(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"level": logx.LevelName(logx.Level.Level()), "levels": logx.Levels})
}

func (s *Server) setLogLevel(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Level string `json:"level"`
	}
	if !decode(w, r, &b, 1<<10) {
		return
	}
	l, ok := logx.ParseLevel(b.Level)
	if !ok {
		failErr(w, &config.FieldError{Field: "level", Msg: "invalid log level"})
		return
	}
	name := logx.LevelName(l)
	if _, err := s.Store.Update(func(c *config.Config) error { c.Logging.Level = name; return nil }); err != nil {
		failErr(w, err)
		return
	}
	old := logx.LevelName(logx.Level.Level())
	logx.Level.Set(l)
	slog.Info("log level changed", "oldLevel", old, "newLevel", name)
	writeJSON(w, http.StatusOK, map[string]string{"level": name})
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, err := strconv.Atoi(q.Get("limit"))
	if err != nil || limit <= 0 {
		limit = 500
	}
	lvl := logx.LevelTrace
	if l, ok := logx.ParseLevel(q.Get("level")); ok {
		lvl = l
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": logx.Recent.Query(min(limit, 2000), lvl, q.Get("q"))})
}

// Settings.

type apiSettings struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	EnableCors bool   `json:"enableCors"`
}

type logSettings struct {
	Level         string `json:"level"`
	EnableConsole bool   `json:"enableConsole"`
	EnableFile    bool   `json:"enableFile"`
	LogDir        string `json:"logDir"`
	MaxFileSize   int64  `json:"maxFileSize"`
	MaxFiles      int    `json:"maxFiles"`
}

type settings struct {
	API     *apiSettings         `json:"api,omitempty"`
	Logging *logSettings         `json:"logging,omitempty"`
	Update  *config.UpdateConfig `json:"update,omitempty"`
	DataDir *string              `json:"dataDir,omitempty"`
}

func settingsOf(c *config.Config) settings {
	return settings{
		API: &apiSettings{c.API.Host, c.API.Port, c.API.EnableCors},
		Logging: &logSettings{
			Level:         c.Logging.Level,
			EnableConsole: c.Logging.EnableConsole,
			EnableFile:    c.Logging.EnableFile,
			LogDir:        c.Logging.LogDir, MaxFileSize: c.Logging.MaxFileSize, MaxFiles: c.Logging.MaxFiles,
		},
		Update:  &c.Update,
		DataDir: &c.DataDir,
	}
}

// restartRequired reports whether settings that are only read at startup
// differ from what the process is running with.
func (s *Server) restartRequired(c *config.Config) bool {
	a, b := settingsOf(s.boot), settingsOf(c)
	a.Logging.Level, b.Logging.Level = "", ""
	return *a.API != *b.API || *a.Logging != *b.Logging || *a.DataDir != *b.DataDir
}

func (s *Server) getSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, settingsOf(s.Store.Get()))
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var b settings
	if !decode(w, r, &b, 16<<10) {
		return
	}
	if err := validateSettings(&b); err != nil {
		failErr(w, err)
		return
	}
	cfg, err := s.Store.Update(func(c *config.Config) error {
		if a := b.API; a != nil {
			c.API.Host, c.API.Port, c.API.EnableCors = a.Host, a.Port, a.EnableCors
		}
		if l := b.Logging; l != nil {
			c.Logging = config.LogConfig{
				Level: l.Level, EnableConsole: l.EnableConsole, EnableFile: l.EnableFile,
				LogDir: l.LogDir, MaxFileSize: l.MaxFileSize, MaxFiles: l.MaxFiles,
			}
		}
		if b.Update != nil {
			c.Update = *b.Update
		}
		if b.DataDir != nil {
			c.DataDir = *b.DataDir
		}
		return nil
	})
	if err != nil {
		failErr(w, err)
		return
	}
	applyLogLevel(cfg.Logging.Level)
	slog.Info("settings updated")
	writeJSON(w, http.StatusOK, map[string]any{"settings": settingsOf(cfg), "restartRequired": s.restartRequired(cfg)})
}

func validateSettings(b *settings) error {
	if a := b.API; a != nil {
		a.Host = strings.TrimSpace(a.Host)
		if a.Port < 1 || a.Port > 65535 {
			return &config.FieldError{Field: "api.port", Msg: "port must be 1-65535"}
		}
		if a.Host == "" {
			return &config.FieldError{Field: "api.host", Msg: "host is required"}
		}
	}
	if l := b.Logging; l != nil {
		if _, ok := logx.ParseLevel(l.Level); !ok {
			return &config.FieldError{Field: "logging.level", Msg: "invalid log level"}
		}
		l.Level = strings.ToLower(l.Level)
		if l.MaxFileSize < 1<<10 {
			return &config.FieldError{Field: "logging.maxFileSize", Msg: "must be at least 1024 bytes"}
		}
		if l.MaxFiles < 1 {
			return &config.FieldError{Field: "logging.maxFiles", Msg: "must be at least 1"}
		}
		if strings.TrimSpace(l.LogDir) == "" {
			return &config.FieldError{Field: "logging.logDir", Msg: "log directory is required"}
		}
	}
	if u := b.Update; u != nil {
		if u.Channel != "stable" && u.Channel != "dev" {
			return &config.FieldError{Field: "update.channel", Msg: "channel must be stable or dev"}
		}
		if u.Source != "github" && u.Source != "proxy" {
			return &config.FieldError{Field: "update.source", Msg: "source must be github or proxy"}
		}
		if u.CheckInterval < 60 {
			return &config.FieldError{Field: "update.checkInterval", Msg: "interval must be at least 60 seconds"}
		}
		if owner, repo, ok := strings.Cut(u.Repo, "/"); !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
			return &config.FieldError{Field: "update.repo", Msg: "repo must be owner/name"}
		}
		if u.Source == "proxy" && !strings.HasPrefix(u.ProxyBaseURL, "https://") && !strings.HasPrefix(u.ProxyBaseURL, "http://") {
			return &config.FieldError{Field: "update.proxyBaseUrl", Msg: "proxy URL must start with http:// or https://"}
		}
	}
	if d := b.DataDir; d != nil && strings.TrimSpace(*d) == "" {
		return &config.FieldError{Field: "dataDir", Msg: "data directory is required"}
	}
	return nil
}

// Update.

func (s *Server) updateStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.Updater.Status())
}

func (s *Server) updateCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	res, err := s.Updater.CheckOnly(ctx)
	if err != nil {
		fail(w, http.StatusBadGateway, "internal", "update check failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) updateApply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Force bool `json:"force"`
	}
	// An empty body preserves the existing wait-for-idle behavior.
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10))
	if err := dec.Decode(&body); err != nil && err != io.EOF {
		fail(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		fail(w, http.StatusBadRequest, "bad_request", "expected a single JSON object")
		return
	}
	state := s.Updater.Status().State
	if body.Force || state == "ready" || state == "waiting" {
		if err := s.Updater.ApplyPending(r.Context(), body.Force); err != nil {
			fail(w, http.StatusConflict, "conflict", err.Error())
			return
		}
	} else {
		s.Updater.StartUpdate(r.Context())
	}
	writeJSON(w, http.StatusAccepted, struct{}{})
}

func (s *Server) updateDismiss(w http.ResponseWriter, _ *http.Request) {
	s.Updater.DismissPending()
	w.WriteHeader(http.StatusNoContent)
}

func FormatUptime(s int64) string {
	d, h, m, sec := s/86400, s%86400/3600, s%3600/60, s%60
	switch {
	case d > 0:
		return fmt.Sprintf("%dd %dh %dm %ds", d, h, m, sec)
	case h > 0:
		return fmt.Sprintf("%dh %dm %ds", h, m, sec)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, sec)
	}
	return fmt.Sprintf("%ds", sec)
}
