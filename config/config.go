package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config represents the top-level Skyline configuration.
// Supports single-node mode, multi-node mode (Nodes list), and Xboard V2 machine mode.
type Config struct {
	LogLevel     string             `json:"log_level" yaml:"log_level"`
	PanelURL     string             `json:"panel_url" yaml:"panel_url"`
	ApiHost      string             `json:"api_host,omitempty" yaml:"api_host,omitempty"`
	Token        string             `json:"token" yaml:"token"`
	ApiKey       string             `json:"api_key,omitempty" yaml:"api_key,omitempty"`
	NodeID       int                `json:"node_id" yaml:"node_id"`
	NodeType     string             `json:"node_type" yaml:"node_type"`
	MachineID    int                `json:"machine_id" yaml:"machine_id"`
	APIVersion   string             `json:"api_version" yaml:"api_version"` // "v1", "v2", or "auto"
	ListenIP     string             `json:"listen_ip" yaml:"listen_ip"`
	CertFile     string             `json:"cert_file" yaml:"cert_file"`
	KeyFile      string             `json:"key_file" yaml:"key_file"`
	PullInterval time.Duration      `json:"pull_interval" yaml:"pull_interval"`
	PushInterval time.Duration      `json:"push_interval" yaml:"push_interval"`
	HTTPTimeout  time.Duration      `json:"http_timeout" yaml:"http_timeout"`
	DeviceTTL    time.Duration      `json:"device_ttl" yaml:"device_ttl"`
	Nodes        []NodeTargetConfig `json:"nodes" yaml:"nodes"`
}

// NodeTargetConfig defines a single node backend instance connected to Xboard.
type NodeTargetConfig struct {
	LogLevel     string        `json:"log_level,omitempty" yaml:"log_level,omitempty"`
	PanelURL     string        `json:"panel_url" yaml:"panel_url"`
	ApiHost      string        `json:"api_host,omitempty" yaml:"api_host,omitempty"`
	Token        string        `json:"token" yaml:"token"`
	ApiKey       string        `json:"api_key,omitempty" yaml:"api_key,omitempty"`
	NodeID       int           `json:"node_id" yaml:"node_id"`
	NodeType     string        `json:"node_type" yaml:"node_type"`
	MachineID    int           `json:"machine_id" yaml:"machine_id"`
	APIVersion   string        `json:"api_version" yaml:"api_version"`
	ListenIP     string        `json:"listen_ip" yaml:"listen_ip"`
	CertFile     string        `json:"cert_file" yaml:"cert_file"`
	KeyFile      string        `json:"key_file" yaml:"key_file"`
	PullInterval time.Duration `json:"pull_interval" yaml:"pull_interval"`
	PushInterval time.Duration `json:"push_interval" yaml:"push_interval"`
	HTTPTimeout  time.Duration `json:"http_timeout" yaml:"http_timeout"`
	DeviceTTL    time.Duration `json:"device_ttl" yaml:"device_ttl"`
}

// Default returns a Config initialized with production defaults.
func Default() *Config {
	return &Config{
		LogLevel:    "warning",
		NodeType:    "mx",
		APIVersion:  "v1",
		HTTPTimeout: 15 * time.Second,
		DeviceTTL:   90 * time.Second,
	}
}

// Load reads configuration from a YAML or JSON file and applies environment overrides.
func Load(path string) (*Config, error) {
	cfg := Default()
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config file %s: %w", path, err)
		}
		if strings.EqualFold(filepath.Ext(path), ".json") {
			if err := json.Unmarshal(raw, cfg); err != nil {
				return nil, fmt.Errorf("parse json config %s: %w", path, err)
			}
		} else {
			if err := yaml.Unmarshal(raw, cfg); err != nil {
				return nil, fmt.Errorf("parse yaml config %s: %w", path, err)
			}
		}
	}
	cfg.ApplyEnv()
	cfg.Normalize()
	return cfg, nil
}

