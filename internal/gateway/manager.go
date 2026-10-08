// Package gateway owns the set of running rules and keeps them in sync with
// the persisted configuration.
package gateway

import (
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lieyanc/FireGateway/internal/config"
	"github.com/lieyanc/FireGateway/internal/events"
	"github.com/lieyanc/FireGateway/internal/proxy"
)

var ErrNotFound = errors.New("rule not found")

// ConflictError reports a duplicate id or an overlapping listen address.
type ConflictError struct {
	Msg   string
	Rules []config.RuleID // the rules involved, if any
}

func (e *ConflictError) Error() string { return e.Msg }

const (
	StateRunning = "running"
	StatePartial = "partial"
	StateError   = "error"
	StateStopped = "stopped"
	// StateSuspended means the owning tenant exhausted its traffic quota.
	StateSuspended = "suspended"
)

type Manager struct {
	store  *config.Store
	broker *events.Broker

	mu        sync.Mutex // serializes mutations
	rmu       sync.RWMutex
	runners   map[config.RuleID]*proxy.Runner
	errs      map[config.RuleID]string // active rules that could not start at all
	clustered bool
	prepared  bool                            // clustered: runners may bind; set by the first Prepare
	serving   atomic.Bool                     // clustered: the forwarding gate, see SetServing
	suspended atomic.Pointer[map[string]bool] // tenants over their traffic quota
}

func New(store *config.Store, broker *events.Broker) *Manager {
	return &Manager{
		store: store, broker: broker, clustered: store.Get().Cluster != nil,
		runners: make(map[config.RuleID]*proxy.Runner),
		errs:    make(map[config.RuleID]string),
	}
}

// Start launches every active rule in the current config.
func (m *Manager) Start() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.clustered {
		return
	}
	m.prepared = true
	cfg := m.store.Get()
	for i := range cfg.Forward {
		r := &cfg.Forward[i]
		switch {
		case r.ID == "":
			slog.Warn("skipping invalid config entry", "index", i, "reason", "missing id")
		case m.runnable(r):
			m.start(r)
		default:
			slog.Info("skipping inactive rule", "ruleId", string(r.ID), "name", r.Name, "status", r.Status)
		}
	}
}

// Stop stops all rules without touching the config.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rmu.Lock()
	defer m.rmu.Unlock()
	for id, rn := range m.runners {
		rn.Stop()
		delete(m.runners, id)
	}
}

func (m *Manager) start(r *config.Rule) {
	if m.clustered && !m.prepared {
		return
	}
	effective, err := m.store.Get().Node.Resolve(*r)
	var rn *proxy.Runner
	if err == nil {
		rn, err = proxy.StartGuarded(&effective, m.Serving)
	}
	m.rmu.Lock()
	defer m.rmu.Unlock()
	if err != nil {
		m.errs[r.ID] = err.Error()
		slog.Error("invalid rule", "ruleId", string(r.ID), "name", r.Name, "err", err)
		return
	}
	delete(m.errs, r.ID)
	m.runners[r.ID] = rn
}

func (m *Manager) stop(id config.RuleID) {
	m.rmu.Lock()
	rn := m.runners[id]
	delete(m.runners, id)
	delete(m.errs, id)
	m.rmu.Unlock()
	if rn != nil {
		rn.Stop()
	}
}

// runnable reports whether a rule should be listening: active and not
// suspended by its tenant's quota.
func (m *Manager) runnable(r *config.Rule) bool {
	if !r.Active() {
		return false
	}
	if set := m.suspended.Load(); set != nil && r.Owner != "" {
		return !(*set)[r.Owner]
	}
	return true
}

// Suspended reports whether a tenant's rules are suspended.
func (m *Manager) Suspended(tenant string) bool {
	set := m.suspended.Load()
	return set != nil && (*set)[tenant]
}

