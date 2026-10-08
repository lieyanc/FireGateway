// Package metrics samples rule counters every second, derives rates, keeps
// realtime and historical traffic series and pushes live stats to the UI.
package metrics

import (
	"context"
	"encoding/gob"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lieyanc/FireGateway/internal/config"
	"github.com/lieyanc/FireGateway/internal/events"
	"github.com/lieyanc/FireGateway/internal/gateway"
	"github.com/lieyanc/FireGateway/internal/proxy"
)

type Point struct {
	T      int64 `json:"t"`
	Up     int64 `json:"up"`
	Down   int64 `json:"down"`
	Conns  int64 `json:"conns"`
	Active int64 `json:"active"`
}

func (p *Point) add(o Point) {
	p.Up += o.Up
	p.Down += o.Down
	p.Conns += o.Conns
	p.Active = max(p.Active, o.Active)
}

// Counters is the JSON shape shared by rule views, overview and stats events.
type Counters struct {
	ActiveConnections int64 `json:"activeConnections"`
	TotalConnections  int64 `json:"totalConnections"`
	BytesUp           int64 `json:"bytesUp"`
	BytesDown         int64 `json:"bytesDown"`
	RateUp            int64 `json:"rateUp"`
	RateDown          int64 `json:"rateDown"`
	Errors            int64 `json:"errors"`
	Rejected          int64 `json:"rejected"`
	Messages          int64 `json:"messages,omitempty"`
}

func CountersOf(s proxy.Snapshot, rateUp, rateDown int64) Counters {
	return Counters{
		ActiveConnections: s.Active, TotalConnections: s.Total,
		BytesUp: s.BytesUp, BytesDown: s.BytesDn,
		RateUp: rateUp, RateDown: rateDown,
		Errors: s.Errors, Rejected: s.Rejected, Messages: s.Messages,
	}
}

func (c *Counters) Add(o Counters) {
	c.ActiveConnections += o.ActiveConnections
	c.TotalConnections += o.TotalConnections
	c.BytesUp += o.BytesUp
	c.BytesDown += o.BytesDown
	c.RateUp += o.RateUp
	c.RateDown += o.RateDown
	c.Errors += o.Errors
	c.Rejected += o.Rejected
	c.Messages += o.Messages
}

const (
	realtimeLen  = 300  // 5 min at 1s
	minuteLen    = 1440 // 24h at 1m
	hourLen      = 720  // 30d at 1h
	saveInterval = 5 * time.Minute
)

// ring is a fixed-capacity series of consecutive buckets, oldest first.
type ring struct {
	pts  []Point
	head int
	n    int
}

func newRing(size int) *ring { return &ring{pts: make([]Point, size)} }

func (r *ring) push(p Point) {
	r.pts[(r.head+r.n)%len(r.pts)] = p
	if r.n < len(r.pts) {
		r.n++
	} else {
		r.head = (r.head + 1) % len(r.pts)
	}
}

func (r *ring) each(fn func(Point)) {
	for i := 0; i < r.n; i++ {
		fn(r.pts[(r.head+i)%len(r.pts)])
	}
}

// series is one rule's (or the global) traffic history.
type series struct {
	realtime, minute, hour *ring
	curMin, curHour        Point // open buckets
}

func newSeries() *series {
	return &series{realtime: newRing(realtimeLen), minute: newRing(minuteLen), hour: newRing(hourLen)}
}

// record adds one 1s sample, closing minute/hour buckets as time moves on.
func (s *series) record(p Point) {
	s.realtime.push(p)
	if m := p.T - p.T%60; m != s.curMin.T {
		if s.curMin.T != 0 {
			s.minute.push(s.curMin)
		}
		s.curMin = Point{T: m}
	}
	s.curMin.add(p)
	if h := p.T - p.T%3600; h != s.curHour.T {
		if s.curHour.T != 0 {
			s.hour.push(s.curHour)
		}
		s.curHour = Point{T: h}
	}
	s.curHour.add(p)
}

type ruleState struct {
	runner   *proxy.Runner
	prev     proxy.Snapshot
	rateUp   int64
	rateDown int64
	series   *series
}

type Sampler struct {
	mgr     *gateway.Manager
	store   *config.Store
	broker  *events.Broker
	dataDir string

	mu      sync.RWMutex
	rules   map[config.RuleID]*ruleState
	tenants map[string]*series // traffic of each tenant's rules
	global  *series
	gRateUp int64
	gRateDn int64

	// Usage receives each tenant's transferred bytes once per sample.
	Usage func(map[string]int64)
}

func New(mgr *gateway.Manager, store *config.Store, broker *events.Broker) *Sampler {
	s := &Sampler{
		mgr: mgr, store: store, broker: broker, dataDir: store.Get().DataDir,
		rules: make(map[config.RuleID]*ruleState), tenants: make(map[string]*series), global: newSeries(),
	}
	if err := s.load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("failed to load metrics history", "err", err)
	}
	return s
}

