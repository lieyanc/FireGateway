// Package config defines the on-disk configuration and a Store that
// serializes mutations and persists them atomically.
package config

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"time"
)

// template is the complete default configuration. It is written out when
// no config file exists and fills in fields missing from an existing one.
//
//go:embed template.json
var template []byte

type Config struct {
	API       APIConfig      `json:"api"`
	Auth      AuthConfig     `json:"auth"`
	Logging   LogConfig      `json:"logging"`
	Update    UpdateConfig   `json:"update"`
	DataDir   string         `json:"dataDir"`
	Forward   []Rule         `json:"forward,omitempty"` // legacy import and in-memory compatibility view
	RulesFile string         `json:"rulesFile"`
	Node      NodeConfig     `json:"node"`
	Cluster   *ClusterConfig `json:"cluster,omitempty"`
}

type APIConfig struct {
	Enabled    bool   `json:"enabled"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	EnableCors bool   `json:"enableCors"`
}

type AuthConfig struct {
	Username      string     `json:"username,omitempty"`
	PasswordHash  string     `json:"passwordHash,omitempty"`
	SessionSecret string     `json:"sessionSecret,omitempty"`
	SessionTTL    int        `json:"sessionTtl"` // seconds
	Tokens        []APIToken `json:"tokens,omitempty"`
}

type APIToken struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Prefix    string    `json:"prefix"`
	Hash      string    `json:"hash"` // hex sha256 of the full token
	CreatedAt time.Time `json:"createdAt"`
}

type LogConfig struct {
	Level         string `json:"level"`
	EnableFile    bool   `json:"enableFile"`
	EnableConsole bool   `json:"enableConsole"`
	LogDir        string `json:"logDir"`
	MaxFileSize   int64  `json:"maxFileSize"`
	MaxFiles      int    `json:"maxFiles"`
}

type UpdateConfig struct {
	Enabled       bool   `json:"enabled"`
	Channel       string `json:"channel"`
	CheckInterval int    `json:"checkInterval"` // seconds
	Source        string `json:"source"`
	ProxyBaseURL  string `json:"proxyBaseUrl"`
	Repo          string `json:"repo"`
}

// Default returns the embedded template. The legacy API_HOST, API_PORT and
// LOG_LEVEL environment variables override it, so they end up in files that
// are created or completed while they are set.
func Default() *Config {
	var c Config
	if err := json.Unmarshal(template, &c); err != nil {
		panic("config template: " + err.Error())
	}
	if v := os.Getenv("API_HOST"); v != "" {
		c.API.Host = v
	}
	if p, err := strconv.Atoi(os.Getenv("API_PORT")); err == nil && p > 0 {
		c.API.Port = p
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		c.Logging.Level = v
	}
	return &c
}

// applyDefaults resets fields that must not be empty to their defaults.
func (c *Config) applyDefaults() {
	d := Default()
	if c.API.Host == "" {
		c.API.Host = d.API.Host
	}
	if c.API.Port == 0 {
		c.API.Port = d.API.Port
	}
	if c.Logging.Level == "" {
		c.Logging.Level = d.Logging.Level
	}
	if c.Logging.LogDir == "" {
		c.Logging.LogDir = d.Logging.LogDir
	}
	if c.Logging.MaxFileSize <= 0 {
		c.Logging.MaxFileSize = d.Logging.MaxFileSize
	}
	if c.Logging.MaxFiles <= 0 {
		c.Logging.MaxFiles = d.Logging.MaxFiles
	}
	if c.Update.Channel == "" {
		c.Update.Channel = d.Update.Channel
	}
	if c.Update.CheckInterval <= 0 {
		c.Update.CheckInterval = d.Update.CheckInterval
	}
	if c.Update.Source == "" {
		c.Update.Source = d.Update.Source
	}
	if c.Update.Repo == "" {
		c.Update.Repo = d.Update.Repo
	}
	if c.DataDir == "" {
		c.DataDir = d.DataDir
	}
	if c.Auth.SessionTTL <= 0 {
		c.Auth.SessionTTL = d.Auth.SessionTTL
	}
	if c.RulesFile == "" {
		c.RulesFile = "rules.json"
	}
	if c.Node.RuleOverrides == nil {
		c.Node.RuleOverrides = map[RuleID]RuleOverride{}
	}
	if c.Cluster != nil {
		if c.Cluster.PollInterval == 0 {
			c.Cluster.PollInterval = 2
		}
		if c.Cluster.FailoverAfter == 0 {
			c.Cluster.FailoverAfter = 10
		}
	}
	if c.Forward == nil {
		c.Forward = []Rule{}
	}
}

// Clone returns a deep copy.
func (c *Config) Clone() *Config {
	b, err := json.Marshal(c)
	if err != nil {
		panic(err)
	}
	var out Config
	if err := json.Unmarshal(b, &out); err != nil {
		panic(err)
	}
	return &out
}

// Rule returns the index of the rule with the given id, or -1.
func (c *Config) RuleIndex(id RuleID) int {
	for i := range c.Forward {
		if c.Forward[i].ID == id {
			return i
		}
	}
	return -1
}

// parse decodes data over the defaults, so fields absent from the file keep
// their default values. incomplete reports whether any were absent.
func parse(data []byte) (c *Config, incomplete bool, err error) {
	c = Default()
	if err := json.Unmarshal(data, c); err != nil {
		return nil, false, err
	}
	c.applyDefaults()
	var want, have map[string]any
	if err := json.Unmarshal(template, &want); err != nil {
		panic("config template: " + err.Error())
	}
	if err := json.Unmarshal(data, &have); err != nil {
		return nil, false, err
	}
	return c, missingKeys(want, have), nil
}

// missingKeys reports whether have lacks (or nulls) any key of want,
// descending into nested objects.
func missingKeys(want, have map[string]any) bool {
	for k, w := range want {
		h, ok := have[k]
		if !ok || h == nil {
			return true
		}
		wm, wok := w.(map[string]any)
		hm, hok := h.(map[string]any)
		if wok && hok && missingKeys(wm, hm) {
			return true
		}
	}
	return false
}

// Store owns the current configuration and its file. All mutations go
// through Update, which validates, persists atomically, then publishes.
type Store struct {
	path  string
	rules *RuleStore
	mu    sync.Mutex // serializes writers
	cur   *Config
	rw    sync.RWMutex
	// Saved connection changes remain separate from the running controller.
	connection *ClusterConnection // protected by mu; applied on process restart
}

// OpenState reports what Open did to the config file.
type OpenState int

const (
	Loaded    OpenState = iota // read unchanged
	Created                    // did not exist; written from the template
	Completed                  // missing fields filled in from the template and saved
)

// Open loads path. A missing file is created from the template, and a file
// lacking fields is completed from it and rewritten.
func Open(path string) (*Store, OpenState, error) {
	s := &Store{path: path}
	state := Loaded
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		s.cur, state = Default(), Created
	} else if err != nil {
		return nil, Loaded, err
	} else {
		var incomplete bool
		s.cur, incomplete, err = parse(data)
		if err != nil {
			return nil, Loaded, err
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(data, &fields)
		if _, legacy := fields["forward"]; incomplete || legacy {
			state = Completed
		}
	}
	s.cur.applyDefaults()
	if err := s.cur.ValidateNode(); err != nil {
		return nil, Loaded, err
	}
	rulesPath := s.cur.RulesFile
	if !filepath.IsAbs(rulesPath) {
		rulesPath = filepath.Join(filepath.Dir(path), rulesPath)
	}
	configAbs, _ := filepath.Abs(path)
	rulesAbs, _ := filepath.Abs(rulesPath)
	if configAbs == rulesAbs {
		return nil, Loaded, errors.New("rulesFile must differ from the node config file")
	}
	clusterID := ""
	if s.cur.Cluster != nil {
		clusterID = s.cur.Cluster.ID
	}
	s.rules, err = openRules(rulesPath, s.cur.Forward, clusterID)
	if err != nil {
		return nil, Loaded, fmt.Errorf("open rules: %w", err)
	}
	s.cur.Forward = nil
	if state != Loaded {
		if err := s.write(s.cur); err != nil {
			return nil, Loaded, err
		}
	}
	return s, state, nil
}

func (s *Store) Rules() *RuleStore { return s.rules }

func (s *Store) Path() string { return s.path }

// Get returns the current config. Callers must treat it as read-only.
func (s *Store) Get() *Config {
	s.rw.RLock()
	out := *s.cur
	s.rw.RUnlock()
	if s.rules != nil {
		out.Forward = s.rules.Snapshot().Rules
	}
	return &out
}

// Update applies fn to a copy of the config and persists the result. If fn
// or the write fails, the current config is left unchanged.
func (s *Store) Update(fn func(c *Config) error) (*Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.rules.Snapshot()
	before := s.Get().Clone()
	before.Forward = previous.Rules
	next := before.Clone()
	if err := fn(next); err != nil {
		return nil, err
	}
	next.applyDefaults()
	if err := next.ValidateNode(); err != nil {
		return nil, err
	}
	if next.RulesFile != before.RulesFile || !reflect.DeepEqual(next.Cluster, before.Cluster) || next.Node.ID != before.Node.ID {
		return nil, &FieldError{"node.id", "use cluster connection settings for node identity and cluster changes; a restart is required"}
	}
	changedRules := !reflect.DeepEqual(before.Forward, next.Forward)
	rules := next.Forward
	before.Forward, next.Forward = nil, nil
	changedLocal := !reflect.DeepEqual(before, next)
	if changedRules && changedLocal {
		return nil, errors.New("update node settings and shared rules separately")
	}
	if changedRules {
		if _, err := s.rules.Update(previous, rules); err != nil {
			return nil, err
		}
	}
	if changedLocal {
		if err := s.write(next); err != nil {
			return nil, fmt.Errorf("save config: %w", err)
		}
		s.rw.Lock()
		s.cur = next
		s.rw.Unlock()
	}
	return s.Get(), nil
}

// Reload re-reads the file from disk and returns the previous and new config.
func (s *Store) Reload() (old, cur *Config, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, nil, err
	}
	next, _, err := parse(data)
	if err != nil {
		return nil, nil, err
	}
	old = s.Get()
	if next.RulesFile != old.RulesFile || !reflect.DeepEqual(next.Cluster, old.Cluster) || next.Node.ID != old.Node.ID {
		return nil, nil, errors.New("rulesFile, node.id or cluster changed; restart required")
	}
	if len(next.Forward) != 0 {
		return nil, nil, errors.New("forward has moved to rulesFile; use the rule management API")
	}
	if err := next.ValidateNode(); err != nil {
		return nil, nil, err
	}
	if old.Cluster == nil {
		if err := s.rules.Reload(); err != nil {
			return nil, nil, err
		}
	}
	next.Forward = nil
	s.rw.Lock()
	s.cur = next
	s.rw.Unlock()
	s.connection = nil
	return old, s.Get(), nil
}

func (s *Store) write(c *Config) error {
	out := *c
	if s.connection != nil {
		out.Node.ID = s.connection.NodeID
		out.Cluster = s.connection.Cluster
	}
	out.Forward = nil
	if err := out.ValidateNode(); err != nil {
		return err
	}
	return writeJSONFile(s.path, &out)
}
