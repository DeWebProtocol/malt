// Package bloom_test tests the Bloom Filter implementation.
package bloom_test

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt/core/arctable/bloom"
	"github.com/dewebprotocol/malt/core/kvstore/memory"
)

func TestStandardBloomBasic(t *testing.T) {
	bf := bloom.NewStandardBloom(100, 0.01)

	// Add some items
	items := []string{"apple", "banana", "cherry", "date", "elderberry"}
	for _, item := range items {
		bf.Add([]byte(item))
	}

	// Test items that were added
	for _, item := range items {
		if !bf.Test([]byte(item)) {
			t.Errorf("expected true for %s", item)
		}
	}

	// Test items that weren't added
	notItems := []string{"fig", "grape", "honeydew"}
	falsePositives := 0
	for _, item := range notItems {
		if bf.Test([]byte(item)) {
			falsePositives++
		}
	}

	// False positive rate should be close to expected
	fpr := float64(falsePositives) / float64(len(notItems))
	if fpr > 0.1 { // allow 10% tolerance
		t.Errorf("false positive rate too high: %f", fpr)
	}

	t.Logf("False positive rate: %f (expected ~0.01)", fpr)
}

func TestStandardBloomClear(t *testing.T) {
	bf := bloom.NewStandardBloom(100, 0.01)

	bf.Add([]byte("test"))
	if !bf.Test([]byte("test")) {
		t.Error("expected true after add")
	}

	bf.Clear()
	if bf.Test([]byte("test")) {
		t.Error("expected false after clear")
	}
}

func TestStandardBloomSize(t *testing.T) {
	bf := bloom.NewStandardBloom(100, 0.01)

	if bf.Size() != 0 {
		t.Error("expected size 0")
	}

	bf.Add([]byte("a"))
	bf.Add([]byte("b"))
	bf.Add([]byte("c"))

	if bf.Size() != 3 {
		t.Errorf("expected size 3, got %d", bf.Size())
	}
}

func TestStandardBloomSerialization(t *testing.T) {
	bf := bloom.NewStandardBloom(100, 0.01)

	// Add some items
	items := []string{"apple", "banana", "cherry"}
	for _, item := range items {
		bf.Add([]byte(item))
	}

	// Serialize
	bitsetBytes := bf.Bitset()
	if bitsetBytes == nil {
		t.Fatal("bitset serialization failed")
	}

	// Deserialize
	bf2, err := bloom.NewStandardBloomFromData(bf.K(), bf.M(), bitsetBytes)
	if err != nil {
		t.Fatalf("deserialization failed: %v", err)
	}

	// Test that deserialized filter works the same
	for _, item := range items {
		if !bf2.Test([]byte(item)) {
			t.Errorf("expected true for %s after deserialization", item)
		}
	}
}

// ============================================================================
// BloomCache Tests
// ============================================================================

func TestBloomCacheNew(t *testing.T) {
	kv := memory.New()

	// Valid creation
	bc := bloom.NewBloomCache(kv, 100)
	if bc == nil {
		t.Error("BloomCache should not be nil")
	}

	// Default size
	bc = bloom.NewBloomCache(kv, 0)
	if bc == nil {
		t.Error("BloomCache with size 0 should use default")
	}
}

func TestBloomCacheCreateNamespace(t *testing.T) {
	kv := memory.New()
	bc := bloom.NewBloomCache(kv, 100)
	ctx := context.Background()

	// Create namespace with default config
	err := bc.CreateNamespace(ctx, "test-namespace", nil)
	if err != nil {
		t.Fatalf("CreateNamespace failed: %v", err)
	}

	// Duplicate namespace should fail
	err = bc.CreateNamespace(ctx, "test-namespace", nil)
	if err == nil {
		t.Error("duplicate namespace should fail")
	}

	// Create namespace with custom config
	cfg := &bloom.NamespaceConfig{
		ExpectedItems:     5000,
		FalsePositiveRate: 0.001,
	}
	err = bc.CreateNamespace(ctx, "custom-namespace", cfg)
	if err != nil {
		t.Fatalf("CreateNamespace with custom config failed: %v", err)
	}
}

func TestBloomCacheGetNamespaceMeta(t *testing.T) {
	kv := memory.New()
	bc := bloom.NewBloomCache(kv, 100)
	ctx := context.Background()

	// Non-existent namespace
	meta, err := bc.GetNamespaceMeta(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetNamespaceMeta for non-existent should not error: %v", err)
	}
	if meta != nil {
		t.Error("non-existent namespace should return nil meta")
	}

	// Create namespace
	cfg := &bloom.NamespaceConfig{
		ExpectedItems:     1000,
		FalsePositiveRate: 0.05,
	}
	bc.CreateNamespace(ctx, "test-namespace", cfg)

	// Get meta
	meta, err = bc.GetNamespaceMeta(ctx, "test-namespace")
	if err != nil {
		t.Fatalf("GetNamespaceMeta failed: %v", err)
	}
	if meta == nil {
		t.Fatal("meta should not be nil")
	}
	if meta.Config.ExpectedItems != 1000 {
		t.Errorf("expected 1000 items, got %d", meta.Config.ExpectedItems)
	}
	if meta.Config.FalsePositiveRate != 0.05 {
		t.Errorf("expected 0.05 fpr, got %f", meta.Config.FalsePositiveRate)
	}
}

