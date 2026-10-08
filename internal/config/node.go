package config

import (
	"net/netip"
	"net/url"
	"regexp"
)

// NodeConfig is private to this machine and is never included in a RuleSet.
type NodeConfig struct {
	ID            string                  `json:"id"`
	RuleOverrides map[RuleID]RuleOverride `json:"ruleOverrides"`
}

type RuleOverride struct {
	LocalHost  *string `json:"localHost,omitempty"`
	TargetHost *string `json:"targetHost,omitempty"`
	LocalPort  *int    `json:"localPort,omitempty"`
	TargetPort *int    `json:"targetPort,omitempty"`
}

// ClusterConfig is read at startup. Credentials and TLS files remain node-local.
type ClusterConfig struct {
	ID        string `json:"id"`
	PeerID    string `json:"peerId"`
	PeerToken string `json:"peerToken"`
	// PeerPort is the private node-to-node port. Each node listens on its own
	// LAN Address and dials PeerAddress, so the link bypasses the management
	// API and any reverse proxy in front of it.
	PeerPort      int      `json:"peerPort"`
	InitialWriter string   `json:"initialWriter"`
	Address       string   `json:"address"`
	PeerAddress   string   `json:"peerAddress"`
	RouterURL     string   `json:"routerUrl"`
	Username      string   `json:"username"`
	Password      string   `json:"password"`
	CAFile        string   `json:"caFile,omitempty"`
	Redirects     []string `json:"redirects"`
	PollInterval  int      `json:"pollInterval"`
	FailoverAfter int      `json:"failoverAfter"`
}

func validEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Scheme == "https" || u.Scheme == "http")
}

func (c *Config) ValidateNode() error {
	if c.Node.ID != "" {
		if err := ValidateID(RuleID(c.Node.ID)); err != nil {
			return &FieldError{"node.id", err.Error()}
		}
	}
	for id, o := range c.Node.RuleOverrides {
		if err := ValidateID(id); err != nil {
			return err
		}
		if o.LocalHost != nil && *o.LocalHost != "" {
			if _, err := netip.ParseAddr(*o.LocalHost); err != nil {
				return &FieldError{"node.ruleOverrides." + string(id) + ".localHost", "must be a local IP address (0.0.0.0 and :: are allowed)"}
			}
		}
		if o.TargetHost != nil && !validHost(*o.TargetHost) {
			return &FieldError{"node.ruleOverrides." + string(id) + ".targetHost", "invalid target host"}
		}
		if o.LocalPort != nil && !validPort(*o.LocalPort) {
			return &FieldError{"node.ruleOverrides." + string(id) + ".localPort", "port must be 1-65535"}
		}
		if o.TargetPort != nil && !validPort(*o.TargetPort) {
			return &FieldError{"node.ruleOverrides." + string(id) + ".targetPort", "port must be 1-65535"}
		}
	}
	if cc := c.Cluster; cc != nil {
		if c.Node.ID == "" {
			return &FieldError{"node.id", "required in cluster mode"}
		}
		if err := ValidateID(RuleID(cc.ID)); err != nil {
			return &FieldError{"cluster.id", err.Error()}
		}
		if !c.API.Enabled {
			return &FieldError{"api.enabled", "node replication requires the management API"}
		}
		if cc.PeerID == c.Node.ID || ValidateID(RuleID(cc.PeerID)) != nil {
			return &FieldError{"cluster.peerId", "requires a distinct peer node ID"}
		}
		if cc.InitialWriter != c.Node.ID && cc.InitialWriter != cc.PeerID {
			return &FieldError{"cluster.initialWriter", "must name one member of the pair"}
		}
		if !validPort(cc.PeerPort) || cc.PeerPort == c.API.Port {
			return &FieldError{"cluster.peerPort", "use a port 1-65535 that differs from the management API port"}
		}
		if len(cc.PeerToken) < 32 {
			return &FieldError{"cluster.peerToken", "use a shared random token of at least 32 characters"}
		}
		for field, address := range map[string]string{"address": cc.Address, "peerAddress": cc.PeerAddress} {
			ip, err := netip.ParseAddr(address)
			if err != nil || !ip.Is4() || ip.IsUnspecified() || ip.IsMulticast() {
				return &FieldError{"cluster." + field, "requires a unicast LAN IPv4 address"}
			}
		}
		if cc.Address == cc.PeerAddress {
			return &FieldError{"cluster.peerAddress", "node addresses must differ"}
		}
		if !validEndpoint(cc.RouterURL) {
			return &FieldError{"cluster.routerUrl", "use an http(s) ubus endpoint without embedded credentials"}
		}
		if cc.Username == "" || cc.Password == "" {
			return &FieldError{"cluster.username", "router credentials are required"}
		}
		if cc.PollInterval < 1 || cc.PollInterval > 5 {
			return &FieldError{"cluster.pollInterval", "must be between 1 and 5 seconds"}
		}
		if cc.FailoverAfter < 10 || cc.FailoverAfter > 300 || cc.FailoverAfter < 3*cc.PollInterval {
			return &FieldError{"cluster.failoverAfter", "must be 10–300 seconds and at least three polling intervals"}
		}
		if len(cc.Redirects) == 0 {
			return &FieldError{"cluster.redirects", "specify existing named DNAT redirects"}
		}
		seen := map[string]bool{}
		for _, name := range cc.Redirects {
			if !regexp.MustCompile(`^[a-zA-Z0-9_]+$`).MatchString(name) || seen[name] {
				return &FieldError{"cluster.redirects", "use unique stable UCI section names"}
			}
			seen[name] = true
		}

	}
	return nil
}

func (n NodeConfig) Resolve(r Rule) (Rule, error) {
	if o, ok := n.RuleOverrides[r.ID]; ok {
		if o.LocalHost != nil {
			r.LocalHost = *o.LocalHost
		}
		if o.TargetHost != nil {
			r.TargetHost = *o.TargetHost
		}
		if o.LocalPort != nil {
			if len(r.LocalPortRange) > 0 {
				return r, &FieldError{"node.ruleOverrides." + string(r.ID) + ".localPort", "cannot override a single port on a range rule"}
			}
			r.LocalPort = *o.LocalPort
		}
		if o.TargetPort != nil {
			if len(r.TargetPortRange) > 0 {
				return r, &FieldError{"node.ruleOverrides." + string(r.ID) + ".targetPort", "cannot override a single port on a range rule"}
			}
			r.TargetPort = *o.TargetPort
		}
	}
	return r, r.Validate()
}
