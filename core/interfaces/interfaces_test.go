// Package interfaces_test tests the new interface implementations.
package interfaces_test

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt/core/interfaces"
	"github.com/dewebprotocol/malt/core/kvstore/memory"
	"github.com/dewebprotocol/malt/core/store/arc"
	"github.com/dewebprotocol/malt/core/store/content"
	cid "github.com/ipfs/go-cid"
)

// TestContentStoreInterface tests ContentStore implementations.
func TestContentStoreInterface(t *testing.T) {
	ctx := context.Background()
	kv := memory.New()

	// Test KVStoreContentStore
	store := content.NewKVStoreContentStore(kv)

	data := []byte("test data")
	c, err := store.Put(ctx, data)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	if !c.Defined() {
		t.Fatal("CID should be defined")
	}

	// Get data back
	retrieved, err := store.Get(ctx, c)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if string(retrieved) != string(data) {
		t.Errorf("Data mismatch: expected %s, got %s", data, retrieved)
	}

	// Has check
	has, err := store.Has(ctx, c)
	if err != nil {
		t.Fatalf("Has failed: %v", err)
	}
	if !has {
		t.Error("Has should return true for stored CID")
	}

	// BatchPut
	datas := [][]byte{[]byte("data1"), []byte("data2"), []byte("data3")}
	cids, err := store.BatchPut(ctx, datas)
	if err != nil {
		t.Fatalf("BatchPut failed: %v", err)
	}
	if len(cids) != len(datas) {
		t.Errorf("BatchPut returned wrong count: expected %d, got %d", len(datas), len(cids))
	}

	// BatchGet
	results, err := store.BatchGet(ctx, cids)
	if err != nil {
		t.Fatalf("BatchGet failed: %v", err)
	}
	for i, c := range cids {
		if results[c.String()] == nil {
			t.Errorf("BatchGet missing data for CID %d", i)
		}
	}

	// Close
	if err := store.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

// TestArcStoreInterface tests ArcStore implementations.
func TestArcStoreInterface(t *testing.T) {
	ctx := context.Background()
	kv := memory.New()

	// Test EATArcStore
	store := arc.NewEATArcStore(kv)

	// Create a root CID for testing
	root, err := createTestRoot()
	if err != nil {
		t.Fatalf("createTestRoot failed: %v", err)
	}

	// Put arc
	target1, err := cid.Parse("bafkreigysp6h6h4cwr7fex5mda5v5gvrlw32qnh3tiyvwrepmvscu6vfli")
	if err != nil {
		t.Fatalf("Parse target1 failed: %v", err)
	}

	if err := store.Put(ctx, root, "path1", target1); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Get arc
	retrieved, err := store.Get(ctx, root, "path1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !retrieved.Equals(target1) {
		t.Errorf("Get mismatch: expected %s, got %s", target1, retrieved)
	}

	// BatchPut
	target2, _ := cid.Parse("bafkreigysp6h6h4cwr7fex5mda5v5gvrlw32qnh3tiyvwrepmvscu6vfli")
	target3, _ := cid.Parse("bafkreigysp6h6h4cwr7fex5mda5v5gvrlw32qnh3tiyvwrepmvscu6vfli")
	arcs := map[string]cid.Cid{
		"path2": target2,
		"path3": target3,
	}

	if err := store.BatchPut(ctx, root, arcs); err != nil {
		t.Fatalf("BatchPut failed: %v", err)
	}

	// BatchGet
	paths := []string{"path1", "path2", "path3"}
	results, err := store.BatchGet(ctx, root, paths)
	if err != nil {
		t.Fatalf("BatchGet failed: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("BatchGet returned wrong count: expected 3, got %d", len(results))
	}

	// Size
	size, err := store.Size(ctx, root)
	if err != nil {
		t.Fatalf("Size failed: %v", err)
	}
	if size != 3 {
		t.Errorf("Size mismatch: expected 3, got %d", size)
	}

	// Snapshot
	snapshot, err := store.Snapshot(ctx, root)
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}
	if snapshot.Len() != 3 {
		t.Errorf("Snapshot length mismatch: expected 3, got %d", snapshot.Len())
	}

	// Delete
	if err := store.Delete(ctx, root, "path1"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Verify deletion
	_, err = store.Get(ctx, root, "path1")
	if err != interfaces.ErrNotFound && err.Error() != "arc not found" {
		t.Errorf("Get should fail for deleted path")
	}

	// Close
	if err := store.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

// createTestRoot creates a test root CID.
func createTestRoot() (cid.Cid, error) {
	// Use a random CID as root for testing
	return cid.Parse("bafkreigysp6h6h4cwr7fex5mda5v5gvrlw32qnh3tiyvwrepmvscu6vfli")
}