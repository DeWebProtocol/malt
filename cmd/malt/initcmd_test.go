package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dewebprotocol/malt/config"
)

func TestInitDisablesBrowserCORSByDefault(t *testing.T) {
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

	if len(cfg.RPC.CORSAllowedOrigins) != 0 {
		t.Fatalf("RPC.CORSAllowedOrigins = %#v, want disabled by default", cfg.RPC.CORSAllowedOrigins)
	}
}