// Run samples until ctx is done, then saves history.
func (s *Sampler) Run(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	save := time.NewTicker(saveInterval)
	defer save.Stop()
	last := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			s.sample(now, now.Sub(last).Seconds())
			last = now
		case <-save.C:
			if err := s.Save(); err != nil {
				slog.Warn("failed to save metrics history", "err", err)
			}
		}
	}
}

func (s *Sampler) sample(now time.Time, elapsed float64) {
	t := now.Unix()
	runners := s.mgr.Runners()
	cfg := s.store.Get()
	ev := StatsEvent{T: t, Rules: make(map[config.RuleID]RuleStats, len(cfg.Forward))}
	var g Point
	g.T = t
	owned := map[string]*Point{}

	s.mu.Lock()
	for i := range cfg.Forward {
		r := &cfg.Forward[i]
		st := s.rules[r.ID]
		if st == nil {
			st = &ruleState{series: newSeries()}
			s.rules[r.ID] = st
		}
		rn := runners[r.ID]
		if rn != st.runner { // restarted: counters reset
			st.runner, st.prev = rn, proxy.Snapshot{}
		}
		var snap proxy.Snapshot
		if rn != nil {
			snap = rn.Snapshot()
		}
		p := Point{
			T:      t,
			Up:     delta(snap.BytesUp, st.prev.BytesUp),
			Down:   delta(snap.BytesDn, st.prev.BytesDn),
			Conns:  delta(snap.Total, st.prev.Total),
			Active: snap.Active,
		}
		st.prev = snap
		st.rateUp, st.rateDown = rate(p.Up, elapsed), rate(p.Down, elapsed)
		st.series.record(p)
		g.Up += p.Up
		g.Down += p.Down
		g.Conns += p.Conns
		g.Active += p.Active
		if r.Owner != "" {
			tp := owned[r.Owner]
			if tp == nil {
				tp = &Point{T: t}
				owned[r.Owner] = tp
			}
			tp.Up += p.Up
			tp.Down += p.Down
			tp.Conns += p.Conns
			tp.Active += p.Active
		}

		c := CountersOf(snap, st.rateUp, st.rateDown)
		ev.Totals.Add(c)
		ev.Rules[r.ID] = RuleStats{c, s.mgr.State(r), r.Owner}
	}
	usage := make(map[string]int64, len(owned))
	for tenant, p := range owned {
		se := s.tenants[tenant]
		if se == nil {
			se = newSeries()
			s.tenants[tenant] = se
		}
		se.record(*p)
		if p.Up+p.Down > 0 {
			usage[tenant] = p.Up + p.Down
		}
	}
	// Tenants without rules keep their history until the tenant is deleted.
	if cfg.Access != nil {
		for tenant := range s.tenants {
			if cfg.Access.Tenant(tenant) == nil {
				delete(s.tenants, tenant)
			}
		}
	}
	// Forget deleted rules.
	for id := range s.rules {
		if _, ok := ev.Rules[id]; !ok {
			delete(s.rules, id)
		}
	}
	s.global.record(g)
	s.gRateUp, s.gRateDn = ev.Totals.RateUp, ev.Totals.RateDown
	s.mu.Unlock()

	if s.Usage != nil && len(usage) > 0 {
		s.Usage(usage)
	}
	s.broker.Publish("stats", ev)
}

type RuleStats struct {
	Counters
	State string `json:"state"`
	Owner string `json:"-"`
}

// StatsEvent is the payload of the per-second "stats" event.
type StatsEvent struct {
	T      int64                       `json:"t"`
	Totals Counters                    `json:"totals"`
	Rules  map[config.RuleID]RuleStats `json:"rules"`
}

// ForTenant keeps only a tenant's rules and recomputes the totals.
func (e StatsEvent) ForTenant(tenant string) StatsEvent {
	out := StatsEvent{T: e.T, Rules: make(map[config.RuleID]RuleStats)}
	for id, st := range e.Rules {
		if st.Owner == tenant {
			out.Rules[id] = st
			out.Totals.Add(st.Counters)
		}
	}
	return out
}

func delta(cur, prev int64) int64 {
	if cur < prev {
		return cur
	}
	return cur - prev
}

func rate(n int64, elapsed float64) int64 {
	if elapsed <= 0 {
		return 0
	}
	return int64(float64(n) / elapsed)
}

// Rates returns the latest per-second rates of a rule.
func (s *Sampler) Rates(id config.RuleID) (up, down int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if st := s.rules[id]; st != nil && st.runner != nil {
		return st.rateUp, st.rateDown
	}
	return 0, 0
}

// Tenant series are addressed as "tenant:<id>"; rule ids cannot contain ':'.
const tenantPrefix = "tenant:"

// TenantSeries names the aggregate series of a tenant's rules.
func TenantSeries(tenant string) config.RuleID { return config.RuleID(tenantPrefix + tenant) }

func (s *Sampler) seriesFor(id config.RuleID) *series {
	if id == "" {
		return s.global
	}
	if tenant, ok := strings.CutPrefix(string(id), tenantPrefix); ok {
		return s.tenants[tenant]
	}
	if st := s.rules[id]; st != nil {
		return st.series
	}
	return nil
}

