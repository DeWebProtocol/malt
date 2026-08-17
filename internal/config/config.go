// Package config owns local-runtime process configuration. Server storage,
// ArcSet persistence, and proof-generation settings deliberately do not appear
// here.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dewebprotocol/malt-client/internal/durablefile"
	"github.com/dewebprotocol/malt-client/internal/securefile"
)

const (
	defaultGatewayURL = "http://127.0.0.1:8080"
)

type Config struct {
	Gateway   GatewayConfig   `json:"gateway"`
	Daemon    DaemonConfig    `json:"daemon"`
	Workspace WorkspaceConfig `json:"workspace"`
	Backup    BackupConfig    `json:"backup"`
}

type GatewayConfig struct {
	BaseURL        string `json:"base_url"`
	CredentialPath string `json:"credential_path"`
	APIKey         string `json:"api_key,omitempty"`
	Bucket         string `json:"bucket,omitempty"`
}

type WorkspaceConfig struct {
	StatePath string `json:"state_path"`
}

type DaemonConfig struct {
	SocketPath string `json:"socket_path"`
	StatePath  string `json:"state_path"`
}

// BackupConfig owns local encrypted-backup state. Keys remain runtime-local and
// are never sent to the Gateway.
type BackupConfig struct {
	KeyringPath string `json:"keyring_path"`
	HistoryDir  string `json:"history_dir"`
	PlansPath   string `json:"plans_path"`
	TempDir     string `json:"temp_dir,omitempty"`
}

func Default() (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("determine home directory: %w", err)
	}
	root := filepath.Join(home, ".malt-client")
	return &Config{
		Gateway: GatewayConfig{
			BaseURL: defaultGatewayURL, CredentialPath: filepath.Join(root, "device-credential.json"),
		},
		Daemon: DaemonConfig{
			SocketPath: filepath.Join(root, "client.sock"),
			StatePath:  filepath.Join(root, "roots.json"),
		},
		Workspace: WorkspaceConfig{StatePath: filepath.Join(root, "buckets.json")},
		Backup: BackupConfig{
			KeyringPath: filepath.Join(root, "backup-keys.json"),
			HistoryDir:  filepath.Join(root, "backup-history"),
			PlansPath:   filepath.Join(root, "backup-plans.json"),
			TempDir:     filepath.Join(root, "staging"),
		},
	}, nil
}

func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}
	return filepath.Join(home, ".malt-client", "config.json"), nil
}

func Load(path string) (*Config, error) {
	if strings.TrimSpace(path) == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return nil, err
		}
	}
	defaults, err := Default()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return defaults, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read runtime config: %w", err)
	}
	if err := securefile.Secure(path); err != nil {
		return nil, fmt.Errorf("protect runtime config: %w", err)
	}
	var legacy struct {
		Backup struct {
			Jobs      json.RawMessage `json:"jobs"`
			StatePath json.RawMessage `json:"state_path"`
		} `json:"backup"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil, fmt.Errorf("decode runtime config: %w", err)
	}
	if len(legacy.Backup.Jobs) != 0 {
		return nil, fmt.Errorf("legacy backup.jobs is no longer supported; create Plan bindings with `malt backup bind` and schedules with `malt backup schedule set`")
	}
	if len(legacy.Backup.StatePath) != 0 {
		return nil, fmt.Errorf("legacy backup.state_path is no longer supported; remove it and use the Plan-only backup.history_dir")
	}
	if err := json.Unmarshal(data, defaults); err != nil {
		return nil, fmt.Errorf("decode runtime config: %w", err)
	}
	defaults.applyDefaults()
	if err := defaults.Validate(); err != nil {
		return nil, err
	}
	return defaults, nil
}

func Write(path string, cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("runtime config is nil")
	}
	if strings.TrimSpace(path) == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return err
		}
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create runtime config directory: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return fmt.Errorf("create runtime config temporary file: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := securefile.Secure(name); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write runtime config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync runtime config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("replace runtime config: %w", err)
	}
	if err := securefile.Secure(path); err != nil {
		return fmt.Errorf("secure runtime config permissions: %w", err)
	}
	if err := durablefile.SyncParent(path); err != nil {
		return fmt.Errorf("sync runtime config directory: %w", err)
	}
	return nil
}

func (c *Config) applyDefaults() {
	defaults, _ := Default()
	if c.Gateway.BaseURL == "" {
		c.Gateway.BaseURL = defaults.Gateway.BaseURL
	}
	if c.Gateway.CredentialPath == "" {
		c.Gateway.CredentialPath = defaults.Gateway.CredentialPath
	}
	if c.Daemon.SocketPath == "" {
		c.Daemon.SocketPath = defaults.Daemon.SocketPath
	}
	if c.Daemon.StatePath == "" {
		c.Daemon.StatePath = defaults.Daemon.StatePath
	}
	if c.Workspace.StatePath == "" {
		c.Workspace.StatePath = defaults.Workspace.StatePath
	}
	if c.Backup.KeyringPath == "" {
		c.Backup.KeyringPath = defaults.Backup.KeyringPath
	}
	if c.Backup.HistoryDir == "" {
		c.Backup.HistoryDir = defaults.Backup.HistoryDir
	}
	if c.Backup.PlansPath == "" {
		c.Backup.PlansPath = defaults.Backup.PlansPath
	}
	if c.Backup.TempDir == "" {
		c.Backup.TempDir = defaults.Backup.TempDir
	}
}

func (c *Config) Validate() error {
	if strings.TrimSpace(c.Gateway.BaseURL) == "" || strings.TrimSpace(c.Gateway.CredentialPath) == "" {
		return fmt.Errorf("gateway base URL and device credential path are required")
	}
	if c.Daemon.SocketPath == "" || c.Daemon.StatePath == "" {
		return fmt.Errorf("daemon socket and state paths are required")
	}
	if c.Workspace.StatePath == "" {
		return fmt.Errorf("Bucket workspace state path is required")
	}
	if c.Backup.KeyringPath == "" || c.Backup.HistoryDir == "" || c.Backup.PlansPath == "" || c.Backup.TempDir == "" {
		return fmt.Errorf("backup keyring, history directory, plans, and staging paths are required")
	}
	return nil
}

func (c *Config) GatewayBaseURL() string { return strings.TrimRight(c.Gateway.BaseURL, "/") }
