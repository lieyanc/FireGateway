package config

import (
	"reflect"
)

// ClusterConnection is private startup configuration, never a shared rule.
type ClusterConnection struct {
	NodeID  string         `json:"nodeId"`
	Cluster *ClusterConfig `json:"cluster"`
}

func connectionOf(c *Config) ClusterConnection {
	copy := c.Clone()
	return ClusterConnection{NodeID: copy.Node.ID, Cluster: copy.Cluster}
}

// ClusterConnection returns saved settings and whether they differ from the
// running process. The caller owns the returned copy, including credentials.
func (s *Store) ClusterConnection() (ClusterConnection, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	active := s.Get()
	saved := active.Clone()
	if s.connection != nil {
		saved.Node.ID, saved.Cluster = s.connection.NodeID, s.connection.Cluster
	}
	out := connectionOf(saved)
	return out, !reflect.DeepEqual(out, connectionOf(active))
}

// UpdateClusterConnection stages a validated connection without changing the
// live node, peer credentials or rule backend. Other config writes preserve it.
func (s *Store) UpdateClusterConnection(fn func(*ClusterConnection) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	active := s.Get()
	next := active.Clone()
	if s.connection != nil {
		next.Node.ID, next.Cluster = s.connection.NodeID, s.connection.Cluster
	}
	connection := connectionOf(next)
	if err := fn(&connection); err != nil {
		return err
	}
	next.Node.ID, next.Cluster = connection.NodeID, connection.Cluster
	next.applyDefaults()
	if err := next.ValidateNode(); err != nil {
		return err
	}
	if old := active.Cluster; old != nil {
		cc := next.Cluster
		if cc == nil || next.Node.ID != active.Node.ID || cc.ID != old.ID || cc.PeerID != old.PeerID || cc.InitialWriter != old.InitialWriter {
			return &FieldError{"cluster", "an active cluster cannot be disabled or assigned different node identities or initial writer through connection settings"}
		}
	}
	// Persist the complete local config atomically, without routing through
	// write(), which still overlays the previous pending connection.
	next.Forward = nil
	if err := writeJSONFile(s.path, next); err != nil {
		return err
	}
	saved := connectionOf(next)
	s.connection = &saved
	return nil
}
