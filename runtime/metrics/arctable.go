package metrics

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/runtime/arctable"
	"github.com/dewebprotocol/malt/runtime/arctable/bloom"
	cid "github.com/ipfs/go-cid"
)

// ErrNamespaceCreatorUnsupported is returned when a wrapped ArcTable cannot create namespaces.
var ErrNamespaceCreatorUnsupported = errors.New("wrapped arctable does not support namespace creation")

// ErrParentLookupUnsupported is returned when a wrapped ArcTable cannot read root parents.
var ErrParentLookupUnsupported = errors.New("wrapped arctable does not support parent lookup")

// ArcTableStats is a point-in-time snapshot of ArcTable operation counters.
type ArcTableStats struct {
	GetCount          uint64 `json:"get_count"`
	BatchGetCount     uint64 `json:"batch_get_count"`
	BatchGetPathCount uint64 `json:"batch_get_path_count"`
	UpdateCount       uint64 `json:"update_count"`
	UpdateArcCount    uint64 `json:"update_arc_count"`
	SnapshotCount     uint64 `json:"snapshot_count"`
	SnapshotArcCount  uint64 `json:"snapshot_arc_count"`
	IterateCount      uint64 `json:"iterate_count"`
}

type arcTableStatsRecorder struct {
	getCount          atomic.Uint64
	batchGetCount     atomic.Uint64
	batchGetPathCount atomic.Uint64
	updateCount       atomic.Uint64
	updateArcCount    atomic.Uint64
	snapshotCount     atomic.Uint64
	snapshotArcCount  atomic.Uint64
	iterateCount      atomic.Uint64
}

func (r *arcTableStatsRecorder) snapshot() ArcTableStats {
	return ArcTableStats{
		GetCount:          r.getCount.Load(),
		BatchGetCount:     r.batchGetCount.Load(),
		BatchGetPathCount: r.batchGetPathCount.Load(),
		UpdateCount:       r.updateCount.Load(),
		UpdateArcCount:    r.updateArcCount.Load(),
		SnapshotCount:     r.snapshotCount.Load(),
		SnapshotArcCount:  r.snapshotArcCount.Load(),
		IterateCount:      r.iterateCount.Load(),
	}
}

func (r *arcTableStatsRecorder) reset() {
	r.getCount.Store(0)
	r.batchGetCount.Store(0)
	r.batchGetPathCount.Store(0)
	r.updateCount.Store(0)
	r.updateArcCount.Store(0)
	r.snapshotCount.Store(0)
	r.snapshotArcCount.Store(0)
	r.iterateCount.Store(0)
}

// ArcTable wraps an arctable.ArcTable and records evaluation counters.
type ArcTable struct {
	base  arctable.ArcTable
	stats arcTableStatsRecorder
}

// NewArcTable wraps base with ArcTable operation counters.
func NewArcTable(base arctable.ArcTable) *ArcTable {
	return &ArcTable{base: base}
}

// Base returns the wrapped ArcTable implementation.
func (m *ArcTable) Base() arctable.ArcTable {
	return m.base
}

// SupportsConcurrentBranches forwards branching capability to the wrapped
// ArcTable when it advertises MVCC-style children from the same parent root.
func (m *ArcTable) SupportsConcurrentBranches() bool {
	branching, ok := m.base.(arctable.BranchingArcTable)
	return ok && branching.SupportsConcurrentBranches()
}

// SnapshotStats returns the current ArcTable counters.
func (m *ArcTable) SnapshotStats() ArcTableStats {
	return m.stats.snapshot()
}

// ResetStats clears all ArcTable counters.
func (m *ArcTable) ResetStats() {
	m.stats.reset()
}

// Get retrieves the target CID for (namespace, root, path).
func (m *ArcTable) Get(ctx context.Context, namespace string, root cid.Cid, path arcset.Path) (cid.Cid, error) {
	m.stats.getCount.Add(1)
	return m.base.Get(ctx, namespace, root, path)
}

// BatchGet retrieves multiple target CIDs in a single operation.
func (m *ArcTable) BatchGet(ctx context.Context, namespace string, root cid.Cid, paths []arcset.Path) (map[arcset.Path]cid.Cid, error) {
	m.stats.batchGetCount.Add(1)
	m.stats.batchGetPathCount.Add(uint64(len(paths)))
	return m.base.BatchGet(ctx, namespace, root, paths)
}

// Update stores arc entries with a new commitment root.
func (m *ArcTable) Update(ctx context.Context, namespace string, newRoot, oldRoot cid.Cid, arcs arcset.ArcSet) error {
	m.stats.updateCount.Add(1)
	if arcs != nil {
		m.stats.updateArcCount.Add(uint64(arcs.Len()))
	}
	return m.base.Update(ctx, namespace, newRoot, oldRoot, arcs)
}

// Snapshot returns an immutable snapshot of all arcs for a given root.
func (m *ArcTable) Snapshot(ctx context.Context, namespace string, root cid.Cid) (arcset.ArcSet, error) {
	m.stats.snapshotCount.Add(1)
	arcs, err := m.base.Snapshot(ctx, namespace, root)
	if err == nil && arcs != nil {
		m.stats.snapshotArcCount.Add(uint64(arcs.Len()))
	}
	return arcs, err
}

// Iterate returns a streaming iterator over arcs for a given root.
func (m *ArcTable) Iterate(ctx context.Context, namespace string, root cid.Cid) arcset.Iterator {
	m.stats.iterateCount.Add(1)
	return m.base.Iterate(ctx, namespace, root)
}

// Close releases resources.
func (m *ArcTable) Close() error {
	return m.base.Close()
}

// CreateNamespace forwards namespace creation when the wrapped ArcTable supports it.
func (m *ArcTable) CreateNamespace(ctx context.Context, namespace string, cfg *bloom.NamespaceConfig) error {
	creator, ok := m.base.(arctable.NamespaceCreator)
	if !ok {
		return ErrNamespaceCreatorUnsupported
	}
	return creator.CreateNamespace(ctx, namespace, cfg)
}

// GetParent forwards version-parent lookups when the wrapped ArcTable supports it.
func (m *ArcTable) GetParent(ctx context.Context, namespace string, version cid.Cid) (cid.Cid, error) {
	reader, ok := m.base.(interface {
		GetParent(context.Context, string, cid.Cid) (cid.Cid, error)
	})
	if !ok {
		return cid.Undef, ErrParentLookupUnsupported
	}
	return reader.GetParent(ctx, namespace, version)
}

var _ arctable.ArcTable = (*ArcTable)(nil)
var _ arctable.NamespaceCreator = (*ArcTable)(nil)
var _ arctable.BranchingArcTable = (*ArcTable)(nil)
