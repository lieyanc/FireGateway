// Package proxy implements the TCP and UDP forwarding engine. A Runner is
// the runtime of one rule: its listeners, live connections, counters and
// hot-swappable policy (targets, ACL, limits).
package proxy

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"github.com/lieyanc/FireGateway/internal/config"
	"github.com/lieyanc/FireGateway/internal/dnscache"
)

// policy is the part of a rule that can change without re-binding.
type policy struct {
	name      string
	host      string   // target host, resolved through dnscache
	ports     []int    // target port per mapping
	targets   []string // host:port per mapping, for display
	aclAllow  bool     // allow-list when true, deny-list when false
	acl       []netip.Prefix
	maxConns  int
	maxPerIP  int
	chunk     int // max bytes per copy step, bounded so rate limiting stays smooth
	limitUp   *rate.Limiter
	limitDown *rate.Limiter
}

func newPolicy(r *config.Rule, mappings []config.Mapping) *policy {
	p := &policy{name: r.Name, host: r.TargetHost, chunk: maxChunk}
	p.ports = make([]int, len(mappings))
	p.targets = make([]string, len(mappings))
	for i, m := range mappings {
		p.ports[i] = m.Target
		p.targets[i] = net.JoinHostPort(r.TargetHost, strconv.Itoa(m.Target))
	}
	if r.ACL != nil {
		p.aclAllow = r.ACL.Mode == "allow"
		for _, c := range r.ACL.CIDRs {
			if pfx, err := config.ParsePrefix(c); err == nil {
				p.acl = append(p.acl, pfx)
			}
		}
	}
	if l := r.Limits; l != nil {
		p.maxConns, p.maxPerIP = l.MaxConnections, l.MaxConnectionsPerIP
		if l.Bandwidth > 0 {
			// Steps of ~1/10s of budget keep throughput smooth; the burst
			// must still fit a full UDP datagram.
			p.chunk = int(min(max(l.Bandwidth/10, 1024), maxChunk))
			burst := max(p.chunk, 64<<10)
			p.limitUp = rate.NewLimiter(rate.Limit(l.Bandwidth), burst)
			p.limitDown = rate.NewLimiter(rate.Limit(l.Bandwidth), burst)
		}
	}
	return p
}

func (p *policy) allowed(a netip.Addr) bool {
	if p.acl == nil {
		return true
	}
	a = a.Unmap()
	hit := slices.ContainsFunc(p.acl, func(pfx netip.Prefix) bool { return pfx.Contains(a) })
	return hit == p.aclAllow
}

// Snapshot is a point-in-time copy of a runner's counters.
type Snapshot struct {
	Active   int64
	Total    int64
	BytesUp  int64
	BytesDn  int64
	Errors   int64
	Rejected int64
	Messages int64
}

// Failure records a port that could not be bound.
type Failure struct {
	Port  int    `json:"port"`
	Error string `json:"error"`
}

type listener interface {
	Close() error
}

type Runner struct {
	ID        config.RuleID
	Type      string
	StartedAt time.Time

	gate func() bool
	pol  atomic.Pointer[policy]
	log  *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc

	listeners []listener
	failures  []Failure

	mu    sync.Mutex
	conns map[uint64]*Conn
	perIP map[netip.Addr]int
	// Bytes of released connections. Live ones are summed on Snapshot, so
	// the copy loops never write to counters shared across connections.
	closedUp, closedDn int64

	total, errors, rejected, messages atomic.Int64
}

// Start binds every mapping of r. Bind failures are recorded, not fatal, so a
// rule with one busy port in a range still serves the rest.
func Start(r *config.Rule) (*Runner, error) { return StartGuarded(r, nil) }

