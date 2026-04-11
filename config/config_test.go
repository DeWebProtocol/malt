package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
)

// resetViper clears all global Viper state so that tests do not pollute each other.
func resetViper(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		viper.Reset()
	})
}

func TestInit_Defaults(t *testing.T) {
	resetViper(t)

	Init()

	if got := viper.GetString("commitment_type"); got != "kzg" {
		t.Errorf("commitment_type = %q, want %q", got, "kzg")
	}
	if got := viper.GetString("kvstore_type"); got != "memory" {
		t.Errorf("kvstore_type = %q, want %q", got, "memory")
	}
	if got := viper.GetString("eat_type"); got != "simple" {
		t.Errorf("eat_type = %q, want %q", got, "simple")
	}
	if got := viper.GetString("cas_type"); got != "mock" {
		t.Errorf("cas_type = %q, want %q", got, "mock")
	}
	if got := viper.GetString("kvstore.path"); got != "./data/malt.db" {
		t.Errorf("kvstore.path = %q, want %q", got, "./data/malt.db")
	}
	if got := viper.GetBool("kvstore.in_memory"); !got {
		t.Errorf("kvstore.in_memory = %v, want true", got)
	}
	if got := viper.GetInt("commitment.vector_size"); got != 256 {
		t.Errorf("commitment.vector_size = %d, want 256", got)
	}
	if got := viper.GetString("cas.gateway_url"); got != "https://ipfs.io/ipfs" {
		t.Errorf("cas.gateway_url = %q, want %q", got, "https://ipfs.io/ipfs")
	}
	if got := viper.GetString("cas.timeout"); got != "30s" {
		t.Errorf("cas.timeout = %q, want %q", got, "30s")
	}
	if got := viper.GetString("logging.level"); got != "info" {
		t.Errorf("logging.level = %q, want %q", got, "info")
	}
	if got := viper.GetString("logging.format"); got != "json" {
		t.Errorf("logging.format = %q, want %q", got, "json")
	}
}

func TestLoad_NoConfigFile(t *testing.T) {
	resetViper(t)

	Init()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.CommitmentType != "kzg" {
		t.Errorf("CommitmentType = %q, want %q", cfg.CommitmentType, "kzg")
	}
	if cfg.KVStoreType != "memory" {
		t.Errorf("KVStoreType = %q, want %q", cfg.KVStoreType, "memory")
	}
	if cfg.EATType != "simple" {
		t.Errorf("EATType = %q, want %q", cfg.EATType, "simple")
	}
	if cfg.CASType != "mock" {
		t.Errorf("CASType = %q, want %q", cfg.CASType, "mock")
	}
	if cfg.KVStore.Path != "./data/malt.db" {
		t.Errorf("KVStore.Path = %q, want %q", cfg.KVStore.Path, "./data/malt.db")
	}
	if !cfg.KVStore.InMemory {
		t.Errorf("KVStore.InMemory = %v, want true", cfg.KVStore.InMemory)
	}
	if cfg.Commitment.VectorSize != 256 {
		t.Errorf("Commitment.VectorSize = %d, want 256", cfg.Commitment.VectorSize)
	}
	if cfg.CAS.GatewayURL != "https://ipfs.io/ipfs" {
		t.Errorf("CAS.GatewayURL = %q, want %q", cfg.CAS.GatewayURL, "https://ipfs.io/ipfs")
	}
	if cfg.CAS.Timeout != "30s" {
		t.Errorf("CAS.Timeout = %q, want %q", cfg.CAS.Timeout, "30s")
	}
	if cfg.Logging.Level != "info" {
		t.Errorf("Logging.Level = %q, want %q", cfg.Logging.Level, "info")
	}
	if cfg.Logging.Format != "json" {
		t.Errorf("Logging.Format = %q, want %q", cfg.Logging.Format, "json")
	}
}

