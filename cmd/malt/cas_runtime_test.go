package main

import (
	"bytes"
	"path/filepath"
	"testing"

	clientconfig "github.com/dewebprotocol/malt-client/internal/config"
	localtransport "github.com/dewebprotocol/malt-client/transport/local"
)

func TestMakeCASClientSupportsLocalOnlyUseWithoutGateway(t *testing.T) {
	root := t.TempDir()
	cfg, err := clientconfig.Default()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Transport.CASPolicy = clientconfig.CASPolicyLocal
	cfg.Transport.LocalCASDir = filepath.Join(root, "local-cas")
	configPath := filepath.Join(root, "config.json")
	if err := clientconfig.Write(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	previous := cfgFile
	cfgFile = configPath
	defer func() { cfgFile = previous }()

	if _, err := makeCASClient(true); err == nil {
		t.Fatal("local-only CAS satisfied a managed Gateway operation")
	}
	blocks, err := makeCASClient(false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blocks.Close() })
	body := []byte("local-only CLI block")
	key, err := blocks.Put(t.Context(), body)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := localtransport.Open(localtransport.Options{Directory: cfg.Transport.LocalCASDir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, err := reopened.Get(t.Context(), key)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("local-only persisted body = %q, %v", got, err)
	}
}