// ApplyEnv overrides configuration fields from SKYLINE_* environment variables.
func (c *Config) ApplyEnv() {
	envStr := func(key string, dst *string) {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			*dst = v
		}
	}
	envInt := func(key string, dst *int) {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				*dst = n
			}
		}
	}
	envStr("SKYLINE_LOG_LEVEL", &c.LogLevel)
	envStr("SKYLINE_PANEL_URL", &c.PanelURL)
	envStr("SKYLINE_TOKEN", &c.Token)
	envInt("SKYLINE_NODE_ID", &c.NodeID)
	envStr("SKYLINE_NODE_TYPE", &c.NodeType)
	envInt("SKYLINE_MACHINE_ID", &c.MachineID)
	envStr("SKYLINE_API_VERSION", &c.APIVersion)
	envStr("SKYLINE_LISTEN_IP", &c.ListenIP)
	envStr("SKYLINE_CERT_FILE", &c.CertFile)
	envStr("SKYLINE_KEY_FILE", &c.KeyFile)
}

// Normalize resolves aliases, defaults, and populates Nodes when configured in single-node mode.
func (c *Config) Normalize() {
	c.PanelURL = strings.TrimRight(strings.TrimSpace(firstNonEmpty(c.PanelURL, c.ApiHost)), "/")
	c.Token = strings.TrimSpace(firstNonEmpty(c.Token, c.ApiKey))
	c.NodeType = normalizeNodeType(c.NodeType)
	c.APIVersion = normalizeAPIVersion(c.APIVersion)
	c.LogLevel = strings.ToLower(strings.TrimSpace(firstNonEmpty(c.LogLevel, "warning")))

	if c.HTTPTimeout <= 0 {
		c.HTTPTimeout = 15 * time.Second
	}
	if c.DeviceTTL <= 0 {
		c.DeviceTTL = 90 * time.Second
	}

	if len(c.Nodes) == 0 && c.NodeID > 0 {
		c.Nodes = []NodeTargetConfig{{NodeID: c.NodeID}}
	}

	for i := range c.Nodes {
		n := &c.Nodes[i]
		n.LogLevel = strings.ToLower(strings.TrimSpace(firstNonEmpty(n.LogLevel, c.LogLevel)))
		n.PanelURL = strings.TrimRight(strings.TrimSpace(firstNonEmpty(n.PanelURL, n.ApiHost, c.PanelURL)), "/")
		n.Token = strings.TrimSpace(firstNonEmpty(n.Token, n.ApiKey, c.Token))
		n.NodeType = normalizeNodeType(firstNonEmpty(n.NodeType, c.NodeType))
		n.APIVersion = normalizeAPIVersion(firstNonEmpty(n.APIVersion, c.APIVersion))
		n.ListenIP = strings.TrimSpace(firstNonEmpty(n.ListenIP, c.ListenIP))
		n.CertFile = strings.TrimSpace(firstNonEmpty(n.CertFile, c.CertFile))
		n.KeyFile = strings.TrimSpace(firstNonEmpty(n.KeyFile, c.KeyFile))
		if n.MachineID == 0 {
			n.MachineID = c.MachineID
		}
		if n.PullInterval <= 0 {
			n.PullInterval = c.PullInterval
		}
		if n.PushInterval <= 0 {
			n.PushInterval = c.PushInterval
		}
		if n.HTTPTimeout <= 0 {
			n.HTTPTimeout = c.HTTPTimeout
		}
		if n.DeviceTTL <= 0 {
			n.DeviceTTL = c.DeviceTTL
		}
	}
}

// Validate checks whether the configuration is complete and valid.
func (c *Config) Validate() error {
	c.Normalize()
	if c.MachineID > 0 && len(c.Nodes) == 0 {
		if c.PanelURL == "" {
			return errors.New("panel_url is required for machine mode")
		}
		if c.Token == "" {
			return errors.New("token is required for machine mode")
		}
		return nil
	}
	if len(c.Nodes) == 0 {
		return errors.New("at least one node (node_id > 0) or machine_id > 0 must be configured")
	}
	for i, n := range c.Nodes {
		if n.PanelURL == "" {
			return fmt.Errorf("nodes[%d]: panel_url is required", i)
		}
		if n.Token == "" {
			return fmt.Errorf("nodes[%d]: token is required", i)
		}
		if n.NodeID <= 0 {
			return fmt.Errorf("nodes[%d]: node_id must be > 0", i)
		}
		if n.NodeType != "mx" {
			return fmt.Errorf("nodes[%d]: unsupported node_type %q (skyline requires \"mx\")", i, n.NodeType)
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func normalizeNodeType(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	if t == "" {
		return "mx"
	}
	return t
}

func normalizeAPIVersion(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "v2", "2":
		return "v2"
	case "auto":
		return "auto"
	default:
		return "v1"
	}
}
