// Package config provides configuration loading and persistence for MALT.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultRPCListen        = "127.0.0.1:4317"
	defaultCASBaseURL       = "http://127.0.0.1:4318"
	defaultCASMode          = "external"
	defaultStructureBackend = "kzg"
	defaultArcTableType     = "versioned"
	defaultKVStoreType      = "badger"
	defaultLoggingLevel     = "info"
	defaultLoggingFormat    = "json"
	defaultCASTimeout       = "30s"
)

// Config is the root configuration for the MALT runtime.
type Config struct {
	RPC       RPCConfig       `json:"rpc"`
	State     StateConfig     `json:"state"`
	Structure StructureConfig `json:"structure"`
	CAS       CASConfig       `json:"cas"`
	Logging   LoggingConfig   `json:"logging"`
	Client    ClientConfig    `json:"client"`
}

// RPCConfig configures the local daemon HTTP endpoint.
type RPCConfig struct {
	Listen             string   `json:"listen"`
	CORSAllowedOrigins []string `json:"cors_allowed_origins,omitempty"`
}

// StateConfig configures local durable runtime state.
type StateConfig struct {
	RootDir  string         `json:"root_dir"`
	KVStore  KVStoreConfig  `json:"kvstore"`
	ArcTable ArcTableConfig `json:"arctable"`
}

// KVStoreConfig configures the local KV store.
type KVStoreConfig struct {
	Type string `json:"type"`
	Path string `json:"path"`
}

// ArcTableConfig configures the ArcTable implementation.
type ArcTableConfig struct {
	Type string `json:"type"`
}

// StructureConfig configures structure runtime defaults.
type StructureConfig struct {
	DefaultBackend string `json:"default_backend"`
}

// CASConfig configures the immutable content backend.
type CASConfig struct {
	Mode    string `json:"mode"`
	BaseURL string `json:"base_url,omitempty"`
	Timeout string `json:"timeout"`
}

// LoggingConfig configures runtime logging.
type LoggingConfig struct {
	Level  string `json:"level"`
	Format string `json:"format"`
}

// ClientConfig configures client-side CLI defaults.
type ClientConfig struct {
}

// DefaultConfig returns the runtime default configuration.
func DefaultConfig() *Config {
	home, _ := os.UserHomeDir()
	stateRoot := filepath.Join(home, ".malt", "state")

	return &Config{
		RPC: RPCConfig{
			Listen: defaultRPCListen,
		},
		State: StateConfig{
			RootDir: stateRoot,
			KVStore: KVStoreConfig{
				Type: defaultKVStoreType,
				Path: "kv",
			},
			ArcTable: ArcTableConfig{
				Type: defaultArcTableType,
			},
		},
		Structure: StructureConfig{
			DefaultBackend: defaultStructureBackend,
		},
		CAS: CASConfig{
			Mode:    defaultCASMode,
			BaseURL: defaultCASBaseURL,
			Timeout: defaultCASTimeout,
		},
		Logging: LoggingConfig{
			Level:  defaultLoggingLevel,
			Format: defaultLoggingFormat,
		},
		Client: ClientConfig{},
	}
}

// DefaultConfigPath returns the canonical default config path.
func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home dir: %w", err)
	}
	return filepath.Join(home, ".malt", "malt.json"), nil
}

// ResolveConfigPath returns the explicit path if provided, otherwise the default path.
func ResolveConfigPath(explicit string) (string, error) {
	if explicit != "" {
		return ExpandPath(explicit)
	}
	return DefaultConfigPath()
}

// ExpandPath expands "~" prefixes and cleans the resulting path.
func ExpandPath(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("determine home dir: %w", err)
		}
		trimmed := strings.TrimPrefix(strings.TrimPrefix(path, "~/"), "~\\")
		return filepath.Clean(filepath.Join(home, trimmed)), nil
	}
	return filepath.Clean(path), nil
}

// Load loads configuration from the default config path if it exists, otherwise returns defaults.
func Load() (*Config, error) {
	path, err := DefaultConfigPath()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			cfg := DefaultConfig()
			return cfg, cfg.Validate()
		}
		return nil, fmt.Errorf("stat config file: %w", err)
	}
	return LoadFromFile(path)
}

// LoadFromFile loads configuration from a specific file.
func LoadFromFile(path string) (*Config, error) {
	resolved, err := ExpandPath(path)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("error reading config file: %w", err)
	}

	cfg := DefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("error reading config file: %w", err)
	}

	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// WriteToFile writes the config to the given path in pretty JSON format.