func TestLoadFromFile_ValidJSON(t *testing.T) {
	resetViper(t)

	content := `{
		"commitment_type": "ipa",
		"kvstore_type": "badger",
		"eat_type": "full",
		"cas_type": "remote",
		"kvstore": {
			"path": "/tmp/test.db",
			"in_memory": false
		},
		"commitment": {
			"vector_size": 512
		},
		"cas": {
			"gateway_url": "https://example.com/ipfs",
			"timeout": "60s"
		},
		"logging": {
			"level": "debug",
			"format": "text"
		}
	}`

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test_config.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write temp config file: %v", err)
	}

	Init()

	cfg, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile(%q) returned error: %v", path, err)
	}

	if cfg.CommitmentType != "ipa" {
		t.Errorf("CommitmentType = %q, want %q", cfg.CommitmentType, "ipa")
	}
	if cfg.KVStoreType != "badger" {
		t.Errorf("KVStoreType = %q, want %q", cfg.KVStoreType, "badger")
	}
	if cfg.EATType != "full" {
		t.Errorf("EATType = %q, want %q", cfg.EATType, "full")
	}
	if cfg.CASType != "remote" {
		t.Errorf("CASType = %q, want %q", cfg.CASType, "remote")
	}
	if cfg.KVStore.Path != "/tmp/test.db" {
		t.Errorf("KVStore.Path = %q, want %q", cfg.KVStore.Path, "/tmp/test.db")
	}
	if cfg.KVStore.InMemory {
		t.Errorf("KVStore.InMemory = %v, want false", cfg.KVStore.InMemory)
	}
	if cfg.Commitment.VectorSize != 512 {
		t.Errorf("Commitment.VectorSize = %d, want 512", cfg.Commitment.VectorSize)
	}
	if cfg.CAS.GatewayURL != "https://example.com/ipfs" {
		t.Errorf("CAS.GatewayURL = %q, want %q", cfg.CAS.GatewayURL, "https://example.com/ipfs")
	}
	if cfg.CAS.Timeout != "60s" {
		t.Errorf("CAS.Timeout = %q, want %q", cfg.CAS.Timeout, "60s")
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("Logging.Level = %q, want %q", cfg.Logging.Level, "debug")
	}
	if cfg.Logging.Format != "text" {
		t.Errorf("Logging.Format = %q, want %q", cfg.Logging.Format, "text")
	}
}

func TestLoadFromFile_InvalidPath(t *testing.T) {
	resetViper(t)

	Init()

	_, err := LoadFromFile("/nonexistent/path/config.json")
	if err == nil {
		t.Fatal("LoadFromFile() with invalid path should return an error")
	}
	if !strings.Contains(err.Error(), "error reading config file") {
		t.Errorf("error message = %q, should contain %q", err.Error(), "error reading config file")
	}
}

func TestLoadFromFile_InvalidJSON(t *testing.T) {
	resetViper(t)

	content := `{this is not valid json!!!`

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "bad_config.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write temp config file: %v", err)
	}

	Init()

	_, err := LoadFromFile(path)
	if err == nil {
		t.Fatal("LoadFromFile() with invalid JSON should return an error")
	}
	if !strings.Contains(err.Error(), "error reading config file") {
		t.Errorf("error message = %q, should contain %q", err.Error(), "error reading config file")
	}
}

func TestConfig_CASTimeout(t *testing.T) {
	tests := []struct {
		name    string
		timeout string
		want    time.Duration
	}{
		{"30 seconds", "30s", 30 * time.Second},
		{"1 minute", "1m", 1 * time.Minute},
		{"2 minutes 30 seconds", "2m30s", 150 * time.Second},
		{"1 hour", "1h", 1 * time.Hour},
		{"500 milliseconds", "500ms", 500 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				CAS: CASConfig{
					Timeout: tt.timeout,
				},
			}

			dur, err := cfg.CASTimeout()
			if err != nil {
				t.Fatalf("CASTimeout() returned error: %v", err)
			}

			if dur != tt.want {
				t.Errorf("CASTimeout() = %v, want %v", dur, tt.want)
			}
		})
	}
}

func TestConfig_CASTimeout_Invalid(t *testing.T) {
	cfg := &Config{
		CAS: CASConfig{
			Timeout: "not-a-duration",
		},
	}

	_, err := cfg.CASTimeout()
	if err == nil {
		t.Fatal("CASTimeout() with invalid duration should return an error")
	}
}

func TestConfig_String(t *testing.T) {
	cfg := &Config{
		CommitmentType: "kzg",
		KVStoreType:    "memory",
		EATType:        "simple",
		CASType:        "mock",
	}

	got := cfg.String()
	want := "Config{commitment=kzg, kv=memory, eat=simple, cas=mock}"
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