// SetSuspended stops the rules of the given tenants, closing their
// connections, and resumes the rules of tenants no longer listed.
func (m *Manager) SetSuspended(tenants map[string]bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	before := m.suspended.Load()
	if before == nil && len(tenants) == 0 || before != nil && reflect.DeepEqual(*before, tenants) {
		return
	}
	was := make(map[config.RuleID]bool)
	rules := m.List()
	for i := range rules {
		was[rules[i].ID] = m.runnable(&rules[i])
	}
	set := maps.Clone(tenants)
	m.suspended.Store(&set)
	changed := false
	for i := range rules {
		r := &rules[i]
		switch now := m.runnable(r); {
		case was[r.ID] && !now:
			m.stop(r.ID)
			changed = true
		case !was[r.ID] && now && (!m.clustered || m.prepared):
			m.start(r)
			changed = true
		}
	}
	for t := range tenants {
		if before == nil || !(*before)[t] {
			slog.Warn("tenant traffic quota exceeded; rules suspended", "tenant", t)
		}
	}
	if before != nil {
		for t := range *before {
			if !tenants[t] {
				slog.Info("tenant rules resumed", "tenant", t)
			}
		}
	}
	if changed {
		m.publish("reloaded", "")
	}
}

// reconcile brings the runtime of one rule from old to next. Either may be
// nil (created / deleted). Policy-only changes are applied in place.
func (m *Manager) reconcile(old, next *config.Rule) {
	if next == nil || !m.runnable(next) {
		if next != nil {
			m.stop(next.ID)
		} else {
			m.stop(old.ID)
		}
		return
	}
	if m.clustered && !m.prepared {
		return
	}
	effective, err := m.store.Get().Node.Resolve(*next)
	if err != nil {
		m.stop(next.ID)
		m.rmu.Lock()
		m.errs[next.ID] = err.Error()
		m.rmu.Unlock()
		return
	}
	next = &effective
	if old != nil {
		resolved, _ := m.store.Get().Node.Resolve(*old)
		old = &resolved
	}
	rn := m.Runner(next.ID)
	if rn != nil && old != nil && old.BindKey() == next.BindKey() {
		if err := rn.Apply(next); err == nil {
			return
		}
	}
	m.stop(next.ID)
	m.start(next)
}

func (m *Manager) Runner(id config.RuleID) *proxy.Runner {
	m.rmu.RLock()
	defer m.rmu.RUnlock()
	return m.runners[id]
}

// Runners returns a snapshot of the running rules.
func (m *Manager) Runners() map[config.RuleID]*proxy.Runner {
	m.rmu.RLock()
	defer m.rmu.RUnlock()
	out := make(map[config.RuleID]*proxy.Runner, len(m.runners))
	for k, v := range m.runners {
		out[k] = v
	}
	return out
}

type Runtime struct {
	State     string
	Error     string
	Listeners int
	Failures  []proxy.Failure
	StartedAt time.Time
	Snapshot  proxy.Snapshot
}

func (m *Manager) Runtime(r *config.Rule) Runtime { return m.runtime(r, true) }

// State returns the rule's run state without sampling its counters.
func (m *Manager) State(r *config.Rule) string { return m.runtime(r, false).State }

func (m *Manager) runtime(r *config.Rule, withSnapshot bool) Runtime {
	if !r.Active() || (m.clustered && !m.Serving()) {
		return Runtime{State: StateStopped}
	}
	if !m.runnable(r) {
		return Runtime{State: StateSuspended, Error: "tenant traffic quota exceeded"}
	}
	m.rmu.RLock()
	rn, errMsg := m.runners[r.ID], m.errs[r.ID]
	m.rmu.RUnlock()
	if rn == nil {
		if errMsg == "" {
			errMsg = "not running"
		}
		return Runtime{State: StateError, Error: errMsg}
	}
	rt := Runtime{
		Listeners: rn.Listeners(), Failures: rn.Failures(),
		StartedAt: rn.StartedAt, State: StateRunning,
	}
	if withSnapshot {
		rt.Snapshot = rn.Snapshot()
	}
	switch {
	case rt.Listeners == 0:
		rt.State = StateError
	case len(rt.Failures) > 0:
		rt.State = StatePartial
	}
	return rt
}

func (m *Manager) Get(id config.RuleID) (config.Rule, bool) {
	cfg := m.store.Get()
	if i := cfg.RuleIndex(id); i >= 0 {
		return cfg.Forward[i], true
	}
	return config.Rule{}, false
}

func (m *Manager) List() []config.Rule { return m.store.Get().Forward }

