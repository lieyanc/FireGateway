package dnscache

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeDNS struct {
	mu    sync.Mutex
	addrs []netip.Addr
	err   error
	calls atomic.Int32
}

func (f *fakeDNS) set(err error, addrs ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err, f.addrs = err, nil
	for _, a := range addrs {
		f.addrs = append(f.addrs, netip.MustParseAddr(a))
	}
}

func (f *fakeDNS) lookup(context.Context, string) ([]netip.Addr, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.addrs, f.err
}

func newFake() (*Cache, *fakeDNS) {
	f := &fakeDNS{}
	c := New()
	c.lookup = f.lookup
	return c, f
}

func TestIPLiteralBypassesCache(t *testing.T) {
	c, f := newFake()
	c.Acquire("::1")
	got, err := c.Lookup(context.Background(), "192.0.2.1")
	if err != nil || len(got) != 1 || got[0] != netip.MustParseAddr("192.0.2.1") {
		t.Fatalf("got %v, %v", got, err)
	}
	if f.calls.Load() != 0 || len(c.Entries()) != 0 {
		t.Fatal("IP literals must not be resolved or cached")
	}
}

func TestCachedLookupAndStaleOnFailure(t *testing.T) {
	c, f := newFake()
	f.set(nil, "192.0.2.1", "::ffff:192.0.2.1", "2001:db8::1")
	c.Acquire("example.test")
	defer c.Release("example.test")

	got, err := c.Lookup(context.Background(), "example.test")
	if err != nil || len(got) != 2 || got[0] != netip.MustParseAddr("192.0.2.1") {
		t.Fatalf("got %v, %v (mapped duplicate should be folded)", got, err)
	}
	c.Lookup(context.Background(), "example.test")
	if n := f.calls.Load(); n != 1 {
		t.Fatalf("lookups = %d, want 1 (cached)", n)
	}

	// A failed refresh keeps serving the last good answer.
	f.set(errors.New("server failure"))
	es, err := c.Refresh("example.test")
	if err != nil || len(es) != 1 || es[0].Error == "" || len(es[0].Addrs) != 2 {
		t.Fatalf("refresh: %+v, %v", es, err)
	}
	if got, err := c.Lookup(context.Background(), "example.test"); err != nil || len(got) != 2 {
		t.Fatalf("stale lookup: %v, %v", got, err)
	}

	f.set(nil, "192.0.2.9")
	if _, err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Lookup(context.Background(), "example.test"); len(got) != 1 || got[0] != netip.MustParseAddr("192.0.2.9") {
		t.Fatalf("after refresh: %v", got)
	}
}

func TestFirstLookupFailure(t *testing.T) {
	c, f := newFake()
	f.set(errors.New("no such host"))
	c.Acquire("bad.test")
	defer c.Release("bad.test")
	if _, err := c.Lookup(context.Background(), "bad.test"); err == nil {
		t.Fatal("expected error")
	}
}

func TestRefcounting(t *testing.T) {
	c, f := newFake()
	f.set(nil, "192.0.2.1")
	c.Acquire("a.test")
	c.Acquire("a.test")
	c.Release("a.test")
	if len(c.Entries()) != 1 {
		t.Fatal("entry dropped while still referenced")
	}
	c.Release("a.test")
	if len(c.Entries()) != 0 {
		t.Fatal("entry kept after last release")
	}
	if _, err := c.Refresh("a.test"); !errors.Is(err, ErrNotCached) {
		t.Fatalf("refresh of released host: %v", err)
	}
	// Uncached hosts still resolve, just without caching.
	if got, err := c.Lookup(context.Background(), "a.test"); err != nil || len(got) != 1 {
		t.Fatalf("uncached lookup: %v, %v", got, err)
	}
}

func TestRetryBackoff(t *testing.T) {
	c, f := newFake()
	f.set(errors.New("server failure"))
	c.Acquire("down.test")
	defer c.Release("down.test")
	c.Lookup(context.Background(), "down.test")
	var gaps []time.Duration
	for range 6 {
		es, _ := c.Refresh("down.test")
		gaps = append(gaps, es[0].NextAt.Sub(es[0].CheckedAt).Round(time.Second))
	}
	want := []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second, TTL, TTL, TTL}
	for i := range want {
		if gaps[i] != want[i] {
			t.Fatalf("retry delays = %v, want %v", gaps, want)
		}
	}
	f.set(nil, "192.0.2.1")
	es, _ := c.Refresh("down.test")
	if d := es[0].NextAt.Sub(es[0].CheckedAt).Round(time.Second); d != TTL || es[0].Error != "" {
		t.Fatalf("after recovery: next in %v, error %q", d, es[0].Error)
	}
}
