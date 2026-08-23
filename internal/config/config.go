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
	Gateway    GatewayConfig    `json:"gateway"`
	Transport  TransportConfig  `json:"transport"`
	Daemon     DaemonConfig     `json:"daemon"`
	Workspace  WorkspaceConfig  `json:"workspace"`
	Backup     BackupConfig     `json:"backup"`
	Filesystem FilesystemConfig `json:"filesystem"`
}

const (
	CASPolicyGateway = "gateway"
	CASPolicyLocal   = "local"
	CASPolicyHybrid  = "hybrid"
)

// TransportConfig selects immutable-byte topology at the runtime composition
// boundary. Application, filesystem, and trust packages never inspect it.
type TransportConfig struct {
	CASPolicy   string `json:"cas_policy"`
	LocalCASDir string `json:"local_cas_dir"`
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
}

// FilesystemConfig owns non-authoritative cache, durable local mount intent,
// and leased write-back journal/cache state. None stores or replaces accepted
// trust state.
type FilesystemConfig struct {
	MountsPath         string `json:"mounts_path"`
	CacheDir           string `json:"cache_dir"`
	WritableStateDir   string `json:"writable_state_dir"`
	MaxStagedFileBytes uint64 `json:"max_staged_file_bytes"`
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
		Transport: TransportConfig{CASPolicy: CASPolicyGateway, LocalCASDir: filepath.Join(root, "local-cas")},
		Daemon: DaemonConfig{
			SocketPath: filepath.Join(root, "client.sock"),
			StatePath:  filepath.Join(root, "roots.json"),
		},
		Workspace: WorkspaceConfig{StatePath: filepath.Join(root, "buckets.json")},
		Backup: BackupConfig{
			KeyringPath: filepath.Join(root, "backup-keys.json"),
			HistoryDir:  filepath.Join(root, "backup-history"),
			PlansPath:   filepath.Join(root, "backup-plans.json"),
		},
		Filesystem: FilesystemConfig{
			MountsPath:         filepath.Join(root, "mounts.json"),
			CacheDir:           filepath.Join(root, "filesystem-cache"),
			WritableStateDir:   filepath.Join(root, "filesystem-writeback"),
			MaxStagedFileBytes: 256 << 20,
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
	if c.Transport.CASPolicy == "" {
		c.Transport.CASPolicy = defaults.Transport.CASPolicy
	}
	if c.Transport.LocalCASDir == "" {
		c.Transport.LocalCASDir = defaults.Transport.LocalCASDir
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
	if c.Filesystem.MountsPath == "" {
		c.Filesystem.MountsPath = defaults.Filesystem.MountsPath
	}
	if c.Filesystem.CacheDir == "" {
		c.Filesystem.CacheDir = defaults.Filesystem.CacheDir
	}
	if c.Filesystem.WritableStateDir == "" {
		c.Filesystem.WritableStateDir = defaults.Filesystem.WritableStateDir
	}
	if c.Filesystem.MaxStagedFileBytes == 0 {
		c.Filesystem.MaxStagedFileBytes = defaults.Filesystem.MaxStagedFileBytes
	}
}

func (c *Config) Validate() error {
	if strings.TrimSpace(c.Gateway.BaseURL) == "" || strings.TrimSpace(c.Gateway.CredentialPath) == "" {
		return fmt.Errorf("gateway base URL and device credential path are required")
	}
	c.Transport.CASPolicy = strings.ToLower(strings.TrimSpace(c.Transport.CASPolicy))
	switch c.Transport.CASPolicy {
	case CASPolicyGateway, CASPolicyLocal, CASPolicyHybrid:
	default:
		return fmt.Errorf("transport CAS policy must be gateway, local, or hybrid")
	}
	if strings.TrimSpace(c.Transport.LocalCASDir) == "" {
		return fmt.Errorf("transport local CAS directory is required")
	}
	if c.Daemon.SocketPath == "" || c.Daemon.StatePath == "" {
		return fmt.Errorf("daemon socket and state paths are required")
	}
	if c.Workspace.StatePath == "" {
		return fmt.Errorf("Bucket workspace state path is required")
	}
	if c.Backup.KeyringPath == "" || c.Backup.HistoryDir == "" || c.Backup.PlansPath == "" {
		return fmt.Errorf("backup keyring, history directory, and plans paths are required")
	}
	if strings.TrimSpace(c.Filesystem.MountsPath) == "" || strings.TrimSpace(c.Filesystem.CacheDir) == "" ||
		strings.TrimSpace(c.Filesystem.WritableStateDir) == "" {
		return fmt.Errorf("filesystem mount registry, cache, and writable-state paths are required")
	}
	if c.Filesystem.MaxStagedFileBytes == 0 || c.Filesystem.MaxStagedFileBytes > uint64(^uint(0)>>1) {
		return fmt.Errorf("filesystem max staged file bytes must fit the local address space")
	}
	return nil
}

func (c *Config) GatewayBaseURL() string { return strings.TrimRight(c.Gateway.BaseURL, "/") }
