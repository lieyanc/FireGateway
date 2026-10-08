package gateway

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"reflect"

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

// Serving reports whether new connections may be forwarded. A standalone
// node always serves; a clustered node serves only while the router's DNAT
// points at it.
func (m *Manager) Serving() bool { return !m.clustered || m.serving.Load() }

// SetServing opens or closes the forwarding gate. Closing refuses new
// connections only: the router already sends new traffic elsewhere, and
// established flows finish on the node conntrack still maps them to.
func (m *Manager) SetServing(on bool) { m.serving.Store(on) }

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

// Prepare binds every runnable rule without touching the forwarding gate.
// It is idempotent: bound rules are kept and rules without any listener are
// retried. The error names rules that cannot bind; the others stay ready, so
// one bad rule never takes the whole node out of service.
func (m *Manager) Prepare() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rules := m.List()
	if err := m.validate(rules); err != nil {
		return err
	}
	m.prepared = true
	var errs []error
	for i := range rules {
		r := &rules[i]
		if !m.runnable(r) {
			continue
		}
		if rn := m.Runner(r.ID); rn == nil || rn.Listeners() == 0 {
			m.stop(r.ID)
			m.start(r)
		}
		// Partially bound rules are reported, not restarted, so retrying never
		// drops the connections their working listeners carry.
		if rn := m.Runner(r.ID); rn == nil || rn.Listeners() == 0 || len(rn.Failures()) != 0 {
			errs = append(errs, fmt.Errorf("rule %s: cannot bind all listeners on this node", r.ID))
		}
	}
	return errors.Join(errs...)
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
	// so the operator can repair them through the UI; rules that do resolve
	// keep running.
	defer m.publish("reloaded", "")
	m.sync(before, next.Rules)
	return m.validate(next.Rules)
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
	m.publish("reloaded", "")
	return nil
}
