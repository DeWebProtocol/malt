package metrics

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/dewebprotocol/malt/core/arctable"
	"github.com/dewebprotocol/malt/core/arctable/bloom"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

// ErrBucketCreatorUnsupported is returned when a wrapped ArcTable cannot create buckets.
var ErrBucketCreatorUnsupported = errors.New("wrapped arctable does not support bucket creation")

// ArcTableStats is a point-in-time snapshot of ArcTable operation counters.
type ArcTableStats struct {
	GetCount          uint64
	BatchGetCount     uint64
	BatchGetPathCount uint64
	UpdateCount       uint64
	UpdateArcCount    uint64
	SnapshotCount     uint64
	SnapshotArcCount  uint64
	IterateCount      uint64
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

// SnapshotStats returns the current ArcTable counters.
func (m *ArcTable) SnapshotStats() ArcTableStats {
	return m.stats.snapshot()
}

// ResetStats clears all ArcTable counters.
func (m *ArcTable) ResetStats() {
	m.stats.reset()
}

// Get retrieves the target CID for (bucketId, root, path).
func (m *ArcTable) Get(ctx context.Context, bucketId string, root cid.Cid, path arcset.Path) (cid.Cid, error) {
	m.stats.getCount.Add(1)
	return m.base.Get(ctx, bucketId, root, path)
}

// BatchGet retrieves multiple target CIDs in a single operation.
func (m *ArcTable) BatchGet(ctx context.Context, bucketId string, root cid.Cid, paths []arcset.Path) (map[arcset.Path]cid.Cid, error) {
	m.stats.batchGetCount.Add(1)
	m.stats.batchGetPathCount.Add(uint64(len(paths)))
	return m.base.BatchGet(ctx, bucketId, root, paths)
}

// Update stores arc entries with a new commitment root.
func (m *ArcTable) Update(ctx context.Context, bucketId string, newRoot, oldRoot cid.Cid, arcs arcset.ArcSet) error {
	m.stats.updateCount.Add(1)
	if arcs != nil {
		m.stats.updateArcCount.Add(uint64(arcs.Len()))
	}
	return m.base.Update(ctx, bucketId, newRoot, oldRoot, arcs)
}

// Snapshot returns an immutable snapshot of all arcs for a given root.
func (m *ArcTable) Snapshot(ctx context.Context, bucketId string, root cid.Cid) (arcset.ArcSet, error) {
	m.stats.snapshotCount.Add(1)
	arcs, err := m.base.Snapshot(ctx, bucketId, root)
	if err == nil && arcs != nil {
		m.stats.snapshotArcCount.Add(uint64(arcs.Len()))
	}
	return arcs, err
}

// Iterate returns a streaming iterator over arcs for a given root.
func (m *ArcTable) Iterate(ctx context.Context, bucketId string, root cid.Cid) arcset.Iterator {
	m.stats.iterateCount.Add(1)
	return m.base.Iterate(ctx, bucketId, root)
}

// Close releases resources.
func (m *ArcTable) Close() error {
	return m.base.Close()
}

// CreateBucket forwards bucket creation when the wrapped ArcTable supports it.
func (m *ArcTable) CreateBucket(ctx context.Context, bucketId string, cfg *bloom.BucketConfig) error {
	creator, ok := m.base.(arctable.BucketCreator)
	if !ok {
		return ErrBucketCreatorUnsupported
	}
	return creator.CreateBucket(ctx, bucketId, cfg)
}

var _ arctable.ArcTable = (*ArcTable)(nil)
var _ arctable.BucketCreator = (*ArcTable)(nil)
