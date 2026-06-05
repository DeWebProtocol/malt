package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInitUsesExplicitConfigPath(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")

	oldConfigFile := configFile
	oldInitForce := initForce
	oldInitNonInteractive := initNonInteractive
	oldInitListen := initListen
	oldInitKVStoreType := initKVStoreType
	oldInitDataDir := initDataDir
	t.Cleanup(func() {
		configFile = oldConfigFile
		initForce = oldInitForce
		initNonInteractive = oldInitNonInteractive
		initListen = oldInitListen
		initKVStoreType = oldInitKVStoreType
		initDataDir = oldInitDataDir
	})

	configFile = filepath.Join(tmp, "custom", "settings.json")
	initForce = false
	initNonInteractive = true
	initListen = ""
	initKVStoreType = "memory"
	initDataDir = ""

	if err := runInit(nil, nil); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	if _, err := os.Stat(configFile); err != nil {
		t.Fatalf("explicit config path was not written: %v", err)
	}

	defaultPath := filepath.Join(home, ".malt", "cas", "settings.json")
	if _, err := os.Stat(defaultPath); !os.IsNotExist(err) {
		t.Fatalf("default config path should not be written when --config is explicit, stat err=%v", err)
	}
}
