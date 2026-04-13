// Package deployment provides Deployment factory implementations.
// Deployment is the composition root for MALT components.
package deployment

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt/core/interfaces"
	"github.com/dewebprotocol/malt/core/graph"
	"github.com/dewebprotocol/malt/core/graph/resolver"
	"github.com/dewebprotocol/malt/core/store/arc"
	"github.com/dewebprotocol/malt/core/store/content"
	"github.com/dewebprotocol/malt/core/commitment"
	"github.com/dewebprotocol/malt/core/kvstore"
	"github.com/dewebprotocol/malt/core/sce/commitment/kzg"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

// MemoryDeployment creates Graph with in-memory storage.
// Useful for testing and development.
type MemoryDeployment struct {
	kv          kvstore.KVStore
	arcStore    interfaces.ArcStore
	contentStore interfaces.ContentStore
	backend     interfaces.CommitmentBackend
	graph       interfaces.Graph
}

// NewMemoryDeployment creates a new memory-based deployment.
func NewMemoryDeployment(kv kvstore.KVStore) *MemoryDeployment {
	return NewMemoryDeploymentWithBackend(kv, nil)
}

// NewMemoryDeploymentWithBackend creates a new memory deployment with specific backend.
func NewMemoryDeploymentWithBackend(kv kvstore.KVStore, backend interfaces.CommitmentBackend) *MemoryDeployment {
	d := &MemoryDeployment{
		kv: kv,
	}

	// Create storage implementations
	d.arcStore = arc.NewEATArcStore(kv)
	d.contentStore = content.NewKVStoreContentStore(kv)

	// Create commitment backend if not provided
	if backend != nil {
		d.backend = backend
	} else {
		// Use KZG as default backend
		kzgScheme, err := kzg.NewScheme()
		if err != nil {
			return nil
		}
		d.backend = commitment.NewSchemeBackend(kzgScheme, "kzg")
	}

	return d
}

// CreateGraph creates a new Graph instance.
func (d *MemoryDeployment) CreateGraph() (interfaces.Graph, error) {
	if d.graph != nil {
		return d.graph, nil
	}

	// Create resolver (empty for now, will be configured later)
	hybridResolver := resolver.NewHybridResolver(nil, nil)

	// Create Graph
	d.graph = graph.NewGraph(
		d.arcStore,
		d.contentStore,
		d.backend,
		hybridResolver,
	)

	return d.graph, nil
}

// ArcStore returns the ArcStore used by this deployment.
func (d *MemoryDeployment) ArcStore() interfaces.ArcStore {
	return d.arcStore
}

// ContentStore returns the ContentStore used by this deployment.
func (d *MemoryDeployment) ContentStore() interfaces.ContentStore {
	return d.contentStore
}

// CommitmentBackend returns the CommitmentBackend used by this deployment.
func (d *MemoryDeployment) CommitmentBackend() interfaces.CommitmentBackend {
	return d.backend
}

// InitializeGraph creates a new empty graph and returns its initial root.
func (d *MemoryDeployment) InitializeGraph(ctx context.Context) (cid.Cid, error) {
	// Create empty arc set
	emptyArcs := arcset.NewMap()

	// Generate commitment
	root, err := d.backend.Commit(emptyArcs)
	if err != nil {
		return cid.Cid{}, fmt.Errorf("failed to create initial commitment: %w", err)
	}

	return root, nil
}

// Name returns the deployment name.
func (d *MemoryDeployment) Name() string {
	return "memory"
}

// Close releases all resources.
func (d *MemoryDeployment) Close() error {
	var errs []error

	if err := d.arcStore.Close(); err != nil {
		errs = append(errs, err)
	}
	if err := d.contentStore.Close(); err != nil {
		errs = append(errs, err)
	}
	if err := d.kv.Close(); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return fmt.Errorf("close errors: %v", errs)
	}
	return nil
}

// Ensure MemoryDeployment implements Deployment.
var _ interfaces.Deployment = (*MemoryDeployment)(nil)