func (m *Manager) publish(action string, id config.RuleID) {
	ev := map[string]string{"action": action}
	if id != "" {
		ev["id"] = string(id)
	}
	m.broker.Publish("rules", ev)
}

func nextID(c *config.Config) config.RuleID {
	maxID := 0
	for _, r := range c.Forward {
		if n, err := strconv.Atoi(string(r.ID)); err == nil && n > maxID {
			maxID = n
		}
	}
	return config.RuleID(strconv.Itoa(maxID + 1))
}

// checkConflicts rejects active rules that would bind overlapping sockets.
func checkConflicts(rules []config.Rule) error {
	for i := range rules {
		a := &rules[i]
		if !a.Active() {
			continue
		}
		for j := i + 1; j < len(rules); j++ {
			b := &rules[j]
			if b.Active() && a.ListenOverlaps(b) {
				return &ConflictError{fmt.Sprintf("rule %q listens on an address that overlaps rule %q", b.ID, a.ID), []config.RuleID{a.ID, b.ID}}
			}
		}
	}
	return nil
}

func prepare(r *config.Rule) error {
	r.Normalize()
	return r.Validate()
}

// Create adds a rule, generating an id when empty.
func (m *Manager) Create(scope Scope, r config.Rule) (config.Rule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, err := m.store.Update(func(c *config.Config) error {
		if r.ID == "" {
			r.ID = nextID(c)
		}
		if err := prepare(&r); err != nil {
			return err
		}
		if c.RuleIndex(r.ID) >= 0 {
			return &ConflictError{Msg: fmt.Sprintf("rule id %q already exists", r.ID)}
		}
		if err := scope.admit(c, &r, nil); err != nil {
			return err
		}
		c.Forward = append(c.Forward, r)
		return scope.validate(m, c)
	})
	if err != nil {
		return r, err
	}
	m.reconcile(nil, &r)
	slog.Info("rule created", "ruleId", string(r.ID), "name", r.Name)
	m.publish("created", r.ID)
	return r, nil
}

// Update replaces a rule; the id is taken from the path, not the body.
func (m *Manager) Update(scope Scope, id config.RuleID, r config.Rule) (config.Rule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r.ID = id
	if err := prepare(&r); err != nil {
		return r, err
	}
	old, err := m.replace(scope, id, func(c *config.Config, old *config.Rule) (config.Rule, error) {
		return r, scope.admit(c, &r, old)
	})
	if err != nil {
		return r, err
	}
	m.reconcile(&old, &r)
	slog.Info("rule updated", "ruleId", string(id), "name", r.Name)
	m.publish("updated", id)
	return r, nil
}

// replace swaps one rule in the config and returns the previous version.
func (m *Manager) replace(scope Scope, id config.RuleID, fn func(*config.Config, *config.Rule) (config.Rule, error)) (old config.Rule, err error) {
	_, err = m.store.Update(func(c *config.Config) error {
		i := scope.find(c, id)
		if i < 0 {
			return ErrNotFound
		}
		old = c.Forward[i]
		next, err := fn(c, &old)
		if err != nil {
			return err
		}
		c.Forward[i] = next
		return scope.validate(m, c)
	})
	return old, err
}

func (m *Manager) Delete(scope Scope, id config.RuleID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.delete(scope, id); err != nil {
		return err
	}
	m.publish("deleted", id)
	return nil
}

func (m *Manager) delete(scope Scope, id config.RuleID) error {
	var old config.Rule
	_, err := m.store.Update(func(c *config.Config) error {
		i := scope.find(c, id)
		if i < 0 {
			return ErrNotFound
		}
		old = c.Forward[i]
		c.Forward = slices.Delete(c.Forward, i, i+1)
		return nil
	})
	if err != nil {
		return err
	}
	m.reconcile(&old, nil)
	slog.Info("rule deleted", "ruleId", string(id), "name", old.Name)
	return nil
}

// SetActive enables or disables a rule.
func (m *Manager) SetActive(scope Scope, id config.RuleID, active bool) (config.Rule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.setActive(scope, id, active)
	if err == nil {
		m.publish("updated", id)
	}
	return r, err
}

