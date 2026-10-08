package api

import (
	"net"
	"net/http"
	"sort"

	"github.com/lieyanc/FireGateway/internal/config"
	"github.com/lieyanc/FireGateway/internal/gateway"
	"github.com/lieyanc/FireGateway/internal/ha"
)

func (s *Server) clusterStatus(w http.ResponseWriter, _ *http.Request) {
	if s.Cluster != nil {
		writeJSON(w, http.StatusOK, s.Cluster.Status())
		return
	}
	rules := s.Store.Rules().Snapshot()
	writeJSON(w, http.StatusOK, ha.Status{Role: "standalone", NodeID: s.Store.Get().Node.ID, DesiredRevision: rules.Revision, Checksum: rules.Checksum})
}

func (s *Server) clusterBootstrap(w http.ResponseWriter, _ *http.Request) {
	if s.Cluster == nil {
		fail(w, http.StatusBadRequest, "bad_request", "cluster mode is not configured")
		return
	}
	if err := s.Cluster.Bootstrap(); err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"initialized": true})
}

func (s *Server) ready(w http.ResponseWriter, _ *http.Request) {
	ready := s.Manager.Serving()
	active := 0
	for _, rule := range s.Manager.List() {
		if rule.Active() {
			active++
			if st := s.Manager.State(&rule); st != gateway.StateRunning && st != gateway.StateSuspended {
				ready = false
			}
		}
	}
	ready = ready && active > 0
	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]bool{"ready": ready})
}

func (s *Server) getNode(w http.ResponseWriter, _ *http.Request) {
	addresses := []string{}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, addr := range addrs {
			if ip, _, err := net.ParseCIDR(addr.String()); err == nil {
				addresses = append(addresses, ip.String())
			}
		}
	}
	sort.Strings(addresses)
	node := s.Store.Get().Node
	type effectiveRule struct {
		ID string `json:"id"`
		config.Rule
	}
	effective := []effectiveRule{}
	for _, rule := range s.Manager.List() {
		if r, err := node.Resolve(rule); err == nil {
			effective = append(effective, effectiveRule{ID: string(r.ID), Rule: r})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"node": node, "addresses": addresses, "effectiveRules": effective, "rulesFile": s.Store.Rules().Path()})
}

func (s *Server) putNode(w http.ResponseWriter, r *http.Request) {
	var node config.NodeConfig
	if !decode(w, r, &node, 256<<10) {
		return
	}
	if err := s.Manager.UpdateNode(node); err != nil {
		failErr(w, err)
		return
	}
	s.getNode(w, r)
}

func (s *Server) clusterAction(w http.ResponseWriter, r *http.Request) {
	if s.Cluster == nil {
		fail(w, 400, "bad_request", "cluster mode is not configured")
		return
	}
	action := r.PathValue("action")
	var body struct {
		FencedPeer   bool `json:"fencedPeer"`
		ArchiveLocal bool `json:"archiveLocal"`
	}
	if action == "promote" || action == "rejoin" {
		if !decode(w, r, &body, 4096) {
			return
		}
	}
	var err error
	if action == "rearm" {
		err = s.Cluster.Rearm(r.Context())
	} else if action == "retry-switch" {
		err = s.Cluster.RetrySwitch(r.Context())
	} else {
		err = s.Cluster.Action(r.Context(), action, (action == "promote" && body.FencedPeer) || (action == "rejoin" && body.ArchiveLocal))
	}
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, 200, s.Cluster.Status())
}
func (s *Server) clusterTransaction(w http.ResponseWriter, r *http.Request) {
	if s.Cluster == nil {
		fail(w, 404, "not_found", "cluster mode is not configured")
		return
	}
	value, ok := s.Cluster.Transaction(r.PathValue("id"))
	if !ok {
		fail(w, 404, "not_found", "transaction not retained; refresh the rule snapshot")
		return
	}
	writeJSON(w, 200, value)
}

func (s *Server) clusterSnapshot(w http.ResponseWriter, r *http.Request) {
	if s.Cluster == nil {
		fail(w, 400, "bad_request", "cluster is not configured")
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="firegateway-shared-rules.json"`)
	writeJSON(w, 200, s.Cluster.Snapshot())
}
