package replication

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dewebprotocol/malt/core/graph"
	"github.com/dewebprotocol/malt/core/kvstore"
	"github.com/dewebprotocol/malt/core/kvstore/badger"
	kvmemory "github.com/dewebprotocol/malt/core/kvstore/memory"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func makeCID(n int) cid.Cid {
	data := []byte{byte(n)}
	h, _ := mh.Sum(data, mh.SHA2_256, -1)
	return cid.NewCidV1(cid.Raw, h)
}

func setupTestGraph(t *testing.T, kv kvstore.KVStore, ctx context.Context, id string) *graph.GraphMeta {
	t.Helper()
	store := graph.NewStore(kv)
	mgr := graph.NewManager(store)
	g, err := mgr.CreateGraph(ctx, id, "kzg", "overwrite")
	if err != nil {
		t.Fatalf("create graph: %v", err)
	}

	// Add some ArcTable entries
	eatKey := id + ":test/path/1"
	if err := kv.Put(ctx, []byte(eatKey), []byte("target1")); err != nil {
		t.Fatalf("put arctable entry: %v", err)
	}
	eatKey2 := id + ":test/path/2"
	if err := kv.Put(ctx, []byte(eatKey2), []byte("target2")); err != nil {
		t.Fatalf("put arctable entry: %v", err)
	}

	// Add lineage entry
	lineageKey := "lineage/root1"
	if err := kv.Put(ctx, []byte(lineageKey), []byte(`{"parent":"root0","child":"root1"}`)); err != nil {
		t.Fatalf("put lineage entry: %v", err)
	}

	// Add COW entry
	cowKey := "lineage/cow/root0/root1"
	if err := kv.Put(ctx, []byte(cowKey), []byte("shortcut")); err != nil {
		t.Fatalf("put cow entry: %v", err)
	}

	// Update graph with a root CID
	g.UpdateRoot(makeCID(42), 2)
	if err := store.Update(ctx, g); err != nil {
		t.Fatalf("update graph: %v", err)
	}

	return g
}

func TestExporter_Export(t *testing.T) {
	ctx := context.Background()
	kv := kvmemory.New()
	g := setupTestGraph(t, kv, ctx, "test-graph-1")

	exporter := NewExporter(kv)
	snap, err := exporter.Export(ctx, g)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	if snap.Version != 1 {
		t.Errorf("expected version 1, got %d", snap.Version)
	}
	if snap.GraphID != "test-graph-1" {
		t.Errorf("expected graph_id test-graph-1, got %s", snap.GraphID)
	}
	if len(snap.ArcTableEntries) != 2 {
		t.Errorf("expected 2 ArcTable entries, got %d", len(snap.ArcTableEntries))
	}
	if len(snap.LineageEntries) != 2 { // includes the COW entry since it also has lineage/ prefix
		t.Errorf("expected 2 lineage entries, got %d", len(snap.LineageEntries))
	}
	if len(snap.COWEntries) != 1 {
		t.Errorf("expected 1 COW entry, got %d", len(snap.COWEntries))
	}
	if snap.Checksum == "" {
		t.Error("expected non-empty checksum")
	}
}

func TestExporter_ExportAll(t *testing.T) {
	ctx := context.Background()
	kv := kvmemory.New()
	store := graph.NewStore(kv)
	mgr := graph.NewManager(store)

	// Create two graphs
	g1, _ := mgr.CreateGraph(ctx, "graph-a", "kzg", "overwrite")
	g2, _ := mgr.CreateGraph(ctx, "graph-b", "kzg", "overwrite")

	// Add entries for each
	kv.Put(ctx, []byte("graph-a:test"), []byte("val1"))
	kv.Put(ctx, []byte("graph-b:test"), []byte("val2"))

	g1.UpdateRoot(makeCID(1), 1)
	g2.UpdateRoot(makeCID(2), 1)
	store.Update(ctx, g1)
	store.Update(ctx, g2)

	exporter := NewExporter(kv)
	snaps, err := exporter.ExportAll(ctx, store)
	if err != nil {
		t.Fatalf("export all: %v", err)
	}
	if len(snaps) != 2 {
		t.Errorf("expected 2 snapshots, got %d", len(snaps))
	}
}