func (m *Manager) setActive(scope Scope, id config.RuleID, active bool) (config.Rule, error) {
	status := config.StatusInactive
	if active {
		status = config.StatusActive
	}
	var next config.Rule
	old, err := m.replace(scope, id, func(_ *config.Config, o *config.Rule) (config.Rule, error) {
		next = *o
		next.Status = status
		return next, nil
	})
	if err != nil {
		return next, err
	}
	if old.Status != next.Status {
		m.reconcile(&old, &next)
		slog.Info("rule status changed", "ruleId", string(id), "status", status)
	}
	return next, nil
}

// Restart re-binds an active rule's listeners.
func (m *Manager) Restart(scope Scope, id config.RuleID) (config.Rule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.Get(id)
	if !ok || !scope.Owns(&r) {
		return r, ErrNotFound
	}
	m.stop(id)
	if m.runnable(&r) && (!m.clustered || m.prepared) {
		m.start(&r)
	}
	m.publish("updated", id)
	return r, nil
}

type BatchResult struct {
	OK     []string      `json:"ok"`
	Failed []BatchFailed `json:"failed"`
}

type BatchFailed struct {
	ID      string `json:"id"`
	Message string `json:"message"`
}

// Batch validates individual items, then persists all accepted changes in a
// single publication so a replicated request cannot partially commit twice.
func (m *Manager) Batch(scope Scope, action string, ids []config.RuleID) (BatchResult, error) {
	res := BatchResult{OK: []string{}, Failed: []BatchFailed{}}
	if action != "enable" && action != "disable" && action != "delete" {
		return res, &config.FieldError{Field: "action", Msg: "action must be enable, disable or delete"}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	before := m.List()
	cfg, err := m.store.Update(func(c *config.Config) error {
		for _, id := range ids {
			i := scope.find(c, id)
			if i < 0 {
				res.Failed = append(res.Failed, BatchFailed{string(id), ErrNotFound.Error()})
				continue
			}
			saved := slices.Clone(c.Forward)
			if action == "delete" {
				c.Forward = slices.Delete(slices.Clone(c.Forward), i, i+1)
			} else {
				if action == "enable" {
					c.Forward[i].Status = "active"
				} else {
					c.Forward[i].Status = "inactive"
				}
			}
			if err := scope.validate(m, c); err != nil {
				c.Forward = saved
				res.Failed = append(res.Failed, BatchFailed{string(id), err.Error()})
				continue
			}
			res.OK = append(res.OK, string(id))
		}
		return nil
	})
	if err != nil {
		return BatchResult{OK: []string{}, Failed: res.Failed}, err
	}
	m.sync(before, cfg.Forward)
	m.publish("reloaded", "")
	return res, nil
}

type SyncResult struct {
	Added   int `json:"added"`
	Updated int `json:"updated"`
	Removed int `json:"removed"`
}

// Import merges or replaces rules within the scope. All rules are validated
// before anything is applied, so a bad payload changes nothing.
func (m *Manager) Import(scope Scope, rules []config.Rule, replace bool) (SyncResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := make(map[config.RuleID]bool, len(rules))
	for i := range rules {
		r := &rules[i]
		if r.ID != "" {
			if seen[r.ID] {
				return SyncResult{}, &ConflictError{Msg: fmt.Sprintf("duplicate rule id %q in import", r.ID)}
			}
			seen[r.ID] = true
		}
	}
	var before []config.Rule
	cfg, err := m.store.Update(func(c *config.Config) error {
		before = c.Forward
		out := slices.Clone(c.Forward)
		if replace {
			// Rules outside the scope are kept, not replaced.
			out = slices.DeleteFunc(out, func(r config.Rule) bool { return scope.Owns(&r) })
		}
		tmp := &config.Config{Forward: out, Access: c.Access}
		// Ids for rules without one must not collide with existing or
		// incoming ids.
		taken := &config.Config{Forward: slices.Concat(before, rules)}
		for i, r := range rules {
			if r.ID == "" {
				r.ID = nextID(taken)
				taken.Forward = append(taken.Forward, r)
			}
			if err := prepare(&r); err != nil {
				var fe *config.FieldError
				if errors.As(err, &fe) {
					fe.Field = fmt.Sprintf("forward[%d].%s", i, fe.Field)
				}
				return err
			}
			j := tmp.RuleIndex(r.ID)
			if j >= 0 && !scope.Owns(&tmp.Forward[j]) {
				return &ConflictError{Msg: fmt.Sprintf("rule id %q is already in use", r.ID)}
			}
			var old *config.Rule
			if j >= 0 {
				old = &tmp.Forward[j]
			}
			if err := scope.admit(tmp, &r, old); err != nil {
				var fe *config.FieldError
				if errors.As(err, &fe) {
					fe.Field = fmt.Sprintf("forward[%d].%s", i, fe.Field)
				}
				return err
			}
			if j >= 0 {
				tmp.Forward[j] = r
			} else {
				tmp.Forward = append(tmp.Forward, r)
			}
		}
		c.Forward = tmp.Forward
		return scope.validate(m, c)
	})
	if err != nil {
		return SyncResult{}, err
	}
	res := m.sync(before, cfg.Forward)
	slog.Info("rules imported", "replace", replace, "added", res.Added, "updated", res.Updated, "removed", res.Removed)
	m.publish("reloaded", "")
	return res, nil
}

// Reload re-reads the config file and applies rule changes.
func (m *Manager) Reload() (SyncResult, *config.Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, cur, err := m.store.Reload()
	if err != nil {
		return SyncResult{}, nil, err
	}
	var res SyncResult
	if !reflect.DeepEqual(old.Node, cur.Node) {
		for id := range m.Runners() {
			m.stop(id)
		}
		for i := range cur.Forward {
			if m.runnable(&cur.Forward[i]) {
				m.start(&cur.Forward[i])
			}
		}
		res.Updated = len(cur.Forward)
	} else {
		res = m.sync(old.Forward, cur.Forward)
	}
	slog.Info("configuration reloaded", "added", res.Added, "updated", res.Updated, "removed", res.Removed)
	m.publish("reloaded", "")
	return res, cur, nil
}

// sync reconciles the runtime from one rule list to another.
func (m *Manager) sync(before, after []config.Rule) SyncResult {
	var res SyncResult
	prev := make(map[config.RuleID]*config.Rule, len(before))
	for i := range before {
		prev[before[i].ID] = &before[i]
	}
	// Stop removed rules first so their ports are free for new ones.
	for id, o := range prev {
		if !slices.ContainsFunc(after, func(r config.Rule) bool { return r.ID == id }) {
			m.reconcile(o, nil)
			res.Removed++
		}
	}
	for i := range after {
		r := &after[i]
		o := prev[r.ID]
		switch {
		case o == nil:
			res.Added++
		case ruleEqual(o, r):
			continue
		default:
			res.Updated++
		}
		m.reconcile(o, r)
	}
	return res
}

func ruleEqual(a, b *config.Rule) bool { return reflect.DeepEqual(a, b) }

// Connection helpers.

// visible returns the ids of the rules a scope may see; nil means all.
func (m *Manager) visible(scope Scope) map[config.RuleID]bool {
	if scope.Tenant == "" {
		return nil
	}
	out := map[config.RuleID]bool{}
	for _, r := range m.List() {
		if scope.Owns(&r) {
			out[r.ID] = true
		}
	}
	return out
}

func (m *Manager) Connections(scope Scope, rule config.RuleID) []proxy.ConnInfo {
	ids := m.visible(scope)
	var out []proxy.ConnInfo
	for id, rn := range m.Runners() {
		if (rule == "" || rule == id) && (ids == nil || ids[id]) {
			out = append(out, rn.Conns()...)
		}
	}
	slices.SortFunc(out, func(a, b proxy.ConnInfo) int { return b.StartedAt.Compare(a.StartedAt) })
	return out
}

func (m *Manager) CloseConn(scope Scope, id uint64) bool {
	ids := m.visible(scope)
	for rule, rn := range m.Runners() {
		if (ids == nil || ids[rule]) && rn.CloseConn(id) {
			return true
		}
	}
	return false
}

func (m *Manager) CloseRuleConns(scope Scope, rule config.RuleID) (int, error) {
	if r, ok := m.Get(rule); !ok || !scope.Owns(&r) {
		return 0, ErrNotFound
	}
	rn := m.Runner(rule)
	if rn == nil {
		return 0, nil
	}
	return rn.CloseAll(), nil
}
