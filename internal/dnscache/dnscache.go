// Package dnscache resolves forwarding target hostnames in the background and
// shares the answers across connections. Go's resolver does not cache, and a
// lookup inside a UDP listener's read loop would stall every client of that
// port. Hosts are resolved when a rule starts using them and re-resolved on a
// timer, so dials read addresses from memory; if a refresh fails the last good
// addresses stay in use.
package dnscache

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	// TTL is how long a successful answer is used before re-resolving. The
	// system resolver does not expose record TTLs, so this is fixed.
	TTL = time.Minute
	// RetryInterval is the delay before retrying a failed lookup. It doubles
	// with each consecutive failure, up to TTL.
	RetryInterval = 5 * time.Second
	lookupTimeout = 5 * time.Second
)

// Default is the process-wide cache used by the proxy.
var Default = New()

type entry struct {
	refs     int
	addrs    []netip.Addr
	err      error
	fails    int       // consecutive failed lookups
	resolved time.Time // last successful lookup
	checked  time.Time // last completed lookup, successful or not
	next     time.Time // scheduled refresh
	timer    *time.Timer
	ready    chan struct{} // closed once the first lookup completes
}

type Cache struct {
	mu      sync.Mutex
	entries map[string]*entry
	lookup  func(context.Context, string) ([]netip.Addr, error)
}

func New() *Cache {
	return &Cache{
		entries: make(map[string]*entry),
		lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
	}
}

// isIP reports whether host needs no resolution.
func isIP(host string) bool {
	_, err := netip.ParseAddr(host)
	return err == nil
}

// Acquire registers a user of host and starts resolving it if it is new.
// Every Acquire must be paired with a Release.
func (c *Cache) Acquire(host string) {
	if host == "" || isIP(host) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[host]; e != nil {
		e.refs++
		return
	}
	e := &entry{refs: 1, ready: make(chan struct{})}
	c.entries[host] = e
	go c.refresh(host, e)
}

// Release drops a user of host, forgetting it once no rule uses it.
func (c *Cache) Release(host string) {
	if host == "" || isIP(host) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[host]
	if e == nil {
		return
	}
	if e.refs--; e.refs <= 0 {
		if e.timer != nil {
			e.timer.Stop()
		}
		delete(c.entries, host)
	}
}

// Lookup returns the addresses of host. For an acquired host it only waits
// if the first lookup is still in flight; otherwise it resolves directly.
func (c *Cache) Lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a}, nil
	}
	c.mu.Lock()
	e := c.entries[host]
	c.mu.Unlock()
	if e == nil {
		return c.resolve(ctx, host)
	}
	select {
	case <-e.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(e.addrs) > 0 {
		return e.addrs, nil // possibly stale if the last refresh failed
	}
	return nil, e.err
}

func (c *Cache) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	addrs, err := c.lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		if a = a.Unmap(); !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no addresses for " + host)
	}
	return out, nil
}

// refresh resolves host now and schedules the next refresh. It returns the
// lookup error, if any.
func (c *Cache) refresh(host string, e *entry) error {
	addrs, err := c.resolve(context.Background(), host)
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	e.checked = now
	if err == nil {
		switch {
		case e.fails > 0:
			slog.Info("target host resolved again", "host", host, "addrs", addrs, "failures", e.fails)
		case e.addrs != nil && !sameSet(addrs, e.addrs):
			slog.Info("target host addresses changed", "host", host, "addrs", addrs)
		}
		e.addrs, e.err, e.fails, e.resolved = addrs, nil, 0, now
	} else {
		e.err = err
		// Warn once per outage; retries are logged at debug level.
		if e.fails++; e.fails == 1 {
			slog.Warn("resolve target host failed", "host", host, "err", err, "keepingStale", len(e.addrs) > 0)
		} else {
			slog.Debug("resolve target host failed", "host", host, "err", err, "failures", e.fails)
		}
	}
	select {
	case <-e.ready:
	default:
		close(e.ready)
	}
	if c.entries[host] != e {
		return err // released meanwhile
	}
	delay := TTL
	if err != nil {
		delay = min(RetryInterval<<min(e.fails-1, 8), TTL)
	}
	e.next = now.Add(delay)
	if e.timer != nil {
		e.timer.Stop()
	}
	e.timer = time.AfterFunc(delay, func() { c.refresh(host, e) })
	return err
}

// sameSet ignores order, which round-robin DNS shuffles between lookups.
func sameSet(a, b []netip.Addr) bool {
	return len(a) == len(b) && !slices.ContainsFunc(a, func(x netip.Addr) bool { return !slices.Contains(b, x) })
}

// Entry is a snapshot of one cached host.
type Entry struct {
	Host       string    `json:"host"`
	Addrs      []string  `json:"addrs"`
	Error      string    `json:"error,omitempty"`
	Pending    bool      `json:"pending"` // first lookup still in flight
	ResolvedAt time.Time `json:"resolvedAt"`
	CheckedAt  time.Time `json:"checkedAt"`
	NextAt     time.Time `json:"nextAt"`
}

func (e *entry) view(host string) Entry {
	v := Entry{Host: host, Addrs: make([]string, len(e.addrs)),
		ResolvedAt: e.resolved, CheckedAt: e.checked, NextAt: e.next}
	for i, a := range e.addrs {
		v.Addrs[i] = a.String()
	}
	if e.err != nil {
		v.Error = e.err.Error()
	}
	select {
	case <-e.ready:
	default:
		v.Pending = true
	}
	return v
}

// Entries lists all cached hosts, sorted by name.
func (c *Cache) Entries() []Entry {
	c.mu.Lock()
	out := make([]Entry, 0, len(c.entries))
	for h, e := range c.entries {
		out = append(out, e.view(h))
	}
	c.mu.Unlock()
	slices.SortFunc(out, func(a, b Entry) int { return strings.Compare(a.Host, b.Host) })
	return out
}

// ErrNotCached is returned by Refresh for a host no running rule uses.
var ErrNotCached = errors.New("host is not in the DNS cache")

// Refresh re-resolves the given hosts now, or every cached host if none are
// given, and returns their updated entries. Lookup failures are reported in
// the entries rather than as an error.
func (c *Cache) Refresh(hosts ...string) ([]Entry, error) {
	c.mu.Lock()
	targets := make(map[string]*entry)
	if len(hosts) == 0 {
		for h, e := range c.entries {
			targets[h] = e
		}
	}
	for _, h := range hosts {
		e := c.entries[h]
		if e == nil {
			c.mu.Unlock()
			return nil, ErrNotCached
		}
		targets[h] = e
	}
	c.mu.Unlock()

	var wg sync.WaitGroup
	for h, e := range targets {
		wg.Go(func() { c.refresh(h, e) })
	}
	wg.Wait()

	c.mu.Lock()
	out := make([]Entry, 0, len(targets))
	for h, e := range targets {
		out = append(out, e.view(h))
	}
	c.mu.Unlock()
	slices.SortFunc(out, func(a, b Entry) int { return strings.Compare(a.Host, b.Host) })
	return out, nil
}
