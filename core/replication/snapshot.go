// Package replication provides graph-level snapshot export/import
// and state synchronization between replicated EAT and semantic state units.
//
// Replication in MALT operates at the KVStore level, exporting all
// keys associated with a graph's bucketId and lineage records,
// then importing them into a target KVStore on another node.
//
// Key concepts:
//   - Snapshot: a portable, self-contained archive of a graph's state
//   - Export: read source KVStore and create a snapshot
//   - Import: write snapshot data to target KVStore
//   - Sync: compare two nodes' states and reconcile differences
package replication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dewebprotocol/malt/core/graph"
	"github.com/dewebprotocol/malt/core/kvstore"
	cid "github.com/ipfs/go-cid"
)

// SnapshotVersion is the current snapshot format version.
const SnapshotVersion uint32 = 1

// Key prefixes used by MALT components.
const (
	GraphMetaPrefix  = "graph/meta/"
	GraphIndexPrefix = "graph/index/"
	LineagePrefix    = "lineage/"
	EATKeySep        = ":"
)

// Snapshot is a portable archive of a graph's complete state.
// It contains all EAT entries, lineage records, and graph metadata
// needed to reconstruct the graph on another node.
type Snapshot struct {
	// Version is the snapshot format version.
	Version uint32 `json:"version"`

	// GraphID is the unique identifier of the graph.
	GraphID string `json:"graph_id"`

	// RootCID is the current structure root.
	RootCID string `json:"root_cid"`

	// ArcCount is the number of arcs in the graph.
	ArcCount int `json:"arc_count"`

	// Backend is the commitment scheme used (kzg/ipa).
	Backend string `json:"backend"`

	// EATType is the EAT implementation used (overwrite/versioned).
	EATType string `json:"eat_type"`

	// CreatedAt is when the snapshot was taken.
	CreatedAt time.Time `json:"created_at"`

	// EATEntries contains all EAT key-value pairs for this graph.
	// Keys are in the format used by the specific EAT implementation.
	EATEntries map[string][]byte `json:"eat_entries,omitempty"`

	// LineageEntries contains all lineage records as JSON.
	// Keys are "lineage/<root_cid>".
	LineageEntries map[string][]byte `json:"lineage_entries,omitempty"`

	// COWEntries contains all copy-on-write shortcut entries.
	// Keys are "lineage/cow/<parent>/<child>".
	COWEntries map[string][]byte `json:"cow_entries,omitempty"`

	// Checksum is the SHA2-256 hex digest of all entries for integrity verification.
	Checksum string `json:"checksum"`
}

// Exporter exports graph snapshots from a source KVStore.
type Exporter struct {
	kv kvstore.KVStore
}

// NewExporter creates a new Exporter backed by the given KVStore.
func NewExporter(kv kvstore.KVStore) *Exporter {
	return &Exporter{kv: kv}
}

// Export exports the complete state of a graph as a Snapshot.
// It extracts all EAT entries, lineage records, and graph metadata.
func (e *Exporter) Export(ctx context.Context, g *graph.GraphMeta) (*Snapshot, error) {
	snap := &Snapshot{
		Version:        SnapshotVersion,
		GraphID:        g.ID,
		RootCID:        g.Root.String(),
		ArcCount:       g.ArcCount,
		Backend:        g.Backend,
		EATType:        g.EATType,
		CreatedAt:      time.Now(),
		EATEntries:     make(map[string][]byte),
		LineageEntries: make(map[string][]byte),
		COWEntries:     make(map[string][]byte),
	}

	// Export EAT entries for this graph's bucket
	// EAT keys use bucketId as namespace prefix
	eatPrefix := []byte(g.ID + EATKeySep)
	e.exportKeys(ctx, eatPrefix, snap.EATEntries)

	// Export lineage records
	e.exportKeys(ctx, []byte(LineagePrefix), snap.LineageEntries)

	// Export COW shortcuts
	e.exportKeys(ctx, []byte(LineagePrefix+"cow/"), snap.COWEntries)

	// Compute checksum
	if err := snap.computeChecksum(); err != nil {
		return nil, fmt.Errorf("compute checksum: %w", err)
	}

	return snap, nil
}

// ExportAll exports snapshots for all active and frozen graphs.
func (e *Exporter) ExportAll(ctx context.Context, store *graph.Store) ([]*Snapshot, error) {
	graphs, err := store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list graphs: %w", err)
	}

	var snapshots []*Snapshot
	for _, g := range graphs {
		if g.IsDeleted() {
			continue
		}
		snap, err := e.Export(ctx, g)
		if err != nil {
			return nil, fmt.Errorf("export graph %s: %w", g.ID, err)
		}
		snapshots = append(snapshots, snap)
	}

	return snapshots, nil
}

// exportKeys extracts all keys with the given prefix and copies their values to dst.
func (e *Exporter) exportKeys(ctx context.Context, prefix []byte, dst map[string][]byte) {
	iter := e.kv.NewIterator(ctx, prefix, nil)
	defer iter.Close()

	for iter.Next() {
		key := string(iter.Key())
		dst[key] = make([]byte, len(iter.Value()))
		copy(dst[key], iter.Value())
	}
}

