package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dewebprotocol/malt/storage/kv"
	kvbadger "github.com/dewebprotocol/malt/storage/kv/badger"
	kvfs "github.com/dewebprotocol/malt/storage/kv/fs"
	kvmemory "github.com/dewebprotocol/malt/storage/kv/memory"
)

// StoreMode controls whether evaluated systems share CAS state.
type StoreMode string

const (
	StoreModeIsolated StoreMode = "isolated"
	StoreModeShared   StoreMode = "shared"
)

// StoreBackend controls the KV implementation used by evaluator storage.
type StoreBackend string

const (
	StoreBackendMemory StoreBackend = "memory"
	StoreBackendFS     StoreBackend = "fs"
	StoreBackendBadger StoreBackend = "badger"
)

// FactoryConfig configures per-system evaluation stores.
type FactoryConfig struct {
	Mode    StoreMode
	Backend StoreBackend
	RootDir string
}

// Factory creates system-local KV/CAS environments.
type Factory struct {
	cfg          FactoryConfig
	tempRoot     string
	sharedCASKV  kvstore.KVStore
	closeTargets []kvstore.KVStore
}

// System is one evaluated system's independent storage environment.
type System struct {
	Name    string
	StateKV kvstore.KVStore
	CAS     *MeteredCAS
	Meter   *Meter
}

// NewFactory creates a store factory.
func NewFactory(cfg FactoryConfig) (*Factory, error) {
	if cfg.Mode == "" {
		cfg.Mode = StoreModeIsolated
	}
	if cfg.Backend == "" {
		cfg.Backend = StoreBackendMemory
	}
	if cfg.Mode != StoreModeIsolated && cfg.Mode != StoreModeShared {
		return nil, fmt.Errorf("unsupported store mode %q", cfg.Mode)
	}
	if cfg.Backend != StoreBackendMemory && cfg.Backend != StoreBackendFS && cfg.Backend != StoreBackendBadger {
		return nil, fmt.Errorf("unsupported store backend %q", cfg.Backend)
	}
	f := &Factory{cfg: cfg}
	if cfg.Backend != StoreBackendMemory && cfg.RootDir == "" {
		root, err := os.MkdirTemp("", "malt-eval-store-*")
		if err != nil {
			return nil, err
		}
		f.tempRoot = root
		f.cfg.RootDir = root
	}
	return f, nil
}

// NewSystem creates a storage environment for one evaluated system.
func (f *Factory) NewSystem(ctx context.Context, name string) (*System, error) {
	if name == "" {
		return nil, fmt.Errorf("system name is empty")
	}
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	meter := NewMeter()
	stateKV, err := f.newKV(name, "state")
	if err != nil {
		return nil, err
	}
	var casKV kvstore.KVStore
	if f.cfg.Mode == StoreModeShared {
		if f.sharedCASKV == nil {
			f.sharedCASKV, err = f.newKV("_shared", "cas")
			if err != nil {
				_ = stateKV.Close()
				return nil, err
			}
		}
		casKV = f.sharedCASKV
	} else {
		casKV, err = f.newKV(name, "cas")
		if err != nil {
			_ = stateKV.Close()
			return nil, err
		}
	}
	return &System{
		Name:    name,
		StateKV: NewMeteredKV(stateKV, meter, CategoryArcTable),
		CAS:     NewMeteredCAS(casKV, meter),
		Meter:   meter,
	}, nil
}

// Close releases all stores created by the factory.
func (f *Factory) Close() error {
	var firstErr error
	for _, target := range f.closeTargets {
		if err := target.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if f.tempRoot != "" {
		if err := os.RemoveAll(f.tempRoot); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (f *Factory) newKV(system, role string) (kvstore.KVStore, error) {
	switch f.cfg.Backend {
	case StoreBackendMemory:
		kv := kvmemory.New()
		f.closeTargets = append(f.closeTargets, kv)
		return kv, nil
	case StoreBackendFS:
		kv, err := kvfs.New(filepath.Join(f.cfg.RootDir, system, role))
		if err != nil {
			return nil, err
		}
		f.closeTargets = append(f.closeTargets, kv)
		return kv, nil
	case StoreBackendBadger:
		kv, err := kvbadger.New(kvbadger.WithPath(filepath.Join(f.cfg.RootDir, system, role)))
		if err != nil {
			return nil, err
		}
		f.closeTargets = append(f.closeTargets, kv)
		return kv, nil
	default:
		return nil, fmt.Errorf("unsupported store backend %q", f.cfg.Backend)
	}
}
