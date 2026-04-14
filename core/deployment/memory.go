// Package deployment provides optional helper composition roots retained for
// demos, tests, and compatibility-oriented code. These types are not the
// primary architectural center of MALT.
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

// MemoryDeployment wires an in-memory graph environment for helper code that
// still uses the Deployment compatibility abstraction.
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

// NewMemoryDeployment creates a new memory-backed compatibility deployment.
func NewMemoryDeployment(kv kvstore.KVStore) *MemoryDeployment {
	return NewMemoryDeploymentWithBackend(kv, nil)
}

// NewMemoryDeploymentWithBackend creates a memory-backed compatibility
// deployment with an explicit backend adapter.
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

	// Retain legacy storage adapters for Deployment interface compatibility.
	// The canonical graph path does not use these adapters directly.
	d.arcStore = arc.NewEATArcStore(kv)
	d.contentStore = content.NewKVStoreContentStore(kv)

	return d
}

// CreateGraph creates a Graph using the canonical graph-scoped path while
// satisfying the legacy Deployment interface.
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

// ArcStore returns the compatibility ArcStore adapter exposed by this deployment.
func (d *MemoryDeployment) ArcStore() interfaces.ArcStore {
	return d.arcStore
}

// ContentStore returns the compatibility ContentStore adapter exposed by this deployment.
func (d *MemoryDeployment) ContentStore() interfaces.ContentStore {
	return d.contentStore
}

// CommitmentBackend returns the compatibility backend adapter used by this deployment.
func (d *MemoryDeployment) CommitmentBackend() interfaces.CommitmentBackend {
	return d.backend
}

// EAT returns the EAT used by the canonical graph path in this deployment.
func (d *MemoryDeployment) EAT() eat.EAT {
	return d.eat
}

// SCE returns the SCE engine used by the canonical graph path in this deployment.
func (d *MemoryDeployment) SCE() *sce.Engine {
	return d.sce
}

// Resolver returns the interoperability-aware resolver used by this deployment.
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