func StartGuarded(r *config.Rule, gate func() bool) (*Runner, error) {
	mappings, err := r.Mappings()
	if err != nil {
		return nil, err
	}
	if r.Type != "tcp" && r.Type != "udp" {
		return nil, errors.New("type must be tcp or udp")
	}
	ctx, cancel := context.WithCancel(context.Background())
	rn := &Runner{
		ID: r.ID, Type: r.Type, StartedAt: time.Now(), gate: gate,
		log:    slog.With("ruleId", string(r.ID), "type", r.Type),
		ctx:    ctx,
		cancel: cancel,
		conns:  make(map[uint64]*Conn),
		perIP:  make(map[netip.Addr]int),
	}
	rn.pol.Store(newPolicy(r, mappings))
	dnscache.Default.Acquire(r.TargetHost)
	for i, m := range mappings {
		addr := net.JoinHostPort(r.LocalHost, strconv.Itoa(m.Local))
		var (
			l   listener
			err error
		)
		if r.Type == "tcp" {
			l, err = rn.listenTCP(i, addr)
		} else {
			l, err = rn.listenUDP(i, addr)
		}
		if err != nil {
			rn.failures = append(rn.failures, Failure{m.Local, err.Error()})
			rn.log.Error("failed to start listener", "listen", addr, "err", err)
			continue
		}
		rn.listeners = append(rn.listeners, l)
	}
	rn.log.Info("rule started", "name", r.Name, "listeners", len(rn.listeners), "failed", len(rn.failures))
	return rn, nil
}

// Apply swaps the hot-updatable policy. The caller must ensure the bind key
// is unchanged. Connections the new ACL denies are closed.
func (r *Runner) Apply(rule *config.Rule) error {
	mappings, err := rule.Mappings()
	if err != nil {
		return err
	}
	p := newPolicy(rule, mappings)
	if old := r.pol.Swap(p); old.host != p.host {
		dnscache.Default.Acquire(p.host)
		dnscache.Default.Release(old.host)
	}
	var denied []*Conn
	r.mu.Lock()
	for _, c := range r.conns {
		if !p.allowed(c.client.Addr()) {
			denied = append(denied, c)
		}
	}
	r.mu.Unlock()
	for _, c := range denied {
		c.Close()
	}
	if len(denied) > 0 {
		r.log.Info("closed connections denied by updated ACL", "count", len(denied))
	}
	return nil
}

// Stop closes all listeners and live connections.
func (r *Runner) Stop() {
	r.cancel()
	for _, l := range r.listeners {
		l.Close()
	}
	r.CloseAll()
	dnscache.Default.Release(r.policy().host)
	r.log.Info("rule stopped")
}

func (r *Runner) Listeners() int      { return len(r.listeners) }
func (r *Runner) Failures() []Failure { return r.failures }
func (r *Runner) policy() *policy     { return r.pol.Load() }
func (r *Runner) Name() string        { return r.policy().name }

// debugOn reports whether debug logs are emitted, so hot paths can skip
// building their arguments.
func (r *Runner) debugOn() bool { return r.log.Enabled(context.Background(), slog.LevelDebug) }

func (r *Runner) Snapshot() Snapshot {
	r.mu.Lock()
	s := Snapshot{Active: int64(len(r.conns)), BytesUp: r.closedUp, BytesDn: r.closedDn}
	for _, c := range r.conns {
		s.BytesUp += c.up.Load()
		s.BytesDn += c.down.Load()
	}
	r.mu.Unlock()
	s.Total = r.total.Load()
	s.Errors = r.errors.Load()
	s.Rejected = r.rejected.Load()
	s.Messages = r.messages.Load()
	return s
}

var connSeq atomic.Uint64

// admit applies ACL and connection limits and registers a new connection.
// It returns nil when the connection must be refused.
func (r *Runner) admit(client netip.AddrPort, listen, target string, closeFn func()) *Conn {
	if r.gate != nil && !r.gate() {
		return nil
	}
	p := r.policy()
	ip := client.Addr().Unmap()
	if !p.allowed(ip) {
		r.rejected.Add(1)
		if r.debugOn() {
			r.log.Debug("connection denied by ACL", "client", client.String())
		}
		return nil
	}
	now := time.Now()
	c := &Conn{
		id: connSeq.Add(1), runner: r, client: client,
		listen: listen, target: target, start: now, closeFn: closeFn,
	}
	c.last.Store(now.UnixNano())
	c.ctx, c.cancel = context.WithCancel(r.ctx)

	r.mu.Lock()
	switch {
	case p.maxConns > 0 && len(r.conns) >= p.maxConns:
		r.mu.Unlock()
		c.cancel()
		r.rejected.Add(1)
		if r.debugOn() {
			r.log.Debug("connection refused: rule connection limit", "client", client.String(), "limit", p.maxConns)
		}
		return nil
	case p.maxPerIP > 0 && r.perIP[ip] >= p.maxPerIP:
		r.mu.Unlock()
		c.cancel()
		r.rejected.Add(1)
		if r.debugOn() {
			r.log.Debug("connection refused: per-IP limit", "client", client.String(), "limit", p.maxPerIP)
		}
		return nil
	}
	r.conns[c.id] = c
	r.perIP[ip]++
	r.mu.Unlock()
	r.total.Add(1)
	return c
}