// computeChecksum computes the SHA2-256 checksum of all entry data.
func (s *Snapshot) computeChecksum() error {
	data, err := json.Marshal(s.checksumPayload())
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	hash := sha256.Sum256(data)
	s.Checksum = hex.EncodeToString(hash[:])
	return nil
}

// checksumPayload returns the data to checksum (excluding the checksum field itself).
func (s *Snapshot) checksumPayload() interface{} {
	return struct {
		EATEntries     map[string][]byte `json:"eat_entries"`
		LineageEntries map[string][]byte `json:"lineage_entries"`
		COWEntries     map[string][]byte `json:"cow_entries"`
	}{
		EATEntries:     s.EATEntries,
		LineageEntries: s.LineageEntries,
		COWEntries:     s.COWEntries,
	}
}

// VerifyChecksum verifies the snapshot's integrity checksum.
func (s *Snapshot) VerifyChecksum() error {
	return verifyChecksum(s)
}

// Marshal serializes the snapshot to compact JSON.
func (s *Snapshot) Marshal() ([]byte, error) {
	return json.Marshal(s)
}

// Unmarshal deserializes the snapshot from JSON bytes.
func Unmarshal(data []byte) (*Snapshot, error) {
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("unmarshal snapshot: %w", err)
	}
	return &s, nil
}

// Importer imports graph snapshots into a target KVStore.
type Importer struct {
	kv kvstore.KVStore
}

// NewImporter creates a new Importer backed by the given KVStore.
func NewImporter(kv kvstore.KVStore) *Importer {
	return &Importer{kv: kv}
}

// Import imports a snapshot into the target KVStore.
// It writes all EAT entries, lineage records, and graph metadata.
// Returns the number of entries imported.
func (imp *Importer) Import(ctx context.Context, snap *Snapshot) (int, error) {
	// Verify checksum before importing
	if err := verifyChecksum(snap); err != nil {
		return 0, fmt.Errorf("snapshot integrity check failed: %w", err)
	}

	count := 0

	// Import EAT entries
	for key, value := range snap.EATEntries {
		if err := imp.kv.Put(ctx, []byte(key), value); err != nil {
			return count, fmt.Errorf("import EAT entry %s: %w", key, err)
		}
		count++
	}

	// Import lineage entries
	for key, value := range snap.LineageEntries {
		if err := imp.kv.Put(ctx, []byte(key), value); err != nil {
			return count, fmt.Errorf("import lineage entry %s: %w", key, err)
		}
		count++
	}

	// Import COW entries
	for key, value := range snap.COWEntries {
		if err := imp.kv.Put(ctx, []byte(key), value); err != nil {
			return count, fmt.Errorf("import COW entry %s: %w", key, err)
		}
		count++
	}

	// Re-create graph metadata entry
	// Parse root CID
	rootCID := cid.Undef
	if snap.RootCID != "" && snap.RootCID != cid.Undef.String() {
		var err error
		rootCID, err = cid.Decode(snap.RootCID)
		if err != nil {
			return count, fmt.Errorf("decode root CID: %w", err)
		}
	}

	g := &graph.GraphMeta{
		ID:        snap.GraphID,
		Root:      rootCID,
		ArcCount:  snap.ArcCount,
		Backend:   snap.Backend,
		EATType:   snap.EATType,
		State:     graph.StateActive,
		CreatedAt: snap.CreatedAt,
		UpdatedAt: time.Now(),
	}

	graphData, err := json.Marshal(g)
	if err != nil {
		return count, fmt.Errorf("marshal graph: %w", err)
	}

	if err := imp.kv.Put(ctx, []byte(GraphMetaPrefix+g.ID), graphData); err != nil {
		return count, fmt.Errorf("import graph metadata: %w", err)
	}
	count++

	if err := imp.kv.Put(ctx, []byte(GraphIndexPrefix+g.ID), []byte(string(g.State))); err != nil {
		return count, fmt.Errorf("import graph index: %w", err)
	}
	count++

	return count, nil
}

// verifyChecksum verifies checksum without modifying the original.
func verifyChecksum(snap *Snapshot) error {
	if snap.Checksum == "" {
		return fmt.Errorf("empty checksum")
	}
	payload := struct {
		EATEntries     map[string][]byte `json:"eat_entries"`
		LineageEntries map[string][]byte `json:"lineage_entries"`
		COWEntries     map[string][]byte `json:"cow_entries"`
	}{
		EATEntries:     snap.EATEntries,
		LineageEntries: snap.LineageEntries,
		COWEntries:     snap.COWEntries,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	hash := sha256.Sum256(data)
	expected := hex.EncodeToString(hash[:])

	if snap.Checksum != expected {
		return fmt.Errorf("checksum mismatch: got %s, expected %s", snap.Checksum, expected)
	}

	return nil
}