func WriteToFile(path string, cfg *Config) error {
	resolved, err := ExpandPath(path)
	if err != nil {
		return err
	}
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}

	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	data = append(data, '\n')

	if err := os.WriteFile(resolved, data, 0o644); err != nil {
		return fmt.Errorf("write config file: %w", err)
	}
	return nil
}

func (c *Config) applyDefaults() {
	defaults := DefaultConfig()
	casModeWasEmpty := c.CAS.Mode == ""
	if c.RPC.Listen == "" {
		c.RPC.Listen = defaults.RPC.Listen
	}
	c.RPC.CORSAllowedOrigins = cleanStringList(c.RPC.CORSAllowedOrigins)
	if c.State.RootDir == "" {
		c.State.RootDir = defaults.State.RootDir
	}
	if c.State.KVStore.Type == "" {
		c.State.KVStore.Type = defaults.State.KVStore.Type
	}
	if c.State.KVStore.Path == "" {
		c.State.KVStore.Path = defaults.State.KVStore.Path
	}
	if c.State.ArcTable.Type == "" {
		c.State.ArcTable.Type = defaults.State.ArcTable.Type
	}
	if c.Structure.DefaultBackend == "" {
		c.Structure.DefaultBackend = defaults.Structure.DefaultBackend
	}
	if c.CAS.Mode == "" {
		c.CAS.Mode = defaults.CAS.Mode
	}
	if c.CAS.BaseURL == "" && casModeWasEmpty {
		c.CAS.BaseURL = defaults.CAS.BaseURL
	}
	if c.CAS.Timeout == "" {
		c.CAS.Timeout = defaults.CAS.Timeout
	}
	if c.Logging.Level == "" {
		c.Logging.Level = defaults.Logging.Level
	}
	if c.Logging.Format == "" {
		c.Logging.Format = defaults.Logging.Format
	}
}

// Validate validates and normalizes the config.
func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config is nil")
	}
	c.applyDefaults()

	var err error
	c.State.RootDir, err = ExpandPath(c.State.RootDir)
	if err != nil {
		return err
	}

	switch c.State.KVStore.Type {
	case "badger", "memory", "fs":
	default:
		return fmt.Errorf("unsupported state.kvstore.type %q", c.State.KVStore.Type)
	}

	switch c.State.ArcTable.Type {
	case "overwrite", "versioned", "simple":
	default:
		return fmt.Errorf("unsupported state.arctable.type %q", c.State.ArcTable.Type)
	}

	switch c.Structure.DefaultBackend {
	case "kzg", "ipa":
	default:
		return fmt.Errorf("unsupported structure.default_backend %q", c.Structure.DefaultBackend)
	}

	switch c.CAS.Mode {
	case "external":
	default:
		return fmt.Errorf("unsupported cas.mode %q (use external)", c.CAS.Mode)
	}

	if _, err := c.CASTimeout(); err != nil {
		return fmt.Errorf("invalid cas.timeout: %w", err)
	}

	if c.CAS.BaseURL == "" {
		return fmt.Errorf("cas.base_url is required")
	}

	return nil
}

func cleanStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := values[:0]
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

// ConfigDir returns the parent directory of the config file.
func ConfigDir(configPath string) (string, error) {
	resolved, err := ResolveConfigPath(configPath)
	if err != nil {
		return "", err
	}
	return filepath.Dir(resolved), nil
}

// RPCBaseURL returns the daemon base URL.
func (c *Config) RPCBaseURL() string {
	listen := c.RPC.Listen
	if !strings.HasPrefix(listen, "http://") && !strings.HasPrefix(listen, "https://") {
		return "http://" + listen
	}
	return listen
}

// APIBaseURL returns the configured reference-executor API base URL.
func (c *Config) APIBaseURL() string {
	return strings.TrimRight(c.RPCBaseURL(), "/")
}

// CASBaseURL returns the active CAS HTTP endpoint.
func (c *Config) CASBaseURL() string {
	return strings.TrimRight(c.CAS.BaseURL, "/")
}

// KVStorePath returns the absolute KVStore path.
func (c *Config) KVStorePath() string {
	if filepath.IsAbs(c.State.KVStore.Path) {
		return c.State.KVStore.Path
	}
	return filepath.Join(c.State.RootDir, c.State.KVStore.Path)
}

// CASTimeout parses and returns the CAS timeout.
func (c *Config) CASTimeout() (time.Duration, error) {
	return time.ParseDuration(c.CAS.Timeout)
}

// String returns a compact string representation of the config.
func (c *Config) String() string {
	return fmt.Sprintf("Config{rpc=%s, state=%s, backend=%s, cas=%s}",
		c.RPC.Listen,
		c.State.RootDir,
		c.Structure.DefaultBackend,
		c.CAS.Mode,
	)
}
