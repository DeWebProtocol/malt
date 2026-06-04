package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	defaultListen      = "127.0.0.1:4318"
	defaultKVStoreType = "badger"
	defaultDataDir     = "data"
)

// Config is the CAS server configuration.
type Config struct {
	Listen  string        `json:"listen"`
	KVStore KVStoreConfig `json:"kvstore"`
}

// KVStoreConfig configures the CAS block store backend.
type KVStoreConfig struct {
	Type    string `json:"type"`
	DataDir string `json:"data_dir"`
}

// DefaultConfig returns the default CAS configuration.
func DefaultConfig() *Config {
	return &Config{
		Listen: defaultListen,
		KVStore: KVStoreConfig{
			Type:    defaultKVStoreType,
			DataDir: defaultDataDir,
		},
	}
}

// DefaultConfigDir returns ~/.malt/cas.
func DefaultConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home dir: %w", err)
	}
	return filepath.Join(home, ".malt", "cas"), nil
}

// DefaultConfigPath returns ~/.malt/cas/settings.json.
func DefaultConfigPath() (string, error) {
	dir, err := DefaultConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "settings.json"), nil
}

// ResolveConfigPath returns the explicit path if provided, otherwise the default.
func ResolveConfigPath(explicit string) (string, error) {
	if explicit != "" {
		return expandPath(explicit)
	}
	return DefaultConfigPath()
}

// Load loads configuration from the given path, falling back to defaults.
func Load(path string) (*Config, error) {
	resolved, err := ResolveConfigPath(path)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := DefaultConfig()
			return cfg, cfg.Validate()
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg := DefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// WriteToFile writes the config as pretty JSON.
func WriteToFile(path string, cfg *Config) error {
	resolved, err := expandPath(path)
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
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

func (c *Config) applyDefaults() {
	defaults := DefaultConfig()
	if c.Listen == "" {
		c.Listen = defaults.Listen
	}
	if c.KVStore.Type == "" {
		c.KVStore.Type = defaults.KVStore.Type
	}
	if c.KVStore.DataDir == "" {
		c.KVStore.DataDir = defaults.KVStore.DataDir
	}
}

// Validate checks the config values.
func (c *Config) Validate() error {
	if c.Listen == "" {
		return fmt.Errorf("listen address is required")
	}

	switch c.KVStore.Type {
	case "badger", "memory", "fs":
	default:
		return fmt.Errorf("unsupported kvstore.type %q (use badger, memory, or fs)", c.KVStore.Type)
	}

	if c.KVStore.Type != "memory" && c.KVStore.DataDir == "" {
		return fmt.Errorf("kvstore.data_dir is required for %q store", c.KVStore.Type)
	}

	return nil
}

// KVStorePath returns the resolved absolute path for the KV store data directory.
func (c *Config) KVStorePath() string {
	if filepath.IsAbs(c.KVStore.DataDir) {
		return c.KVStore.DataDir
	}
	dir, _ := DefaultConfigDir()
	return filepath.Join(dir, c.KVStore.DataDir)
}

// SettingsPath returns the default settings file path.
func (c *Config) SettingsPath() string {
	path, _ := DefaultConfigPath()
	return path
}

func expandPath(path string) (string, error) {
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
