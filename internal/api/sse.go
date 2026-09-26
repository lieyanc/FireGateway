package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/lieyanc/FireGateway/internal/logx"
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

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
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
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Name, e.Data); err != nil {
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
			data, _ := json.Marshal(e)
			if _, err := fmt.Fprintf(w, "event: log\ndata: %s\n\n", data); err != nil {
				return
			}
			// Coalesce bursts into one flush.
			for drained := false; !drained; {
				select {
				case e, ok := <-ch:
					if !ok {
						return
					}
					data, _ := json.Marshal(e)
					fmt.Fprintf(w, "event: log\ndata: %s\n\n", data)
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
