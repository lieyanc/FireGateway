package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"time"
)

type apiServer struct {
	srv     *http.Server
	proxies []Proxy
	start   time.Time
	cors    bool
}

func startAPI(c APIConfig, proxies []Proxy, start time.Time) (*apiServer, error) {
	a := &apiServer{proxies: proxies, start: start, cors: boolOr(c.EnableCors, true)}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", a.health)
	mux.HandleFunc("/stats", a.stats)
	mux.HandleFunc("/status", a.status)
	mux.HandleFunc("/proxy-stats", a.proxyStats)
	mux.HandleFunc("/log-level", a.logLevel)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		a.fail(w, http.StatusNotFound, "Not Found", "Endpoint "+r.URL.Path+" not found")
	})

	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	a.srv = &http.Server{Handler: a.withCors(mux), ReadHeaderTimeout: 5 * time.Second}
	go a.srv.Serve(ln)
	slog.Info("API server started", "addr", addr)
	return a, nil
}

func (a *apiServer) Close() error { return a.srv.Close() }

func (a *apiServer) withCors(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.cors {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusOK)
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}

func (a *apiServer) health(w http.ResponseWriter, _ *http.Request) {
	up := int64(time.Since(a.start).Seconds())
	ok := len(a.proxies) > 0
	status, code := "healthy", http.StatusOK
	if !ok {
		status, code = "unhealthy", http.StatusServiceUnavailable
	}
	a.json(w, code, map[string]any{
		"status":        status,
		"timestamp":     time.Now().UTC(),
		"uptime":        map[string]any{"seconds": up, "human": formatUptime(up)},
		"memory":        memStats(),
		"system":        map[string]any{"cpuCount": runtime.NumCPU(), "goroutines": runtime.NumGoroutine(), "platform": runtime.GOOS, "goVersion": runtime.Version()},
		"checks":        map[string]bool{"proxies": ok},
		"activeProxies": len(a.proxies),
	})
}

func (a *apiServer) stats(w http.ResponseWriter, _ *http.Request) {
	a.json(w, http.StatusOK, map[string]any{
		"timestamp": time.Now().UTC(),
		"uptime":    time.Since(a.start).Milliseconds(),
		"system":    map[string]any{"memory": memStats(), "cpuCount": runtime.NumCPU(), "goroutines": runtime.NumGoroutine()},
		"proxy":     summarize(a.proxies),
	})
}

func (a *apiServer) status(w http.ResponseWriter, _ *http.Request) {
	list := make([]ProxyStats, len(a.proxies))
	for i, p := range a.proxies {
		list[i] = p.Stats()
	}
	a.json(w, http.StatusOK, map[string]any{
		"timestamp":    time.Now().UTC(),
		"totalProxies": len(a.proxies),
		"proxies":      list,
	})
}

func (a *apiServer) proxyStats(w http.ResponseWriter, _ *http.Request) {
	m := make(map[string]ProxyStats, len(a.proxies))
	for _, p := range a.proxies {
		s := p.Stats()
		m[s.ID] = s
	}
	a.json(w, http.StatusOK, map[string]any{
		"timestamp":    time.Now().UTC(),
		"totalProxies": len(a.proxies),
		"proxyStats":   m,
	})
}

func (a *apiServer) logLevel(w http.ResponseWriter, r *http.Request) {
	levels := []string{"error", "warn", "info", "debug", "trace"}
	switch r.Method {
	case http.MethodGet:
		a.json(w, http.StatusOK, map[string]any{"currentLevel": levelName(logLevel.Level()), "availableLevels": levels})
	case http.MethodPost:
		var body struct {
			Level string `json:"level"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
			a.fail(w, http.StatusBadRequest, "Bad Request", "Invalid JSON")
			return
		}
		l, ok := parseLevel(body.Level)
		if !ok {
			a.fail(w, http.StatusBadRequest, "Bad Request", "Invalid log level")
			return
		}
		old := levelName(logLevel.Level())
		logLevel.Set(l)
		slog.Info("log level changed via API", "oldLevel", old, "newLevel", body.Level, "changedBy", r.RemoteAddr)
		a.json(w, http.StatusOK, map[string]any{"message": "Log level updated successfully", "oldLevel": old, "newLevel": body.Level})
	default:
		a.fail(w, http.StatusMethodNotAllowed, "Method Not Allowed", "Only GET and POST methods are allowed")
	}
}

func (a *apiServer) json(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func (a *apiServer) fail(w http.ResponseWriter, code int, errName, msg string) {
	a.json(w, code, map[string]any{"error": errName, "message": msg, "timestamp": time.Now().UTC(), "statusCode": code})
}

type summary struct {
	TotalProxies      int   `json:"totalProxies"`
	TotalConnections  int64 `json:"totalConnections"`
	ActiveConnections int64 `json:"activeConnections"`
	TotalErrors       int64 `json:"totalErrors"`
	TotalMessages     int64 `json:"totalMessages"`
	BytesUpstream     int64 `json:"bytesUpstream"`
	BytesDownstream   int64 `json:"bytesDownstream"`
}

func summarize(proxies []Proxy) summary {
	s := summary{TotalProxies: len(proxies)}
	for _, p := range proxies {
		st := p.Stats()
		s.TotalConnections += st.TotalConnections
		s.ActiveConnections += st.ActiveConnections
		s.TotalErrors += st.Errors
		s.TotalMessages += st.MessagesForwarded
		s.BytesUpstream += st.BytesUpstream
		s.BytesDownstream += st.BytesDownstream
	}
	return s
}

// memStats reports memory in MB.
func memStats() map[string]uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return map[string]uint64{"sys": m.Sys >> 20, "heapAlloc": m.HeapAlloc >> 20, "heapInuse": m.HeapInuse >> 20}
}

func formatUptime(s int64) string {
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
