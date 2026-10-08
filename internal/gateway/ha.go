package gateway

import (
	"fmt"
	"net"
	"net/netip"
	"reflect"
	"time"

	"github.com/lieyanc/FireGateway/internal/config"
)

// ValidateIngress ensures the router's configured destination really reaches
// this machine and each effective listener, before changing any DNAT rule.
func (m *Manager) ValidateIngress(address string, rules []config.Rule) error {
	ip, err := netip.ParseAddr(address)
	if err != nil || !ip.Is4() {
		return fmt.Errorf("router did not provide this node's LAN IPv4 address")
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return err
	}
	local := false
	for _, addr := range addrs {
		if prefix, err := netip.ParsePrefix(addr.String()); err == nil && (prefix.Addr().Unmap() == ip || (ip.IsLoopback() && prefix.Addr().IsLoopback() && prefix.Contains(ip))) {
			local = true
		}
	}
	if !local {
		return fmt.Errorf("router destination %s is not assigned to this node", address)
	}
	for _, rule := range rules {
		if !rule.Active() {
			continue
		}
		effective, err := m.store.Get().Node.Resolve(rule)
		if err != nil {
			return err
		}
		if effective.LocalPort != rule.LocalPort {
			return fmt.Errorf("rule %s: OpenWrt switches IP only; keep the shared listen port and override targetPort for local services", rule.ID)
		}
		host := effective.LocalHost
		if host != "" && host != "0.0.0.0" && host != "::" && host != address {
			return fmt.Errorf("rule %s listens on %s but the router sends traffic to %s; set this node's localHost override", rule.ID, host, address)
		}
	}
	return nil
}

func (m *Manager) Serving() bool {
	if !m.clustered {
		return true
	}
	deadline := m.leaseUntil.Load()
	return deadline != nil && time.Now().Before(*deadline)
}
func (m *Manager) Activate(until time.Time, expected ...string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(expected) > 0 && m.store.Rules().Snapshot().Checksum != expected[0] {
		return false
	}
	if len(expected) > 0 {
		for _, rule := range m.List() {
			if m.runnable(&rule) {
				rn := m.Runner(rule.ID)
				if rn == nil || rn.Listeners() == 0 || len(rn.Failures()) > 0 {
					return false
				}
			}
		}
	}
	m.leaseUntil.Store(&until)
	return true
}
func (m *Manager) LeaseExpired() bool {
	deadline := m.leaseUntil.Load()
	return deadline != nil && !time.Now().Before(*deadline)
}
func (m *Manager) AppliedRevision() int64 { return m.applied.Load() }

// Demote closes the gate before waiting for any configuration mutation.
func (m *Manager) Demote() {
	m.leaseUntil.Store(nil)
	for _, rn := range m.Runners() {
		rn.CloseAll()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prepared = false
	for id := range m.Runners() {
		m.stop(id)
	}
	m.applied.Store(0)
}

func (m *Manager) validate(rules []config.Rule) error {
	node := m.store.Get().Node
	effective := make([]config.Rule, len(rules))
	for i, r := range rules {
		var err error
		effective[i], err = node.Resolve(r)
		if err != nil {
			return err
		}
	}
	return checkConflicts(effective)
}

// Prepare binds every active listener with its forwarding gate still closed.
// A partial port-range bind makes the entire node ineligible for takeover.
func (m *Manager) Prepare() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rules := m.List()
	if err := m.validate(rules); err != nil {
		return err
	}
	active := 0
	for _, r := range rules {
		if r.Active() {
			active++
		}
	}
	if m.clustered && active == 0 {
		return fmt.Errorf("no active forwarding rules; add or enable a shared rule before takeover")
	}
	m.prepared = true
	for i := range rules {
		r := &rules[i]
		if !m.runnable(r) {
			continue
		}
		if m.Runner(r.ID) == nil {
			m.start(r)
		}
		rn := m.Runner(r.ID)
		if rn == nil || rn.Listeners() == 0 || len(rn.Failures()) != 0 {
			m.leaseUntil.Store(nil)
			m.prepared = false
			for id := range m.Runners() {
				m.stop(id)
			}
			m.applied.Store(0)
			return fmt.Errorf("rule %s: cannot bind all listeners on this node", r.ID)
		}
	}
	m.applied.Store(m.store.Rules().Snapshot().Revision)
	return nil
}

func (m *Manager) InstallRules(next config.RuleSet) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	before := m.List()
	if err := m.store.Rules().Install(next); err != nil {
		return err
	}
	// Keep desired rules visible even when this node's overrides are invalid,
	// so the operator can repair them through the UI while forwarding is off.
	defer m.publish("reloaded", "")
	if err := m.validate(next.Rules); err != nil {
		return err
	}

	m.sync(before, next.Rules)
	return nil
}

func (m *Manager) RestoreRules(next config.RuleSet) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	before := m.List()
	if err := m.store.Rules().Restore(next); err != nil {
		return err
	}
	// Keep desired rules visible even when this node's overrides are invalid,
	// so the operator can repair them through the UI while forwarding is off.
	defer m.publish("reloaded", "")
	if err := m.validate(next.Rules); err != nil {
		return err
	}

	m.sync(before, next.Rules)
	return nil
}

// UpdateNode changes only this node's address overrides, then reapplies them.
func (m *Manager) UpdateNode(node config.NodeConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	before := m.store.Get()
	next := before.Clone()
	next.Node = node
	if err := next.ValidateNode(); err != nil {
		return err
	}
	effective := make([]config.Rule, len(next.Forward))
	for i, r := range next.Forward {
		var err error
		effective[i], err = node.Resolve(r)
		if err != nil {
			return err
		}
	}
	if err := checkConflicts(effective); err != nil {
		return err
	}
	if _, err := m.store.Update(func(c *config.Config) error { c.Node = node; return nil }); err != nil {
		return err
	}
	if m.clustered {
		m.leaseUntil.Store(nil)
		m.prepared = false
		for _, rn := range m.Runners() {
			rn.CloseAll()
		}
	}
	for i, r := range next.Forward {
		old, _ := before.Node.Resolve(r)
		if reflect.DeepEqual(old, effective[i]) {
			continue
		}
		m.stop(r.ID)
		if m.runnable(&r) {
			m.start(&r)
		}
	}
	m.applied.Store(0)
	m.publish("reloaded", "")
	return nil
}
