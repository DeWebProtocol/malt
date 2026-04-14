// Package deployment provides Deployment factory implementations.
// Deployment is the composition root for MALT components.
package deployment

import (
	"context"
	"fmt"

	commitpkg "github.com/dewebprotocol/malt/core/commitment"
	"github.com/dewebprotocol/malt/core/eat"
	"github.com/dewebprotocol/malt/core/eat/overwrite"
	"github.com/dewebprotocol/malt/core/graph"
	"github.com/dewebprotocol/malt/core/interfaces"
	"github.com/dewebprotocol/malt/core/kvstore"
	"github.com/dewebprotocol/malt/core/resolver"
	"github.com/dewebprotocol/malt/core/sce"
	"github.com/dewebprotocol/malt/core/sce/commitment"
	"github.com/dewebprotocol/malt/core/sce/commitment/kzg"
	"github.com/dewebprotocol/malt/core/store/arc"
	"github.com/dewebprotocol/malt/core/store/content"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

// MemoryDeployment creates Graph with in-memory storage.
// Useful for testing and development.
type MemoryDeployment struct {
	kv           kvstore.KVStore
	eat          eat.EAT
	sce          *sce.Engine
	resolver     *resolver.Resolver
	arcStore     interfaces.ArcStore
	contentStore interfaces.ContentStore
	backend      interfaces.CommitmentBackend
	graph        interfaces.Graph
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

	// Create EAT
	e, err := overwrite.NewEAT(overwrite.WithKVStore(kv))
	if err != nil {
		return nil
	}
	d.eat = e

	// Create SCE
	var scheme commitment.Scheme
	if backend != nil {
		d.backend = backend
		// Extract scheme from backend if needed, use KZG as fallback
		kzgScheme, err := kzg.NewScheme()
		if err == nil {
			scheme = kzgScheme
		}
	} else {
		kzgScheme, err := kzg.NewScheme()
		if err != nil {
			return nil
		}
		scheme = kzgScheme
		d.backend = commitpkg.NewSchemeBackend(kzgScheme, "kzg")
	}
	d.sce = sce.NewEngine(scheme)

	// Create storage implementations (for Deployment interface compliance)
	// Note: Graph no longer uses these directly — it delegates to resolver (EAT) and writer (SCE+EAT).
	// These are retained for interface compliance and standalone access.
	d.arcStore = arc.NewEATArcStore(kv)
	d.contentStore = content.NewKVStoreContentStore(kv)

	return d
}

// CreateGraph creates a new Graph instance with its own per-graph components.
func (d *MemoryDeployment) CreateGraph() (interfaces.Graph, error) {
	if d.graph != nil {
		return d.graph, nil
	}

	g, err := graph.NewGraph("memory", d.eat, nil, // No CAS in memory
		graph.WithBucketId("default"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create graph: %w", err)
	}

	d.graph = g
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

// EAT returns the EAT used by this deployment.
func (d *MemoryDeployment) EAT() eat.EAT {
	return d.eat
}

// SCE returns the SCE engine used by this deployment.
func (d *MemoryDeployment) SCE() *sce.Engine {
	return d.sce
}

// Resolver returns the hybrid resolver.
func (d *MemoryDeployment) Resolver() *resolver.Resolver {
	return d.resolver
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

	if d.eat != nil {
		if err := d.eat.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if d.arcStore != nil {
		if err := d.arcStore.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if d.contentStore != nil {
		if err := d.contentStore.Close(); err != nil {
			errs = append(errs, err)
		}
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
