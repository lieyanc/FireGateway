package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	API     APIConfig `json:"api"`
	Logging LogConfig `json:"logging"`
	Forward []Rule    `json:"forward"`
}

type APIConfig struct {
	Enabled    *bool  `json:"enabled"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	EnableCors *bool  `json:"enableCors"`
}

type LogConfig struct {
	Level         string `json:"level"`
	EnableFile    *bool  `json:"enableFile"`
	EnableConsole *bool  `json:"enableConsole"`
	LogDir        string `json:"logDir"`
	MaxFileSize   int64  `json:"maxFileSize"`
	MaxFiles      int    `json:"maxFiles"`
}

type Rule struct {
	ID              RuleID `json:"id"`
	Name            string `json:"name"`
	Type            string `json:"type"`
	Status          string `json:"status"`
	LocalHost       string `json:"localHost"`
	LocalPort       int    `json:"localPort"`
	TargetHost      string `json:"targetHost"`
	TargetPort      int    `json:"targetPort"`
	LocalPortRange  []int  `json:"localPortRange"`
	TargetPortRange []int  `json:"targetPortRange"`
}

// RuleID accepts both numeric and string ids, as the legacy config did.
type RuleID string

func (id *RuleID) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*id = RuleID(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("id must be a string or number: %s", b)
	}
	*id = RuleID(n.String())
	return nil
}

// Mapping is one local port forwarded to one target port.
type Mapping struct {
	Local, Target int
}

// Mappings expands a rule into its port pairs; ranges take precedence over single ports.
func (r *Rule) Mappings() ([]Mapping, error) {
	if r.LocalPortRange != nil && r.TargetPortRange != nil {
		lr, tr := r.LocalPortRange, r.TargetPortRange
		if len(lr) != 2 || len(tr) != 2 || lr[0] > lr[1] || tr[0] > tr[1] || lr[1]-lr[0] != tr[1]-tr[0] {
			return nil, fmt.Errorf("invalid port range local=%v target=%v", lr, tr)
		}
		if !validPort(lr[0]) || !validPort(lr[1]) || !validPort(tr[0]) || !validPort(tr[1]) {
			return nil, fmt.Errorf("port out of range local=%v target=%v", lr, tr)
		}
		m := make([]Mapping, 0, lr[1]-lr[0]+1)
		for i := 0; i <= lr[1]-lr[0]; i++ {
			m = append(m, Mapping{lr[0] + i, tr[0] + i})
		}
		return m, nil
	}
	if r.LocalPort != 0 && r.TargetPort != 0 {
		if !validPort(r.LocalPort) || !validPort(r.TargetPort) {
			return nil, fmt.Errorf("port out of range local=%d target=%d", r.LocalPort, r.TargetPort)
		}
		return []Mapping{{r.LocalPort, r.TargetPort}}, nil
	}
	return nil, errors.New("missing localPort/targetPort or localPortRange/targetPortRange")
}

func validPort(p int) bool { return p > 0 && p < 65536 }

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	if c.Forward == nil {
		return nil, errors.New("invalid config format: 'forward' array not found")
	}

	// Defaults mirror the legacy Node implementation.
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
	return &c, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func boolOr(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}
