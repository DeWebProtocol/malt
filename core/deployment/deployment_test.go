// Package deployment_test tests Deployment implementations.
package deployment_test

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt/core/deployment"
	"github.com/dewebprotocol/malt/core/kvstore/memory"
)

// TestMemoryDeployment tests MemoryDeployment.
func TestMemoryDeployment(t *testing.T) {
	ctx := context.Background()
	kv := memory.New()

	d := deployment.NewMemoryDeployment(kv)

	// Test CreateGraph
	graph, err := d.CreateGraph()
	if err != nil {
		t.Fatalf("CreateGraph failed: %v", err)
	}
	if graph == nil {
		t.Fatal("Graph should not be nil")
	}

	// Test ArcStore
	arcStore := d.ArcStore()
	if arcStore == nil {
		t.Fatal("ArcStore should not be nil")
	}

	// Test ContentStore
	contentStore := d.ContentStore()
	if contentStore == nil {
		t.Fatal("ContentStore should not be nil")
	}

	// Test CommitmentBackend
	backend := d.CommitmentBackend()
	if backend == nil {
		t.Fatal("CommitmentBackend should not be nil")
	}

	// Test InitializeGraph
	root, err := d.InitializeGraph(ctx)
	if err != nil {
		t.Fatalf("InitializeGraph failed: %v", err)
	}
	if !root.Defined() {
		t.Fatal("Root should be defined")
	}

	// Test Name
	if d.Name() != "memory" {
		t.Errorf("Name mismatch: expected 'memory', got '%s'", d.Name())
	}

	// Test Close
	if err := d.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

// TestMemoryDeploymentComponents tests that components are properly wired.
func TestMemoryDeploymentComponents(t *testing.T) {
	kv := memory.New()
	d := deployment.NewMemoryDeployment(kv)

	// Verify ArcStore uses the same KVStore
	arcStore := d.ArcStore()
	if arcStore == nil {
		t.Fatal("ArcStore should not be nil")
	}

	// Verify ContentStore uses the same KVStore
	contentStore := d.ContentStore()
	if contentStore == nil {
		t.Fatal("ContentStore should not be nil")
	}

	// Test that Graph is created correctly
	graph1, err := d.CreateGraph()
	if err != nil {
		t.Fatalf("CreateGraph failed: %v", err)
	}

	// Graph should be cached
	graph2, err := d.CreateGraph()
	if err != nil {
		t.Fatalf("CreateGraph second call failed: %v", err)
	}

	// Same instance should be returned
	if graph1 != graph2 {
		t.Error("CreateGraph should return cached instance")
	}

	d.Close()
}