// Realtime returns the last 5 minutes at 1s resolution, zero-filled.
func (s *Sampler) Realtime(id config.RuleID) []Point {
	now := time.Now().Unix()
	s.mu.RLock()
	defer s.mu.RUnlock()
	return fill(s.seriesFor(id), now-realtimeLen+1, now, 1, func(se *series, fn func(Point)) {
		se.realtime.each(fn)
	})
}

var Ranges = map[string]struct {
	Dur  int64
	Step int64
}{
	"1h":  {3600, 60},
	"24h": {86400, 60},
	"7d":  {7 * 86400, 3600},
	"30d": {30 * 86400, 3600},
}

// History returns zero-filled buckets for the range, including the open bucket.
func (s *Sampler) History(id config.RuleID, rng string) (step int64, pts []Point, ok bool) {
	r, ok := Ranges[rng]
	if !ok {
		return 0, nil, false
	}
	now := time.Now().Unix()
	end := now - now%r.Step
	s.mu.RLock()
	defer s.mu.RUnlock()
	return r.Step, fill(s.seriesFor(id), end-r.Dur+r.Step, end, r.Step, func(se *series, fn func(Point)) {
		if r.Step == 60 {
			se.minute.each(fn)
			fn(se.curMin)
		} else {
			se.hour.each(fn)
			fn(se.curHour)
		}
	}), true
}

func fill(se *series, from, to, step int64, walk func(*series, func(Point))) []Point {
	out := make([]Point, 0, (to-from)/step+1)
	for t := from; t <= to; t += step {
		out = append(out, Point{T: t})
	}
	if se == nil {
		return out
	}
	walk(se, func(p Point) {
		if p.T >= from && p.T <= to {
			out[(p.T-from)/step].add(p)
		}
	})
	return out
}

type TopItem struct {
	RuleID string `json:"ruleId"`
	Name   string `json:"name"`
	Up     int64  `json:"up"`
	Down   int64  `json:"down"`
	Conns  int64  `json:"conns"`
}

// Top sums the traffic over the range of each rule keep accepts (all when
// nil), largest first.
func (s *Sampler) Top(rng string, keep func(*config.Rule) bool) ([]TopItem, bool) {
	if _, ok := Ranges[rng]; !ok {
		return nil, false
	}
	cfg := s.store.Get()
	items := make([]TopItem, 0, len(cfg.Forward))
	for _, r := range cfg.Forward {
		if keep != nil && !keep(&r) {
			continue
		}
		_, pts, _ := s.History(r.ID, rng)
		it := TopItem{RuleID: string(r.ID), Name: r.Name}
		for _, p := range pts {
			it.Up += p.Up
			it.Down += p.Down
			it.Conns += p.Conns
		}
		items = append(items, it)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Up+items[i].Down > items[j].Up+items[j].Down })
	return items, true
}

// GlobalRates returns the latest aggregate rates.
func (s *Sampler) GlobalRates() (up, down int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.gRateUp, s.gRateDn
}

// Persistence. Only minute/hour history is kept; realtime data is transient.

type savedSeries struct {
	Minute, Hour    []Point
	CurMin, CurHour Point
}

type savedFile struct {
	Version int
	Global  savedSeries
	Rules   map[string]savedSeries
	Tenants map[string]savedSeries
}

func (s *Sampler) path() string { return filepath.Join(s.dataDir, "metrics.gob") }

func dump(se *series) savedSeries {
	var out savedSeries
	se.minute.each(func(p Point) { out.Minute = append(out.Minute, p) })
	se.hour.each(func(p Point) { out.Hour = append(out.Hour, p) })
	out.CurMin, out.CurHour = se.curMin, se.curHour
	return out
}

func restore(sv savedSeries) *series {
	se := newSeries()
	for _, p := range sv.Minute {
		se.minute.push(p)
	}
	for _, p := range sv.Hour {
		se.hour.push(p)
	}
	se.curMin, se.curHour = sv.CurMin, sv.CurHour
	return se
}

// Save writes history atomically.
func (s *Sampler) Save() error {
	s.mu.RLock()
	f := savedFile{Version: 1, Global: dump(s.global), Rules: make(map[string]savedSeries, len(s.rules))}
	for id, st := range s.rules {
		f.Rules[string(id)] = dump(st.series)
	}
	f.Tenants = make(map[string]savedSeries, len(s.tenants))
	for id, se := range s.tenants {
		f.Tenants[id] = dump(se)
	}
	s.mu.RUnlock()

	if err := os.MkdirAll(s.dataDir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dataDir, ".metrics-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := gob.NewEncoder(tmp).Encode(&f); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path())
}

func (s *Sampler) load() error {
	fh, err := os.Open(s.path())
	if err != nil {
		return err
	}
	defer fh.Close()
	var f savedFile
	if err := gob.NewDecoder(fh).Decode(&f); err != nil {
		return err
	}
	s.global = restore(f.Global)
	for id, sv := range f.Rules {
		s.rules[config.RuleID(id)] = &ruleState{series: restore(sv)}
	}
	for id, sv := range f.Tenants {
		s.tenants[id] = restore(sv)
	}
	return nil
}
