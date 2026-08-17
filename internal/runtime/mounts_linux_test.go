//go:build linux

package runtime

import (
	"path/filepath"
	"testing"

	clientconfig "github.com/dewebprotocol/malt-client/internal/config"
)

func TestNewMountManagerComposesLinuxStateWithoutRemoteAccess(t *testing.T) {
	root := t.TempDir()
	cfg := &clientconfig.Config{
		Gateway: clientconfig.GatewayConfig{
			BaseURL: "https://gateway.example.test",
		},
		Daemon: clientconfig.DaemonConfig{
			StatePath: filepath.Join(root, "roots.json"),
		},
		Filesystem: clientconfig.FilesystemConfig{
			MountsPath: filepath.Join(root, "mounts.json"),
			CacheDir:   filepath.Join(root, "cache"),
		},
	}
	manager, err := NewMountManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	statuses, err := manager.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 0 {
		t.Fatalf("fresh manager statuses=%#v", statuses)
	}
	if err := manager.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}
