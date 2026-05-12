package store

import (
	"context"
	"errors"
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
