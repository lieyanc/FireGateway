package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/lieyanc/FireGateway/internal/config"
	"github.com/lieyanc/FireGateway/internal/events"
	"github.com/lieyanc/FireGateway/internal/gateway"
	"github.com/lieyanc/FireGateway/internal/logx"
	"github.com/lieyanc/FireGateway/internal/metrics"
)

const ssePing = 15 * time.Second

// sseStart prepares a streaming response and disables the write deadline.
func sseStart(w http.ResponseWriter) *http.ResponseController {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // nginx: do not buffer the stream
	rc := http.NewResponseController(w)
	rc.SetWriteDeadline(time.Time{})
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": connected\n\n")
	rc.Flush()
	return rc
}

// eventFor adapts a broadcast event to the caller: tenant members get stats
// for their own rules only and no ids of other tenants' rules.
func (s *Server) eventFor(scope gateway.Scope, e events.Event) []byte {
	if scope == gateway.Admin {
		return e.Data
	}
	switch v := e.Value.(type) {
	case metrics.StatsEvent:
		b, _ := json.Marshal(v.ForTenant(scope.Tenant))
		return b
	case map[string]string:
		if id, ok := v["id"]; ok {
			if rule, found := s.Manager.Get(config.RuleID(id)); !found || !scope.Owns(&rule) {
				b, _ := json.Marshal(map[string]string{"action": v["action"]})
				return b
			}
		}
	}
	return e.Data
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	scope := scopeOf(r)
	ch, cancel := s.Broker.Subscribe()
	defer cancel()
	rc := sseStart(w)
	ping := time.NewTicker(ssePing)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-ch:
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Name, s.eventFor(scope, e)); err != nil {
				return
			}
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
		}
		if rc.Flush() != nil {
			return
		}
	}
}

func (s *Server) logStream(w http.ResponseWriter, r *http.Request) {
	lvl := logx.LevelTrace
	if l, ok := logx.ParseLevel(r.URL.Query().Get("level")); ok {
		lvl = l
	}
	keep := s.logFilter(r)
	ch, cancel := logx.Recent.Subscribe(lvl)
	defer cancel()
	rc := sseStart(w)
	ping := time.NewTicker(ssePing)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			if keep == nil || keep(&e) {
				data, _ := json.Marshal(e)
				if _, err := fmt.Fprintf(w, "event: log\ndata: %s\n\n", data); err != nil {
					return
				}
			}
			// Coalesce bursts into one flush.
			for drained := false; !drained; {
				select {
				case e, ok := <-ch:
					if !ok {
						return
					}
					if keep == nil || keep(&e) {
						data, _ := json.Marshal(e)
						fmt.Fprintf(w, "event: log\ndata: %s\n\n", data)
					}
				default:
					drained = true
				}
			}
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
		}
		if rc.Flush() != nil {
			return
		}
	}
}
