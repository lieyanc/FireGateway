package main

import (
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync/atomic"
)

type Proxy interface {
	Stats() ProxyStats
	Close() error
}

type ProxyStats struct {
	ID                string `json:"id"`
	Type              string `json:"type"`
	RuleID            RuleID `json:"ruleId"`
	Name              string `json:"name,omitempty"`
	Listen            string `json:"listen"`
	Target            string `json:"target"`
	TotalConnections  int64  `json:"totalConnections"`
	ActiveConnections int64  `json:"activeConnections"`
	Errors            int64  `json:"errors"`
	MessagesForwarded int64  `json:"messagesForwarded,omitempty"`
	BytesUpstream     int64  `json:"bytesUpstream"`
	BytesDownstream   int64  `json:"bytesDownstream"`
}

// base holds identity and lock-free counters shared by TCP and UDP proxies.
type base struct {
	id, typ   string
	rule      *Rule
	listen    string
	target    string
	log       *slog.Logger
	total     atomic.Int64
	active    atomic.Int64
	errors    atomic.Int64
	messages  atomic.Int64
	bytesUp   atomic.Int64
	bytesDown atomic.Int64
}

func newBase(typ string, r *Rule, idx int, m Mapping) base {
	id := fmt.Sprintf("%s_%s_%d", typ, r.ID, idx)
	listen := net.JoinHostPort(r.LocalHost, strconv.Itoa(m.Local))
	target := net.JoinHostPort(r.TargetHost, strconv.Itoa(m.Target))
	return base{
		id: id, typ: typ, rule: r, listen: listen, target: target,
		log: slog.With("proxy", id, "ruleId", string(r.ID), "listen", listen, "target", target),
	}
}

func (b *base) Stats() ProxyStats {
	return ProxyStats{
		ID:                b.id,
		Type:              b.typ,
		RuleID:            b.rule.ID,
		Name:              b.rule.Name,
		Listen:            b.listen,
		Target:            b.target,
		TotalConnections:  b.total.Load(),
		ActiveConnections: b.active.Load(),
		Errors:            b.errors.Load(),
		MessagesForwarded: b.messages.Load(),
		BytesUpstream:     b.bytesUp.Load(),
		BytesDownstream:   b.bytesDown.Load(),
	}
}

// startRule starts one listener per port mapping of an active rule.
func startRule(r *Rule) []Proxy {
	log := slog.With("ruleId", string(r.ID), "name", r.Name, "type", r.Type)
	if r.Type != "tcp" && r.Type != "udp" {
		log.Error("invalid server type", "validTypes", "tcp,udp")
		return nil
	}
	mappings, err := r.Mappings()
	if err != nil {
		log.Error("invalid rule", "err", err)
		return nil
	}
	var out []Proxy
	for i, m := range mappings {
		var (
			p   Proxy
			err error
		)
		if r.Type == "tcp" {
			p, err = startTCP(r, i, m)
		} else {
			p, err = startUDP(r, i, m)
		}
		if err != nil {
			log.Error("failed to start proxy", "localPort", m.Local, "targetPort", m.Target, "err", err)
			continue
		}
		out = append(out, p)
	}
	return out
}