func TestBloomCacheGet(t *testing.T) {
	kv := memory.New()
	bc := bloom.NewBloomCache(kv, 100)
	ctx := context.Background()

	// Get non-existent namespace (auto-creates with default config)
	filter, err := bc.Get(ctx, "auto-namespace")
	if err != nil {
		t.Fatalf("Get auto-created namespace failed: %v", err)
	}
	if filter == nil {
		t.Error("filter should not be nil")
	}

	// Verify namespace was created
	meta, _ := bc.GetNamespaceMeta(ctx, "auto-namespace")
	if meta == nil {
		t.Error("auto-namespace should be created")
	}
}

func TestBloomCacheAddAndMightContain(t *testing.T) {
	kv := memory.New()
	bc := bloom.NewBloomCache(kv, 100)
	ctx := context.Background()

	// Create namespace
	bc.CreateNamespace(ctx, "test-namespace", nil)

	// Add paths
	paths := []string{"path/a", "path/b", "path/c"}
	err := bc.Add(ctx, "test-namespace", paths)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// MightContain for existing paths
	for _, path := range paths {
		result, err := bc.MightContain(ctx, "test-namespace", path)
		if err != nil {
			t.Fatalf("MightContain failed: %v", err)
		}
		if !result {
			t.Errorf("expected true for %s", path)
		}
	}

	// MightContain for non-existent path (may be false positive)
	_, err = bc.MightContain(ctx, "test-namespace", "nonexistent/path")
	if err != nil {
		t.Fatalf("MightContain failed: %v", err)
	}
	// Can be true (false positive) or false (correct negative), both valid
}

func TestBloomCacheMightContainBatch(t *testing.T) {
	kv := memory.New()
	bc := bloom.NewBloomCache(kv, 100)
	ctx := context.Background()

	// Create namespace
	bc.CreateNamespace(ctx, "test-namespace", nil)

	// Add paths
	paths := []string{"path/a", "path/b", "path/c"}
	bc.Add(ctx, "test-namespace", paths)

	// Batch check (include both existing and non-existing paths)
	testPaths := []string{"path/a", "path/b", "path/c", "nonexistent"}
	results, err := bc.MightContainBatch(ctx, "test-namespace", testPaths)
	if err != nil {
		t.Fatalf("MightContainBatch failed: %v", err)
	}
	if len(results) != 4 {
		t.Errorf("expected 4 results, got %d", len(results))
	}

	// Added paths should return true (may have false positives but no false negatives)
	for _, p := range paths {
		if !results[p] {
			t.Errorf("expected true for %s (bloom filters have no false negatives)", p)
		}
	}
}

func TestBloomCacheDeleteNamespace(t *testing.T) {
	kv := memory.New()
	bc := bloom.NewBloomCache(kv, 100)
	ctx := context.Background()

	// Create namespace
	bc.CreateNamespace(ctx, "test-namespace", nil)
	bc.Add(ctx, "test-namespace", []string{"path/a"})

	// Delete namespace
	err := bc.DeleteNamespace(ctx, "test-namespace")
	if err != nil {
		t.Fatalf("DeleteNamespace failed: %v", err)
	}

	// Verify deleted
	meta, _ := bc.GetNamespaceMeta(ctx, "test-namespace")
	if meta != nil {
		t.Error("namespace should be deleted")
	}

	// Cache should be cleared
	if bc.Size() != 0 {
		t.Error("cache should be empty after delete")
	}
}

func TestBloomCacheLRUEviction(t *testing.T) {
	kv := memory.New()
	bc := bloom.NewBloomCache(kv, 3) // Small cache
	ctx := context.Background()

	// Create more namespaces than cache size
	for i := 0; i < 5; i++ {
		namespace := string(rune('a' + i))
		bc.CreateNamespace(ctx, namespace, nil)
	}

	// Cache size should be limited
	if bc.Size() > 3 {
		t.Errorf("cache size %d should be <= 3", bc.Size())
	}
}

func TestBloomCacheInvalidate(t *testing.T) {
	kv := memory.New()
	bc := bloom.NewBloomCache(kv, 100)
	ctx := context.Background()

	// Create namespace and add to cache
	bc.CreateNamespace(ctx, "test-namespace", nil)
	bc.Get(ctx, "test-namespace")

	if bc.Size() != 1 {
		t.Error("cache should have 1 entry")
	}

	// Invalidate
	bc.Invalidate("test-namespace")

	if bc.Size() != 0 {
		t.Error("cache should be empty after invalidate")
	}

	// Get should reload from kvstore
	filter, err := bc.Get(ctx, "test-namespace")
	if err != nil {
		t.Fatalf("Get after invalidate failed: %v", err)
	}
	if filter == nil {
		t.Error("filter should be reloaded")
	}
}

func TestBloomCachePersistence(t *testing.T) {
	kv := memory.New()
	ctx := context.Background()

	// Create BloomCache, add data
	bc1 := bloom.NewBloomCache(kv, 100)
	bc1.CreateNamespace(ctx, "test-namespace", &bloom.NamespaceConfig{
		ExpectedItems:     5000,
		FalsePositiveRate: 0.01,
	})
	bc1.Add(ctx, "test-namespace", []string{"path/a", "path/b"})

	// Create new BloomCache with same kvstore (simulates restart)
	bc2 := bloom.NewBloomCache(kv, 100)

	// Data should persist
	meta, err := bc2.GetNamespaceMeta(ctx, "test-namespace")
	if err != nil {
		t.Fatalf("GetNamespaceMeta failed: %v", err)
	}
	if meta == nil {
		t.Fatal("namespace metadata should persist")
	}
	if meta.Config.ExpectedItems != 5000 {
		t.Errorf("expected 5000 items, got %d", meta.Config.ExpectedItems)
	}

	// Bloom filter should persist
	result, _ := bc2.MightContain(ctx, "test-namespace", "path/a")
	if !result {
		t.Error("path/a should exist in persisted bloom filter")
	}
}
