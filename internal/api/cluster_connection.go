package api

import (
	"context"
	"net/http"

	"github.com/lieyanc/FireGateway/internal/config"
	"github.com/lieyanc/FireGateway/internal/ha"
)

func (s *Server) getClusterConnection(w http.ResponseWriter, _ *http.Request) {
	connection, pending := s.Store.ClusterConnection()
	var tokenSet, passwordSet bool
	if cc := connection.Cluster; cc != nil {
		tokenSet, passwordSet = cc.PeerToken != "", cc.Password != ""
		cc.PeerToken, cc.Password = "", ""
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, struct {
		config.ClusterConnection
		PeerTokenConfigured      bool `json:"peerTokenConfigured"`
		RouterPasswordConfigured bool `json:"routerPasswordConfigured"`
		RestartRequired          bool `json:"restartRequired"`
		IdentityLocked           bool `json:"identityLocked"`
	}{connection, tokenSet, passwordSet, pending, s.Store.Get().Cluster != nil})
}

func (s *Server) putClusterConnection(w http.ResponseWriter, r *http.Request) {
	var body config.ClusterConnection
	if !decode(w, r, &body, 32<<10) {
		return
	}
	err := s.Store.UpdateClusterConnection(func(saved *config.ClusterConnection) error {
		if cc := body.Cluster; cc != nil {
			// Secrets are write-only in the management API. Empty or omitted
			// fields retain the latest saved values, including pending edits.
			if previous := saved.Cluster; previous != nil {
				if cc.PeerToken == "" {
					cc.PeerToken = previous.PeerToken
				}
				if cc.Password == "" {
					cc.Password = previous.Password
				}
			}
			// Check local trust files before saving something that would make
			// the next process fail to start. Constructors perform no I/O to peers.
			if _, err := ha.NewPeerClient(*cc, body.NodeID); err != nil {
				return &config.FieldError{Field: "cluster.peerCaFile", Msg: "peer CA file must contain readable PEM certificates"}
			}
			if _, err := ha.NewClient(*cc); err != nil {
				return &config.FieldError{Field: "cluster.caFile", Msg: "router CA file must contain readable PEM certificates"}
			}
		}
		*saved = body
		return nil
	})
	if err != nil {
		failErr(w, err)
		return
	}
	s.getClusterConnection(w, r)
}

// Test the saved peer settings, including pending credentials, without pairing,
// publishing rules, applying settings, or contacting the upstream router.
func (s *Server) testPeerConnection(w http.ResponseWriter, r *http.Request) {
	connection, _ := s.Store.ClusterConnection()
	if connection.Cluster == nil {
		fail(w, http.StatusBadRequest, "bad_request", "save the cluster connection settings first")
		return
	}
	client, err := ha.NewPeerClient(*connection.Cluster, connection.NodeID)
	if err != nil {
		fail(w, http.StatusBadRequest, "validation", "peer CA file must contain readable PEM certificates")
		return
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(r.Context(), ha.PeerTimeout)
	defer cancel()
	result, err := client.Call(ctx, "heartbeat", ha.PeerRequest{})
	if err != nil {
		fail(w, http.StatusBadGateway, "peer_unavailable", err.Error())
		return
	}
	if result.State == nil || result.State.NodeID != connection.Cluster.PeerID {
		fail(w, http.StatusBadGateway, "peer_unavailable", "peer did not return its expected node identity")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"connected": true, "nodeId": result.State.NodeID})
}
