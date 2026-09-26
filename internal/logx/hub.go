package logx

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

type Entry struct {
	Seq   uint64            `json:"seq"`
	Time  time.Time         `json:"time"`
	Level string            `json:"level"`
	Msg   string            `json:"msg"`
	Attrs map[string]string `json:"attrs,omitempty"`

	level slog.Level
}

// Hub is a fixed-size ring of recent entries with live subscribers.
type Hub struct {
	mu   sync.RWMutex
	buf  []Entry
	next int // write position
	full bool
	seq  uint64
	subs map[chan Entry]struct{}
}

func NewHub(size int) *Hub {
	return &Hub{buf: make([]Entry, size), subs: make(map[chan Entry]struct{})}
}

func (h *Hub) add(e Entry) {
	h.mu.Lock()
	h.seq++
	e.Seq = h.seq
	h.buf[h.next] = e
	h.next = (h.next + 1) % len(h.buf)
	if h.next == 0 {
		h.full = true
	}
	for ch := range h.subs {
		select {
		case ch <- e:
		default: // slow subscriber: drop rather than block logging
		}
	}
	h.mu.Unlock()
}

// Query returns up to limit entries at or above minLevel whose message or
// attributes contain q (case-insensitive), oldest first.
func (h *Hub) Query(limit int, minLevel slog.Level, q string) []Entry {
	q = strings.ToLower(q)
	h.mu.RLock()
	defer h.mu.RUnlock()
	n := h.next
	if h.full {
		n = len(h.buf)
	}
	out := make([]Entry, 0, min(limit, n))
	// Walk newest to oldest so the limit keeps the most recent entries.
	for i := 0; i < n && len(out) < limit; i++ {
		e := h.buf[(h.next-1-i+len(h.buf))%len(h.buf)]
		if e.level < minLevel || (q != "" && !e.matches(q)) {
			continue
		}
		out = append(out, e)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (e *Entry) matches(q string) bool {
	if strings.Contains(strings.ToLower(e.Msg), q) {
		return true
	}
	for k, v := range e.Attrs {
		if strings.Contains(strings.ToLower(k+"="+v), q) {
			return true
		}
	}
	return false
}

// Subscribe returns a channel receiving new entries at or above minLevel and
// a cancel func that must be called to release it.
func (h *Hub) Subscribe(minLevel slog.Level) (<-chan Entry, func()) {
	raw := make(chan Entry, 256)
	out := make(chan Entry, 256)
	h.mu.Lock()
	h.subs[raw] = struct{}{}
	h.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(out)
		for {
			select {
			case e := <-raw:
				if e.level >= minLevel {
					select {
					case out <- e:
					default:
					}
				}
			case <-done:
				return
			}
		}
	}()
	var once sync.Once
	return out, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, raw)
			h.mu.Unlock()
			close(done)
		})
	}
}

// ringHandler feeds records into a Hub, honoring the global Level.
type ringHandler struct {
	hub    *Hub
	attrs  []slog.Attr
	prefix string // group prefix
}

func (r *ringHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= Level.Level() }

func (r *ringHandler) Handle(_ context.Context, rec slog.Record) error {
	e := Entry{Time: rec.Time, Level: levelLabel(rec.Level), Msg: rec.Message, level: rec.Level}
	if n := len(r.attrs) + rec.NumAttrs(); n > 0 {
		e.Attrs = make(map[string]string, n)
		for _, a := range r.attrs {
			flatten(e.Attrs, "", a)
		}
		rec.Attrs(func(a slog.Attr) bool {
			flatten(e.Attrs, r.prefix, a)
			return true
		})
	}
	r.hub.add(e)
	return nil
}

func flatten(m map[string]string, prefix string, a slog.Attr) {
	v := a.Value.Resolve()
	if v.Kind() == slog.KindGroup {
		p := prefix
		if a.Key != "" {
			p += a.Key + "."
		}
		for _, g := range v.Group() {
			flatten(m, p, g)
		}
		return
	}
	if a.Key == "" {
		return
	}
	m[prefix+a.Key] = v.String()
}

func (r *ringHandler) WithAttrs(as []slog.Attr) slog.Handler {
	out := *r
	out.attrs = make([]slog.Attr, 0, len(r.attrs)+len(as))
	out.attrs = append(out.attrs, r.attrs...)
	for _, a := range as {
		if r.prefix != "" {
			a.Key = r.prefix + a.Key
		}
		out.attrs = append(out.attrs, a)
	}
	return &out
}

func (r *ringHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return r
	}
	out := *r
	out.prefix = r.prefix + name + "."
	return &out
}
