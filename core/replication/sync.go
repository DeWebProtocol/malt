// State synchronization between EAT+SCE units.
//
// Sync compares snapshots between a source and target KVStore and
// reconciles differences by importing missing entries into the target.

package replication

import (
	"context"
	"fmt"
	"sort"

	"github.com/dewebprotocol/malt/core/graph"
	"github.com/dewebprotocol/malt/core/kvstore"
)

// Syncer compares state between two KVStores and reconciles differences.
type Syncer struct {
	sourceKV kvstore.KVStore
	targetKV kvstore.KVStore
}

// NewSyncer creates a Syncer that compares source against target.
func NewSyncer(sourceKV, targetKV kvstore.KVStore) *Syncer {
	return &Syncer{sourceKV: sourceKV, targetKV: targetKV}
}

// DiffResult describes the differences between source and target.
type DiffResult struct {
	// MissingInTarget are keys that exist in source but not in target.
	MissingInTarget []string

	// ExtraInTarget are keys that exist in target but not in source.
	ExtraInTarget []string

	// Mismatched are keys that exist in both but have different values.
	Mismatched []string
}

// Diff compares all MALT-relevant keys between source and target.
// Returns a DiffResult describing the differences.
func (s *Syncer) Diff(ctx context.Context) (*DiffResult, error) {
	diff := &DiffResult{
		MissingInTarget: []string{},
		ExtraInTarget:   []string{},
		Mismatched:      []string{},
	}

	// Scan all MALT key prefixes
	prefixes := [][]byte{
		[]byte(EATKeySep),     // EAT entries use bucketId: prefix
		[]byte(LineagePrefix), // Lineage records
		[]byte(GraphMetaPrefix),
		[]byte(GraphIndexPrefix),
	}

	for _, prefix := range prefixes {
		// Find keys in source
		sourceKeys := s.collectKeys(ctx, prefix)

		for key := range sourceKeys {
			hasTarget, err := s.targetKV.Has(ctx, []byte(key))
			if err != nil {
				return nil, fmt.Errorf("check target key %s: %w", key, err)
			}

			if !hasTarget {
				diff.MissingInTarget = append(diff.MissingInTarget, key)
				continue
			}

			// Both have this key, compare values
			srcVal, err := s.sourceKV.Get(ctx, []byte(key))
			if err != nil {
				return nil, fmt.Errorf("get source key %s: %w", key, err)
			}
			tgtVal, err := s.targetKV.Get(ctx, []byte(key))
			if err != nil {
				return nil, fmt.Errorf("get target key %s: %w", key, err)
			}

			if string(srcVal) != string(tgtVal) {
				diff.Mismatched = append(diff.Mismatched, key)
			}
		}

		// Find keys only in target
		targetKeys := s.collectTargetKeys(ctx, prefix)
		for key := range targetKeys {
			hasSource, err := s.sourceKV.Has(ctx, []byte(key))
			if err != nil {
				return nil, fmt.Errorf("check source key %s: %w", key, err)
			}
			if !hasSource {
				diff.ExtraInTarget = append(diff.ExtraInTarget, key)
			}
		}
	}

	// Sort for deterministic output
	sort.Strings(diff.MissingInTarget)
	sort.Strings(diff.ExtraInTarget)
	sort.Strings(diff.Mismatched)

	return diff, nil
}

// SyncResult describes what was synchronized.
type SyncResult struct {
	// Imported is the number of entries imported to target.
	Imported int

	// ImportedKeys lists keys that were imported.
	ImportedKeys []string

	// Skipped is the number of keys that already matched.
	Skipped int
}

// Sync reconciles source state into target.
// It imports all missing and mismatched keys from source to target.
// Extra keys in target are left untouched (they may be from other graphs).
func (s *Syncer) Sync(ctx context.Context) (*SyncResult, error) {
	diff, err := s.Diff(ctx)
	if err != nil {
		return nil, fmt.Errorf("diff: %w", err)
	}

	result := &SyncResult{
		ImportedKeys: []string{},
	}

	// Import missing keys
	for _, key := range diff.MissingInTarget {
		val, err := s.sourceKV.Get(ctx, []byte(key))
		if err != nil {
			return result, fmt.Errorf("get source key %s: %w", key, err)
		}
		if err := s.targetKV.Put(ctx, []byte(key), val); err != nil {
			return result, fmt.Errorf("put target key %s: %w", key, err)
		}
		result.Imported++
		result.ImportedKeys = append(result.ImportedKeys, key)
	}

	// Overwrite mismatched keys
	for _, key := range diff.Mismatched {
		val, err := s.sourceKV.Get(ctx, []byte(key))
		if err != nil {
			return result, fmt.Errorf("get source key %s: %w", key, err)
		}
		if err := s.targetKV.Put(ctx, []byte(key), val); err != nil {
			return result, fmt.Errorf("put target key %s: %w", key, err)
		}
		result.Imported++
		result.ImportedKeys = append(result.ImportedKeys, key)
	}

	result.Skipped = len(diff.ExtraInTarget)

	return result, nil
}

// SyncGraphs exports a graph from source store and imports to target store.
// This is a convenience function that combines Export + Import.
func SyncGraphs(sourceKV, targetKV kvstore.KVStore, g *graph.Graph, ctx context.Context) (int, error) {
	exporter := NewExporter(sourceKV)
	snap, err := exporter.Export(ctx, g)
	if err != nil {
		return 0, fmt.Errorf("export: %w", err)
	}

	importer := NewImporter(targetKV)
	count, err := importer.Import(ctx, snap)
	if err != nil {
		return count, fmt.Errorf("import: %w", err)
	}

	return count, nil
}

// collectKeys returns all keys with the given prefix from source KVStore.
func (s *Syncer) collectKeys(ctx context.Context, prefix []byte) map[string]struct{} {
	keys := make(map[string]struct{})
	iter := s.sourceKV.NewIterator(ctx, prefix, nil)
	defer iter.Close()

	for iter.Next() {
		keys[string(iter.Key())] = struct{}{}
	}
	return keys
}

// collectTargetKeys returns all keys with the given prefix from target KVStore.
func (s *Syncer) collectTargetKeys(ctx context.Context, prefix []byte) map[string]struct{} {
	keys := make(map[string]struct{})
	iter := s.targetKV.NewIterator(ctx, prefix, nil)
	defer iter.Close()

	for iter.Next() {
		keys[string(iter.Key())] = struct{}{}
	}
	return keys
}
