package main

import (
	"os"
	"path/filepath"
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
