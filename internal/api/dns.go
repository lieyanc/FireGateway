package api

import (
	"errors"
	"net/http"

	"github.com/lieyanc/FireGateway/internal/dnscache"
)

type dnsRuleRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type dnsView struct {
	dnscache.Entry
	Rules []dnsRuleRef `json:"rules"`
}

// dnsViews attaches the rules targeting each host.
func (s *Server) dnsViews(es []dnscache.Entry) []dnsView {
	byHost := make(map[string][]dnsRuleRef)
	for _, r := range s.Manager.List() {
		byHost[r.TargetHost] = append(byHost[r.TargetHost], dnsRuleRef{string(r.ID), r.Name})
	}
	out := make([]dnsView, len(es))
	for i, e := range es {
		out[i] = dnsView{Entry: e, Rules: byHost[e.Host]}
		if out[i].Rules == nil {
			out[i].Rules = []dnsRuleRef{}
		}
	}
	return out
}

func (s *Server) listDNS(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ttl":           int(dnscache.TTL.Seconds()),
		"retryInterval": int(dnscache.RetryInterval.Seconds()),
		"items":         s.dnsViews(dnscache.Default.Entries()),
	})
}

// refreshDNS re-resolves one host, or every cached host when none is given.
func (s *Server) refreshDNS(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Host string `json:"host"`
	}
	if !decode(w, r, &b, 1<<10) {
		return
	}
	var hosts []string
	if b.Host != "" {
		hosts = []string{b.Host}
	}
	es, err := dnscache.Default.Refresh(hosts...)
	if errors.Is(err, dnscache.ErrNotCached) {
		fail(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": s.dnsViews(es)})
}
