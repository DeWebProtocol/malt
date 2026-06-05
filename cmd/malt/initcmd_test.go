package main

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dewebprotocol/malt/config"
)

func TestInitWritesRecommendedBrowserCORSOrigins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)

	oldCfgFile := cfgFile
	oldInitForce := initForce
	oldInitNonInteractive := initNonInteractive
	oldInitStateRoot := initStateRoot
	oldInitRPCListen := initRPCListen
	oldInitCASBaseURL := initCASBaseURL
	oldInitKVStoreType := initKVStoreType
	t.Cleanup(func() {
		cfgFile = oldCfgFile
		initForce = oldInitForce
		initNonInteractive = oldInitNonInteractive
		initStateRoot = oldInitStateRoot
		initRPCListen = oldInitRPCListen
		initCASBaseURL = oldInitCASBaseURL
		initKVStoreType = oldInitKVStoreType
	})

	cfgFile = filepath.Join(home, "malt.json")
	initForce = false
	initNonInteractive = true
	initStateRoot = ""
	initRPCListen = ""
	initCASBaseURL = ""
	initKVStoreType = ""

	if err := runInit(testCommandWithContext(context.Background()), nil); err != nil {
		t.Fatalf("runInit() error = %v", err)
	}

	cfg, err := config.LoadFromFile(cfgFile)
	if err != nil {
		t.Fatalf("LoadFromFile() error = %v", err)
	}

	want := []string{
		"https://dewebprotocol.dev",
		"https://dewebprotocol.github.io",
		"http://localhost:*",
		"http://127.0.0.1:*",
		"http://[::1]:*",
	}
	if !slices.Equal(cfg.RPC.CORSAllowedOrigins, want) {
		t.Fatalf("RPC.CORSAllowedOrigins = %#v, want %#v", cfg.RPC.CORSAllowedOrigins, want)
	}
}
