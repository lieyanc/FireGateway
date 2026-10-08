// Package events is a small pub/sub used to push live updates to SSE clients.
// Payloads are encoded once per publish and shared by all subscribers.
package events

import (
	"encoding/json"
	"log/slog"
	"sync"
)

type Event struct {
	Name  string
	Data  []byte // JSON
	Value any    // the published value, for subscribers that filter it
}

type Broker struct {
	mu   sync.RWMutex
	subs map[chan Event]struct{}
}

func NewBroker() *Broker { return &Broker{subs: make(map[chan Event]struct{})} }

func (b *Broker) Publish(name string, v any) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if len(b.subs) == 0 {
		return
	}
	data, err := json.Marshal(v)
	if err != nil {
		slog.Error("encode event", "event", name, "err", err)
		return
	}
	e := Event{name, data, v}
	for ch := range b.subs {
		select {
		case ch <- e:
		default: // a stalled client misses ticks instead of blocking everyone
		}
	}
}

func (b *Broker) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 32)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, ch)
			b.mu.Unlock()
		})
	}
}
