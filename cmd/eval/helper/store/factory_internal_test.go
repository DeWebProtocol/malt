package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestNewSystemChecksCanceledContextBeforeAllocatingStores(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	factory, err := NewFactory(FactoryConfig{Backend: StoreBackendMemory})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}

	_, err = factory.NewSystem(ctx, "maltflat")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("NewSystem error = %v, want context.Canceled", err)
	}
	if len(factory.closeTargets) != 0 {
		t.Fatalf("close target count = %d, want 0", len(factory.closeTargets))
	}
}

func TestNewSystemCreatesUnmeteredCacheKVForPersistentBackends(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	factory, err := NewFactory(FactoryConfig{
		Backend: StoreBackendBadger,
		RootDir: root,
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer factory.Close()

	system, err := factory.NewSystem(ctx, "maltflat")
	if err != nil {
		t.Fatalf("NewSystem: %v", err)
	}
	if system.CacheKV == nil {
		t.Fatal("CacheKV is nil")
	}
	if err := system.CacheKV.Put(ctx, []byte("cache-key"), []byte("cache-value")); err != nil {
		t.Fatalf("cache put: %v", err)
	}
	if got, err := system.CacheKV.Get(ctx, []byte("cache-key")); err != nil || string(got) != "cache-value" {
		t.Fatalf("cache get = %q, %v", got, err)
	}
	snapshot := system.Meter.Snapshot()
	if snapshot.Total.NewPersistedBytes != 0 {
		t.Fatalf("cache writes should be unmetered, accounting = %+v", snapshot)
	}
	if _, err := os.Stat(filepath.Join(root, "maltflat", "cache")); err != nil {
		t.Fatalf("badger cache directory missing: %v", err)
	}
}
