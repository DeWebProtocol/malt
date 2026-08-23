package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteAndLoadPreserveClientBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := &Config{
		Gateway: GatewayConfig{BaseURL: "https://gateway.example.test/", APIKey: "secret", Bucket: "bkt_one"},
		Daemon: DaemonConfig{
			SocketPath: filepath.Join(t.TempDir(), "client.sock"),
			StatePath:  filepath.Join(t.TempDir(), "roots.json"),
		},
	}
	if err := Write(path, cfg); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %#o, want 0600", info.Mode().Perm())
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.GatewayBaseURL() != "https://gateway.example.test" {
		t.Fatalf("gateway URL = %q", loaded.GatewayBaseURL())
	}
	if loaded.Gateway.APIKey != "secret" || loaded.Gateway.Bucket != "bkt_one" || loaded.Workspace.StatePath == "" {
		t.Fatalf("managed Gateway config = %#v, workspace = %#v", loaded.Gateway, loaded.Workspace)
	}
	if loaded.Backup.KeyringPath == "" || loaded.Backup.HistoryDir == "" || loaded.Backup.TempDir == "" {
		t.Fatalf("backup defaults missing: %#v", loaded.Backup)
	}
	if loaded.Transport.CASPolicy != CASPolicyGateway || loaded.Transport.LocalCASDir == "" {
		t.Fatalf("transport defaults missing: %#v", loaded.Transport)
	}
	if loaded.Filesystem.MountsPath == "" || loaded.Filesystem.CacheDir == "" || loaded.Filesystem.WritableStateDir == "" ||
		loaded.Filesystem.MaxStagedFileBytes != 256<<20 {
		t.Fatalf("filesystem defaults missing: %#v", loaded.Filesystem)
	}
}

func TestLoadAppliesMissingDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"gateway":{"base_url":"http://127.0.0.1:9090"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Daemon.SocketPath == "" || loaded.Daemon.StatePath == "" || loaded.Workspace.StatePath == "" ||
		loaded.Backup.KeyringPath == "" || loaded.Filesystem.MountsPath == "" || loaded.Filesystem.CacheDir == "" ||
		loaded.Filesystem.WritableStateDir == "" || loaded.Filesystem.MaxStagedFileBytes == 0 ||
		loaded.Transport.CASPolicy != CASPolicyGateway || loaded.Transport.LocalCASDir == "" {
		t.Fatalf("daemon defaults missing: %#v", loaded.Daemon)
	}
}

func TestValidateTransportCASPolicy(t *testing.T) {
	for _, policy := range []string{CASPolicyGateway, CASPolicyLocal, CASPolicyHybrid, " HYBRID "} {
		cfg, err := Default()
		if err != nil {
			t.Fatal(err)
		}
		cfg.Transport.CASPolicy = policy
		if err := cfg.Validate(); err != nil {
			t.Fatalf("policy %q: %v", policy, err)
		}
	}
	for _, mutate := range []func(*Config){
		func(cfg *Config) { cfg.Transport.CASPolicy = "peer" },
		func(cfg *Config) { cfg.Transport.LocalCASDir = " " },
	} {
		cfg, err := Default()
		if err != nil {
			t.Fatal(err)
		}
		mutate(cfg)
		if err := cfg.Validate(); err == nil {
			t.Fatalf("Validate accepted invalid transport config %#v", cfg.Transport)
		}
	}
}

func TestValidateRejectsInvalidFilesystemWritebackConfiguration(t *testing.T) {
	for _, mutate := range []func(*Config){
		func(cfg *Config) { cfg.Filesystem.WritableStateDir = " " },
		func(cfg *Config) { cfg.Filesystem.MaxStagedFileBytes = ^uint64(0) },
	} {
		cfg, err := Default()
		if err != nil {
			t.Fatal(err)
		}
		mutate(cfg)
		if err := cfg.Validate(); err == nil {
			t.Fatalf("Validate accepted invalid filesystem config %#v", cfg.Filesystem)
		}
	}
}

func TestLoadRejectsLegacyBackupJobs(t *testing.T) {
	for _, raw := range []string{
		`[{"name":"docs","source":"/data/docs","every":"1h"}]`,
		`[]`,
		`null`,
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(`{"backup":{"jobs":`+raw+`}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "backup.jobs") {
			t.Fatalf("legacy backup.jobs=%s error = %v", raw, err)
		}
	}
}

func TestLoadRejectsLegacyBackupStatePath(t *testing.T) {
	for _, raw := range []string{`"/tmp/legacy-backups.json"`, `""`, `null`} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(`{"backup":{"state_path":`+raw+`}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "backup.state_path") {
			t.Fatalf("legacy backup.state_path=%s error = %v", raw, err)
		}
	}
}

func TestWriteTightensExistingConfigPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Gateway.APIKey = "secret"
	if err := Write(path, cfg); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %#o, want 0600", info.Mode().Perm())
	}
}