func (r *Runner) release(c *Conn) {
	ip := c.client.Addr().Unmap()
	r.mu.Lock()
	if _, ok := r.conns[c.id]; ok {
		delete(r.conns, c.id)
		r.closedUp += c.up.Load()
		r.closedDn += c.down.Load()
		if r.perIP[ip]--; r.perIP[ip] <= 0 {
			delete(r.perIP, ip)
		}
	}
	r.mu.Unlock()
	c.cancel()
}

// Conns returns info on all live connections.
func (r *Runner) Conns() []ConnInfo {
	r.mu.Lock()
	out := make([]ConnInfo, 0, len(r.conns))
	for _, c := range r.conns {
		out = append(out, c.info())
	}
	r.mu.Unlock()
	return out
}

// CloseConn closes one connection by id.
func (r *Runner) CloseConn(id uint64) bool {
	r.mu.Lock()
	c := r.conns[id]
	r.mu.Unlock()
	if c == nil {
		return false
	}
	c.Close()
	return true
}

// CloseAll closes every live connection and returns how many were closed.
func (r *Runner) CloseAll() int {
	r.mu.Lock()
	cs := make([]*Conn, 0, len(r.conns))
	for _, c := range r.conns {
		cs = append(cs, c)
	}
	r.mu.Unlock()
	for _, c := range cs {
		c.Close()
	}
	return len(cs)
}

// Conn is a tracked TCP connection or UDP session.
type Conn struct {
	id             uint64
	runner         *Runner
	client         netip.AddrPort
	listen, target string
	start          time.Time
	last           atomic.Int64 // unix nanos
	up, down       atomic.Int64
	ctx            context.Context
	cancel         context.CancelFunc
	closeFn        func()
	closeOnce      sync.Once
}

func (c *Conn) Close() {
	c.closeOnce.Do(func() {
		c.cancel()
		c.closeFn()
	})
}

type ConnInfo struct {
	ID         uint64
	RuleID     config.RuleID
	Type       string
	Listen     string
	Target     string
	Client     string
	StartedAt  time.Time
	LastActive time.Time
	BytesUp    int64
	BytesDown  int64
}

func (c *Conn) info() ConnInfo {
	return ConnInfo{
		ID: c.id, RuleID: c.runner.ID, Type: c.runner.Type,
		Listen: c.listen, Target: c.target, Client: c.client.String(),
		StartedAt: c.start, LastActive: time.Unix(0, c.last.Load()),
		BytesUp: c.up.Load(), BytesDown: c.down.Load(),
	}
}

// account records n bytes moved in one direction and applies the rule's
// bandwidth limit. It returns an error only if the connection was closed
// while waiting for budget.
func (c *Conn) account(n int, up bool) error {
	c.last.Store(time.Now().UnixNano())
	p := c.runner.policy()
	lim := p.limitDown
	if up {
		c.up.Add(int64(n))
		lim = p.limitUp
	} else {
		c.down.Add(int64(n))
	}
	if lim == nil {
		return nil
	}
	// A policy swap can shrink the burst below n; split rather than fail.
	for n > 0 {
		step := min(n, lim.Burst())
		if err := lim.WaitN(c.ctx, step); err != nil {
			return err
		}
		n -= step
	}
	return nil
}

func (c *Conn) chunk() int { return c.runner.policy().chunk }
