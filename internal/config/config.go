// Package config defines the on-disk configuration and a Store that
// serializes mutations and persists them atomically.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

type Config struct {
	API     APIConfig    `json:"api"`
	Auth    AuthConfig   `json:"auth"`
	Logging LogConfig    `json:"logging"`
	Update  UpdateConfig `json:"update"`
	DataDir string       `json:"dataDir,omitempty"`
	Forward []Rule       `json:"forward"`
}

type APIConfig struct {
	Enabled    *bool  `json:"enabled,omitempty"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	EnableCors *bool  `json:"enableCors,omitempty"`
}

type AuthConfig struct {
	Username      string     `json:"username,omitempty"`
	PasswordHash  string     `json:"passwordHash,omitempty"`
	SessionSecret string     `json:"sessionSecret,omitempty"`
	SessionTTL    int        `json:"sessionTtl,omitempty"` // seconds
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
	EnableFile    *bool  `json:"enableFile,omitempty"`
	EnableConsole *bool  `json:"enableConsole,omitempty"`
	LogDir        string `json:"logDir"`
	MaxFileSize   int64  `json:"maxFileSize"`
	MaxFiles      int    `json:"maxFiles"`
}

type UpdateConfig struct {
	Enabled       bool   `json:"enabled"`
	Channel       string `json:"channel,omitempty"`
	CheckInterval int    `json:"checkInterval,omitempty"` // seconds
	Source        string `json:"source,omitempty"`
	ProxyBaseURL  string `json:"proxyBaseUrl,omitempty"`
	Repo          string `json:"repo,omitempty"`
}

const DefaultSessionTTL = 7 * 24 * 3600

// Default returns the configuration written when no config file exists.
func Default() *Config {
	c := &Config{Forward: []Rule{}}
	c.applyDefaults()
	return c
}

// applyDefaults fills zero values; defaults mirror the legacy Node implementation.
func (c *Config) applyDefaults() {
	if c.API.Host == "" {
		c.API.Host = envOr("API_HOST", "127.0.0.1")
	}
	if c.API.Port == 0 {
		c.API.Port, _ = strconv.Atoi(envOr("API_PORT", "8080"))
	}
	if c.Logging.Level == "" {
		c.Logging.Level = envOr("LOG_LEVEL", "info")
	}
	if c.Logging.LogDir == "" {
		c.Logging.LogDir = "./logs"
	}
	if c.Logging.MaxFileSize <= 0 {
		c.Logging.MaxFileSize = 10 << 20
	}
	if c.Logging.MaxFiles <= 0 {
		c.Logging.MaxFiles = 5
	}
	if c.Update.Channel == "" {
		c.Update.Channel = "stable"
	}
	if c.Update.CheckInterval <= 0 {
		c.Update.CheckInterval = 3600
	}
	if c.Update.Source == "" {
		c.Update.Source = "github"
	}
	if c.Update.Repo == "" {
		c.Update.Repo = "lieyanc/FireGateway"
	}
	if c.DataDir == "" {
		c.DataDir = "./data"
	}
	if c.Auth.SessionTTL <= 0 {
		c.Auth.SessionTTL = DefaultSessionTTL
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

func parse(data []byte) (*Config, error) {
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	if c.Forward == nil {
		return nil, errors.New("invalid config format: 'forward' array not found")
	}
	c.applyDefaults()
	return &c, nil
}

// Store owns the current configuration and its file. All mutations go
// through Update, which validates, persists atomically, then publishes.
type Store struct {
	path string
	mu   sync.Mutex // serializes writers
	cur  *Config
	rw   sync.RWMutex
}

// Open loads path, creating it with defaults when missing. created reports
// whether a new file was written.
func Open(path string) (s *Store, created bool, err error) {
	s = &Store{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		s.cur = Default()
		return s, true, s.write(s.cur)
	}
	if err != nil {
		return nil, false, err
	}
	if s.cur, err = parse(data); err != nil {
		return nil, false, err
	}
	return s, false, nil
}

func (s *Store) Path() string { return s.path }

// Get returns the current config. Callers must treat it as read-only.
func (s *Store) Get() *Config {
	s.rw.RLock()
	defer s.rw.RUnlock()
	return s.cur
}

// Update applies fn to a copy of the config and persists the result. If fn
// or the write fails, the current config is left unchanged.
func (s *Store) Update(fn func(c *Config) error) (*Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.Get().Clone()
	if err := fn(next); err != nil {
		return nil, err
	}
	next.applyDefaults()
	if err := s.write(next); err != nil {
		return nil, fmt.Errorf("save config: %w", err)
	}
	s.rw.Lock()
	s.cur = next
	s.rw.Unlock()
	return next, nil
}

// Reload re-reads the file from disk and returns the previous and new config.
func (s *Store) Reload() (old, cur *Config, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, nil, err
	}
	next, err := parse(data)
	if err != nil {
		return nil, nil, err
	}
	s.rw.Lock()
	old, s.cur = s.cur, next
	s.rw.Unlock()
	return old, next, nil
}

// write saves c via a temp file and rename so readers never see a torn file.
// The file holds credential hashes, so it is kept private.
func (s *Store) write(c *Config) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func BoolOr(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}

func Bool(b bool) *bool { return &b }
