// Package config owns trusted-client process configuration. Server storage,
// ArcSet persistence, and proof-generation settings deliberately do not appear
// here.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key,omitempty"`
	Bucket  string `json:"bucket,omitempty"`
}

type WorkspaceConfig struct {
	StatePath string `json:"state_path"`
}

type DaemonConfig struct {
	SocketPath string `json:"socket_path"`
	StatePath  string `json:"state_path"`
}

// BackupConfig owns local encrypted-backup state. Keys remain client-side and
// are never sent to the Gateway.
type BackupConfig struct {
	KeyringPath string            `json:"keyring_path"`
	StatePath   string            `json:"state_path"`
	TempDir     string            `json:"temp_dir,omitempty"`
	Jobs        []BackupJobConfig `json:"jobs,omitempty"`
}

type BackupJobConfig struct {
	Name    string `json:"name"`
	Source  string `json:"source"`
	Every   string `json:"every"`
	Enabled bool   `json:"enabled"`
	Message string `json:"message,omitempty"`
}

func Default() (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("determine home directory: %w", err)
	}
	root := filepath.Join(home, ".malt-client")
	return &Config{
		Gateway: GatewayConfig{BaseURL: defaultGatewayURL},
		Daemon: DaemonConfig{
			SocketPath: filepath.Join(root, "client.sock"),
			StatePath:  filepath.Join(root, "roots.json"),
		},
		Workspace: WorkspaceConfig{StatePath: filepath.Join(root, "buckets.json")},
		Backup: BackupConfig{
			KeyringPath: filepath.Join(root, "backup-keys.json"),
			StatePath:   filepath.Join(root, "backups.json"),
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
		return nil, fmt.Errorf("read client config: %w", err)
	}
	if err := securefile.Secure(path); err != nil {
		return nil, fmt.Errorf("protect client config: %w", err)
	}
	if err := json.Unmarshal(data, defaults); err != nil {
		return nil, fmt.Errorf("decode client config: %w", err)
	}
	defaults.applyDefaults()
	if err := defaults.Validate(); err != nil {
		return nil, err
	}
	return defaults, nil
}

func Write(path string, cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("client config is nil")
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
		return fmt.Errorf("create client config directory: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return fmt.Errorf("create client config temporary file: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := securefile.Secure(name); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write client config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync client config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("replace client config: %w", err)
	}
	if err := securefile.Secure(path); err != nil {
		return fmt.Errorf("secure client config permissions: %w", err)
	}
	return nil
}

func (c *Config) applyDefaults() {
	defaults, _ := Default()
	if c.Gateway.BaseURL == "" {
		c.Gateway.BaseURL = defaults.Gateway.BaseURL
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
	if c.Backup.StatePath == "" {
		c.Backup.StatePath = defaults.Backup.StatePath
	}
	if c.Backup.TempDir == "" {
		c.Backup.TempDir = defaults.Backup.TempDir
	}
}

func (c *Config) Validate() error {
	if strings.TrimSpace(c.Gateway.BaseURL) == "" {
		return fmt.Errorf("gateway base URL is empty")
	}
	if c.Daemon.SocketPath == "" || c.Daemon.StatePath == "" {
		return fmt.Errorf("daemon socket and state paths are required")
	}
	if c.Workspace.StatePath == "" {
		return fmt.Errorf("Bucket workspace state path is required")
	}
	if c.Backup.KeyringPath == "" || c.Backup.StatePath == "" || c.Backup.TempDir == "" {
		return fmt.Errorf("backup keyring, state, and staging paths are required")
	}
	if strings.TrimSpace(c.Gateway.Bucket) != "" && strings.TrimSpace(c.Gateway.APIKey) == "" {
		return fmt.Errorf("gateway Bucket requires an API key")
	}
	seenJobs := make(map[string]struct{}, len(c.Backup.Jobs))
	for i := range c.Backup.Jobs {
		job := &c.Backup.Jobs[i]
		job.Name = strings.TrimSpace(job.Name)
		job.Source = strings.TrimSpace(job.Source)
		job.Every = strings.TrimSpace(job.Every)
		job.Message = strings.TrimSpace(job.Message)
		if job.Name == "" || job.Source == "" || job.Every == "" {
			return fmt.Errorf("backup job %d requires name, source, and every", i)
		}
		if _, ok := seenJobs[job.Name]; ok {
			return fmt.Errorf("duplicate backup job %q", job.Name)
		}
		seenJobs[job.Name] = struct{}{}
		every, err := time.ParseDuration(job.Every)
		if err != nil || every <= 0 {
			return fmt.Errorf("backup job %q has invalid interval %q", job.Name, job.Every)
		}
	}
	return nil
}

func (c *Config) GatewayBaseURL() string { return strings.TrimRight(c.Gateway.BaseURL, "/") }