func TestImporter_Import(t *testing.T) {
	ctx := context.Background()

	// Source
	srcKV := kvmemory.New()
	g := setupTestGraph(t, srcKV, ctx, "import-test")

	// Export from source
	exporter := NewExporter(srcKV)
	snap, err := exporter.Export(ctx, g)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	// Import to target
	tgtKV := kvmemory.New()
	importer := NewImporter(tgtKV)
	count, err := importer.Import(ctx, snap)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if count == 0 {
		t.Error("expected non-zero import count")
	}

	// Verify entries were imported
	has, _ := tgtKV.Has(ctx, []byte("import-test:test/path/1"))
	if !has {
		t.Error("expected ArcTable entry to be imported")
	}

	has, _ = tgtKV.Has(ctx, []byte("lineage/root1"))
	if !has {
		t.Error("expected lineage entry to be imported")
	}

	has, _ = tgtKV.Has(ctx, []byte("lineage/cow/root0/root1"))
	if !has {
		t.Error("expected COW entry to be imported")
	}

	// Verify graph metadata
	has, _ = tgtKV.Has(ctx, []byte("graph/meta/import-test"))
	if !has {
		t.Error("expected graph metadata to be imported")
	}
}

func TestSnapshot_MarshalUnmarshal(t *testing.T) {
	ctx := context.Background()
	kv := kvmemory.New()
	g := setupTestGraph(t, kv, ctx, "marshal-test")

	exporter := NewExporter(kv)
	snap, err := exporter.Export(ctx, g)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	data, err := snap.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	snap2, err := Unmarshal(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if snap2.GraphID != snap.GraphID {
		t.Errorf("graph_id mismatch: %s != %s", snap2.GraphID, snap.GraphID)
	}
	if snap2.Checksum != snap.Checksum {
		t.Errorf("checksum mismatch: %s != %s", snap2.Checksum, snap.Checksum)
	}
	if len(snap2.ArcTableEntries) != len(snap.ArcTableEntries) {
		t.Errorf("ArcTable entry count mismatch: %d != %d", len(snap2.ArcTableEntries), len(snap.ArcTableEntries))
	}
}

func TestSnapshot_VerifyChecksum(t *testing.T) {
	ctx := context.Background()
	kv := kvmemory.New()
	g := setupTestGraph(t, kv, ctx, "checksum-test")

	exporter := NewExporter(kv)
	snap, err := exporter.Export(ctx, g)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	// Valid checksum
	if err := snap.VerifyChecksum(); err != nil {
		t.Fatalf("verify checksum failed: %v", err)
	}

	// Tampered data
	snap.ArcTableEntries["tampered"] = []byte("bad data")
	if err := snap.VerifyChecksum(); err == nil {
		t.Error("expected checksum mismatch after tampering")
	}
}

func TestSnapshot_ImportBadChecksum(t *testing.T) {
	ctx := context.Background()
	kv := kvmemory.New()
	g := setupTestGraph(t, kv, ctx, "bad-checksum-test")

	exporter := NewExporter(kv)
	snap, _ := exporter.Export(ctx, g)

	// Tamper with checksum
	snap.Checksum = "deadbeef"

	tgtKV := kvmemory.New()
	importer := NewImporter(tgtKV)
	_, err := importer.Import(ctx, snap)
	if err == nil {
		t.Error("expected error for invalid checksum")
	}
}

func TestSyncer_Diff(t *testing.T) {
	ctx := context.Background()

	// Source with data
	srcKV := kvmemory.New()
	setupTestGraph(t, srcKV, ctx, "sync-test")

	// Empty target
	tgtKV := kvmemory.New()

	syncer := NewSyncer(srcKV, tgtKV)
	diff, err := syncer.Diff(ctx)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}

	if len(diff.MissingInTarget) == 0 {
		t.Error("expected missing entries in empty target")
	}
	if len(diff.Mismatched) != 0 {
		t.Errorf("expected no mismatches in empty target, got %d", len(diff.Mismatched))
	}
}

func TestSyncer_Sync(t *testing.T) {
	ctx := context.Background()

	// Source with data
	srcKV := kvmemory.New()
	setupTestGraph(t, srcKV, ctx, "sync-test-2")

	// Empty target
	tgtKV := kvmemory.New()

	syncer := NewSyncer(srcKV, tgtKV)
	result, err := syncer.Sync(ctx)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	if result.Imported == 0 {
		t.Error("expected non-zero imported count")
	}

	// Verify target now has the data
	diff, err := syncer.Diff(ctx)
	if err != nil {
		t.Fatalf("diff after sync: %v", err)
	}

	if len(diff.MissingInTarget) != 0 {
		t.Errorf("expected no missing entries after sync, got %d", len(diff.MissingInTarget))
	}
	if len(diff.Mismatched) != 0 {
		t.Errorf("expected no mismatches after sync, got %d", len(diff.Mismatched))
	}
}

