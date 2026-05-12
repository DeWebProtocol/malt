package evalwrite

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt/cmd/eval/helper/replay"
	evalstore "github.com/dewebprotocol/malt/cmd/eval/helper/store"
)

func TestBuildSystemsUsesIsolatedStoresByDefault(t *testing.T) {
	ctx := context.Background()
	factory, err := evalstore.NewFactory(evalstore.FactoryConfig{
		Mode:    evalstore.StoreModeIsolated,
		Backend: evalstore.StoreBackendMemory,
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	t.Cleanup(func() { _ = factory.Close() })

	systems, err := BuildSystems(ctx, factory, "maltflat,merkledag,hamt")
	if err != nil {
		t.Fatalf("BuildSystems: %v", err)
	}
	names := systemNames(systems)
	want := []string{"maltflat", "merkledag", "hamt"}
	if len(names) != len(want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names = %v, want %v", names, want)
		}
	}
}

func TestBuildSystemsRejectsUnknownSystem(t *testing.T) {
	ctx := context.Background()
	factory, err := evalstore.NewFactory(evalstore.FactoryConfig{Backend: evalstore.StoreBackendMemory})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	t.Cleanup(func() { _ = factory.Close() })
	if _, err := BuildSystems(ctx, factory, "maltflat,nope"); err == nil {
		t.Fatal("expected unknown system error")
	}
}

func systemNames(systems []replay.SystemAdapter) []string {
	names := make([]string, len(systems))
	for i, system := range systems {
		names[i] = system.Name()
	}
	return names
}
