// Package bloom_test tests the Bloom Filter implementation.
package bloom_test

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt/core/eat/bloom"
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

func TestBloomCacheCreateBucket(t *testing.T) {
	kv := memory.New()
	bc := bloom.NewBloomCache(kv, 100)
	ctx := context.Background()

	// Create bucket with default config
	err := bc.CreateBucket(ctx, "test-bucket", nil)
	if err != nil {
		t.Fatalf("CreateBucket failed: %v", err)
	}

	// Duplicate bucket should fail
	err = bc.CreateBucket(ctx, "test-bucket", nil)
	if err == nil {
		t.Error("duplicate bucket should fail")
	}

	// Create bucket with custom config
	cfg := &bloom.BucketConfig{
		ExpectedItems:     5000,
		FalsePositiveRate: 0.001,
	}
	err = bc.CreateBucket(ctx, "custom-bucket", cfg)
	if err != nil {
		t.Fatalf("CreateBucket with custom config failed: %v", err)
	}
}

func TestBloomCacheGetBucketMeta(t *testing.T) {
	kv := memory.New()
	bc := bloom.NewBloomCache(kv, 100)
	ctx := context.Background()

	// Non-existent bucket
	meta, err := bc.GetBucketMeta(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetBucketMeta for non-existent should not error: %v", err)
	}
	if meta != nil {
		t.Error("non-existent bucket should return nil meta")
	}

	// Create bucket
	cfg := &bloom.BucketConfig{
		ExpectedItems:     1000,
		FalsePositiveRate: 0.05,
	}
	bc.CreateBucket(ctx, "test-bucket", cfg)

	// Get meta
	meta, err = bc.GetBucketMeta(ctx, "test-bucket")
	if err != nil {
		t.Fatalf("GetBucketMeta failed: %v", err)
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

	// Get non-existent bucket (auto-creates with default config)
	filter, err := bc.Get(ctx, "auto-bucket")
	if err != nil {
		t.Fatalf("Get auto-created bucket failed: %v", err)
	}
	if filter == nil {
		t.Error("filter should not be nil")
	}

	// Verify bucket was created
	meta, _ := bc.GetBucketMeta(ctx, "auto-bucket")
	if meta == nil {
		t.Error("auto-bucket should be created")
	}
}

func TestBloomCacheAddAndMightContain(t *testing.T) {
	kv := memory.New()
	bc := bloom.NewBloomCache(kv, 100)
	ctx := context.Background()

	// Create bucket
	bc.CreateBucket(ctx, "test-bucket", nil)

	// Add paths
	paths := []string{"path/a", "path/b", "path/c"}
	err := bc.Add(ctx, "test-bucket", paths)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// MightContain for existing paths
	for _, path := range paths {
		result, err := bc.MightContain(ctx, "test-bucket", path)
		if err != nil {
			t.Fatalf("MightContain failed: %v", err)
		}
		if !result {
			t.Errorf("expected true for %s", path)
		}
	}

	// MightContain for non-existent path (may be false positive)
	result, _ := bc.MightContain(ctx, "test-bucket", "nonexistent/path")
	// Can be true (false positive) or false (correct negative), both valid
	_ = result
}

func TestBloomCacheMightContainBatch(t *testing.T) {
	kv := memory.New()
	bc := bloom.NewBloomCache(kv, 100)
	ctx := context.Background()

	// Create bucket
	bc.CreateBucket(ctx, "test-bucket", nil)

	// Add paths
	paths := []string{"path/a", "path/b", "path/c"}
	bc.Add(ctx, "test-bucket", paths)

	// Batch check (include both existing and non-existing paths)
	testPaths := []string{"path/a", "path/b", "path/c", "nonexistent"}
	results, err := bc.MightContainBatch(ctx, "test-bucket", testPaths)
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

func TestBloomCacheDeleteBucket(t *testing.T) {
	kv := memory.New()
	bc := bloom.NewBloomCache(kv, 100)
	ctx := context.Background()

	// Create bucket
	bc.CreateBucket(ctx, "test-bucket", nil)
	bc.Add(ctx, "test-bucket", []string{"path/a"})

	// Delete bucket
	err := bc.DeleteBucket(ctx, "test-bucket")
	if err != nil {
		t.Fatalf("DeleteBucket failed: %v", err)
	}

	// Verify deleted
	meta, _ := bc.GetBucketMeta(ctx, "test-bucket")
	if meta != nil {
		t.Error("bucket should be deleted")
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

	// Create more buckets than cache size
	for i := 0; i < 5; i++ {
		bucketId := string(rune('a' + i))
		bc.CreateBucket(ctx, bucketId, nil)
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

	// Create bucket and add to cache
	bc.CreateBucket(ctx, "test-bucket", nil)
	bc.Get(ctx, "test-bucket")

	if bc.Size() != 1 {
		t.Error("cache should have 1 entry")
	}

	// Invalidate
	bc.Invalidate("test-bucket")

	if bc.Size() != 0 {
		t.Error("cache should be empty after invalidate")
	}

	// Get should reload from kvstore
	filter, err := bc.Get(ctx, "test-bucket")
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
	bc1.CreateBucket(ctx, "test-bucket", &bloom.BucketConfig{
		ExpectedItems:     5000,
		FalsePositiveRate: 0.01,
	})
	bc1.Add(ctx, "test-bucket", []string{"path/a", "path/b"})

	// Create new BloomCache with same kvstore (simulates restart)
	bc2 := bloom.NewBloomCache(kv, 100)

	// Data should persist
	meta, err := bc2.GetBucketMeta(ctx, "test-bucket")
	if err != nil {
		t.Fatalf("GetBucketMeta failed: %v", err)
	}
	if meta == nil {
		t.Fatal("bucket metadata should persist")
	}
	if meta.Config.ExpectedItems != 5000 {
		t.Errorf("expected 5000 items, got %d", meta.Config.ExpectedItems)
	}

	// Bloom filter should persist
	result, _ := bc2.MightContain(ctx, "test-bucket", "path/a")
	if !result {
		t.Error("path/a should exist in persisted bloom filter")
	}
}