func TestSyncer_DiffWithExtraInTarget(t *testing.T) {
	ctx := context.Background()

	srcKV := kvmemory.New()
	tgtKV := kvmemory.New()

	// Only source has this key
	srcKV.Put(ctx, []byte("lineage/src-only"), []byte("src"))
	// Only target has this key
	tgtKV.Put(ctx, []byte("lineage/tgt-only"), []byte("tgt"))

	syncer := NewSyncer(srcKV, tgtKV)
	diff, err := syncer.Diff(ctx)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}

	if len(diff.MissingInTarget) == 0 {
		t.Error("expected 'lineage/src-only' to be missing in target")
	}
	if len(diff.ExtraInTarget) == 0 {
		t.Error("expected 'lineage/tgt-only' to be extra in target")
	}
}

func TestSyncGraphs(t *testing.T) {
	ctx := context.Background()

	srcKV := kvmemory.New()
	g := setupTestGraph(t, srcKV, ctx, "sync-graphs-test")

	tgtKV := kvmemory.New()

	count, err := SyncGraphs(srcKV, tgtKV, g, ctx)
	if err != nil {
		t.Fatalf("sync graphs: %v", err)
	}
	if count == 0 {
		t.Error("expected non-zero sync count")
	}

	// Verify target has the data
	has, _ := tgtKV.Has(ctx, []byte("sync-graphs-test:test/path/1"))
	if !has {
		t.Error("expected ArcTable entry after sync")
	}
}

func TestExporter_EmptyGraph(t *testing.T) {
	ctx := context.Background()
	kv := kvmemory.New()
	store := graph.NewStore(kv)
	mgr := graph.NewManager(store)
	g, _ := mgr.CreateGraph(ctx, "empty-graph", "kzg", "overwrite")

	exporter := NewExporter(kv)
	snap, err := exporter.Export(ctx, g)
	if err != nil {
		t.Fatalf("export empty graph: %v", err)
	}

	if snap.GraphID != "empty-graph" {
		t.Errorf("expected graph_id empty-graph, got %s", snap.GraphID)
	}
	if len(snap.ArcTableEntries) != 0 {
		t.Errorf("expected 0 ArcTable entries, got %d", len(snap.ArcTableEntries))
	}
}

func TestSyncer_SyncIdempotent(t *testing.T) {
	ctx := context.Background()

	srcKV := kvmemory.New()
	setupTestGraph(t, srcKV, ctx, "idempotent-test")

	tgtKV := kvmemory.New()

	syncer := NewSyncer(srcKV, tgtKV)

	// First sync
	result1, err := syncer.Sync(ctx)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// Second sync should import nothing
	result2, err := syncer.Sync(ctx)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}

	if result2.Imported != 0 {
		t.Errorf("expected 0 imports on second sync, got %d", result2.Imported)
	}
	if result1.Imported == 0 {
		t.Error("expected non-zero imports on first sync")
	}
}

func TestSnapshot_FileRoundTrip(t *testing.T) {
	ctx := context.Background()
	kv := kvmemory.New()
	g := setupTestGraph(t, kv, ctx, "file-test")

	exporter := NewExporter(kv)
	snap, err := exporter.Export(ctx, g)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	// Write to temp file
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "snapshot.json")
	data, _ := snap.Marshal()
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// Read back
	readData, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	snap2, err := Unmarshal(readData)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if snap2.Checksum != snap.Checksum {
		t.Errorf("checksum mismatch after file round trip")
	}
}

func TestBadgerPersistence(t *testing.T) {
	ctx := context.Background()

	// Create a temp directory for BadgerDB
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "badger-test")

	srcKV := kvmemory.New()
	g := setupTestGraph(t, srcKV, ctx, "badger-test")

	// Export from memory source
	exporter := NewExporter(srcKV)
	snap, err := exporter.Export(ctx, g)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	// Import to persistent BadgerDB
	tgtKV, err := badger.New(badger.WithPath(dbPath))
	if err != nil {
		t.Fatalf("create badger: %v", err)
	}
	importer := NewImporter(tgtKV)
	count, err := importer.Import(ctx, snap)
	if err != nil {
		t.Fatalf("import to badger: %v", err)
	}
	if count == 0 {
		t.Error("expected non-zero import count")
	}
	tgtKV.Close()

	// Re-open and verify persistence
	reopenedKV, err := badger.New(badger.WithPath(dbPath))
	if err != nil {
		t.Fatalf("reopen badger: %v", err)
	}
	defer reopenedKV.Close()

	has, _ := reopenedKV.Has(ctx, []byte("badger-test:test/path/1"))
	if !has {
		t.Error("expected ArcTable entry to persist in BadgerDB")
	}
}
