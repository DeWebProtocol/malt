package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRemembersExplicitSettingsPath(t *testing.T) {
	tmp := t.TempDir()
	settingsPath := filepath.Join(tmp, "custom-settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{
  "listen": "127.0.0.1:4999",
  "kvstore": {
    "type": "memory"
  }
}`), 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	cfg, err := Load(settingsPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := cfg.SettingsPath(); got != settingsPath {
		t.Fatalf("SettingsPath() = %q, want %q", got, settingsPath)
	}
}

func TestValidateRejectsInvalidListenAddress(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Listen = "127.0.0.1"

	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("Validate error = %v, want listen validation error", err)
	}
}

func TestResolvePathsReportHomeDirErrors(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")

	cfg := DefaultConfig()
	cfg.KVStore.DataDir = "data"
	if _, err := cfg.ResolveKVStorePath(); err == nil {
		t.Fatal("ResolveKVStorePath should report missing home directory")
	}
	if _, err := cfg.ResolveSettingsPath(); err == nil {
		t.Fatal("ResolveSettingsPath should report missing home directory")
	}
}
