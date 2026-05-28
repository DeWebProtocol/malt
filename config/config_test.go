package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.RPC.Listen != "127.0.0.1:4317" {
		t.Fatalf("RPC.Listen = %q", cfg.RPC.Listen)
	}
	wantCORS := []string{
		"http://127.0.0.1:5173",
		"http://localhost:5173",
		"http://127.0.0.1:5174",
		"http://localhost:5174",
		"http://127.0.0.1:4173",
		"http://localhost:4173",
		"http://127.0.0.1:4174",
		"http://localhost:4174",
		"https://dewebprotocol.dev",
		"https://dewebprotocol.github.io",
	}
	if !slices.Equal(cfg.RPC.CORSAllowedOrigins, wantCORS) {
		t.Fatalf("RPC.CORSAllowedOrigins = %v, want %v", cfg.RPC.CORSAllowedOrigins, wantCORS)
	}
	if cfg.State.KVStore.Type != "badger" {
		t.Fatalf("State.KVStore.Type = %q", cfg.State.KVStore.Type)
	}
	if cfg.State.ArcTable.Type != "versioned" {
		t.Fatalf("State.ArcTable.Type = %q", cfg.State.ArcTable.Type)
	}
	if cfg.Structure.DefaultBackend != "kzg" {
		t.Fatalf("Structure.DefaultBackend = %q", cfg.Structure.DefaultBackend)
	}
	if cfg.CAS.Mode != "embedded-mock" {
		t.Fatalf("CAS.Mode = %q", cfg.CAS.Mode)
	}
	if !cfg.CAS.EmbeddedMock.Enabled {
		t.Fatal("embedded mock should be enabled by default")
	}
}

func TestLoad_NoConfigFileReturnsDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.CAS.Mode != "embedded-mock" {
		t.Fatalf("CAS.Mode = %q", cfg.CAS.Mode)
	}
	expectedRoot := filepath.Join(home, ".malt", "state")
	if cfg.State.RootDir != expectedRoot {
		t.Fatalf("State.RootDir = %q, want %q", cfg.State.RootDir, expectedRoot)
	}
}

func TestLoadFromFile_EmptyCORSUsesDefaultBrowserOrigins(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "malt.json")
	content := `{
  "rpc": {
    "listen": "127.0.0.1:9999",
    "cors_allowed_origins": []
  }
}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile() error = %v", err)
	}

	if len(cfg.RPC.CORSAllowedOrigins) == 0 {
		t.Fatal("RPC.CORSAllowedOrigins should include browser app defaults")
	}
	if !slices.Contains(cfg.RPC.CORSAllowedOrigins, "https://dewebprotocol.dev") {
		t.Fatalf("RPC.CORSAllowedOrigins = %v, want official web origin", cfg.RPC.CORSAllowedOrigins)
	}
	if !slices.Contains(cfg.RPC.CORSAllowedOrigins, "http://127.0.0.1:5173") {
		t.Fatalf("RPC.CORSAllowedOrigins = %v, want local dev origin", cfg.RPC.CORSAllowedOrigins)
	}
	if !slices.Contains(cfg.RPC.CORSAllowedOrigins, "http://127.0.0.1:5174") {
		t.Fatalf("RPC.CORSAllowedOrigins = %v, want local fallback dev origin", cfg.RPC.CORSAllowedOrigins)
	}
}

func TestLoadFromFile_NewSchema(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "malt.json")
	content := `{
  "rpc": {
    "listen": "127.0.0.1:9999",
    "cors_allowed_origins": ["http://localhost:5173", "https://docs.example"]
  },
  "state": {
    "root_dir": "~/custom-state",
    "kvstore": {
      "type": "fs",
      "path": "kv-data"
    },
    "arctable": {
      "type": "overwrite"
    }
  },
  "structure": {
    "default_backend": "kzg"
  },
  "cas": {
    "mode": "external",
    "base_url": "http://127.0.0.1:5001",
    "timeout": "45s",
    "embedded_mock": {
      "enabled": false,
      "listen": "127.0.0.1:4318"
    }
  },
  "logging": {
    "level": "debug",
    "format": "text"
  }
}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)

	cfg, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile() error = %v", err)
	}

	if cfg.RPC.Listen != "127.0.0.1:9999" {
		t.Fatalf("RPC.Listen = %q", cfg.RPC.Listen)
	}
	if got := cfg.RPC.CORSAllowedOrigins; len(got) != 2 || got[0] != "http://localhost:5173" || got[1] != "https://docs.example" {
		t.Fatalf("RPC.CORSAllowedOrigins = %#v", got)
	}
	if cfg.CAS.Mode != "external" {
		t.Fatalf("CAS.Mode = %q", cfg.CAS.Mode)
	}
	if cfg.CASBaseURL() != "http://127.0.0.1:5001" {
		t.Fatalf("CASBaseURL() = %q", cfg.CASBaseURL())
	}
	if !strings.Contains(cfg.State.RootDir, "custom-state") {
		t.Fatalf("State.RootDir = %q", cfg.State.RootDir)
	}
	if cfg.KVStorePath() != filepath.Join(cfg.State.RootDir, "kv-data") {
		t.Fatalf("KVStorePath() = %q", cfg.KVStorePath())
	}
}

func TestValidateAllowsFsAndIpa(t *testing.T) {
	cfg := DefaultConfig()
	cfg.State.KVStore.Type = "fs"
	cfg.Structure.DefaultBackend = "ipa"

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestLoadFromFile_InvalidPath(t *testing.T) {
	_, err := LoadFromFile("/nonexistent/path/malt.json")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestWriteToFileRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config", "malt.json")

	cfg := DefaultConfig()
	cfg.CAS.Mode = "external"
	cfg.CAS.BaseURL = "http://127.0.0.1:5001"
	cfg.CAS.EmbeddedMock.Enabled = false

	if err := WriteToFile(path, cfg); err != nil {
		t.Fatalf("WriteToFile() error = %v", err)
	}

	loaded, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile() error = %v", err)
	}

	if loaded.CAS.Mode != "external" {
		t.Fatalf("loaded CAS.Mode = %q", loaded.CAS.Mode)
	}
	if loaded.CAS.BaseURL != "http://127.0.0.1:5001" {
		t.Fatalf("loaded CAS.BaseURL = %q", loaded.CAS.BaseURL)
	}
}

func TestValidateRequiresExternalBaseURL(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CAS.Mode = "external"
	cfg.CAS.BaseURL = ""
	cfg.CAS.EmbeddedMock.Enabled = false

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestCASTimeout(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CAS.Timeout = "45s"

	got, err := cfg.CASTimeout()
	if err != nil {
		t.Fatalf("CASTimeout() error = %v", err)
	}
	if got != 45*time.Second {
		t.Fatalf("CASTimeout() = %v", got)
	}
}

func TestEmbeddedMockLatency(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CAS.EmbeddedMock.Latency = "250ms"

	got, err := cfg.EmbeddedMockLatency()
	if err != nil {
		t.Fatalf("EmbeddedMockLatency() error = %v", err)
	}
	if got != 250*time.Millisecond {
		t.Fatalf("EmbeddedMockLatency() = %v", got)
	}
}

func TestAPIBaseURL(t *testing.T) {
	cfg := DefaultConfig()
	if got := cfg.APIBaseURL(); got != "http://127.0.0.1:4317" {
		t.Fatalf("APIBaseURL() = %q", got)
	}
}
