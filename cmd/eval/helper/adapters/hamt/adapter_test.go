package hamt_test

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt/cmd/eval/helper/adapters/hamt"
	evalstore "github.com/dewebprotocol/malt/cmd/eval/helper/store"
)

func TestNewUsesHAMTSystemName(t *testing.T) {
	ctx := context.Background()
	factory, err := evalstore.NewFactory(evalstore.FactoryConfig{Backend: evalstore.StoreBackendMemory})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	t.Cleanup(func() { _ = factory.Close() })
	system, err := factory.NewSystem(ctx, "hamt")
	if err != nil {
		t.Fatalf("NewSystem: %v", err)
	}
	adapter := hamt.New(system)
	if adapter.Name() != "hamt" {
		t.Fatalf("adapter name = %q, want hamt", adapter.Name())
	}
}
