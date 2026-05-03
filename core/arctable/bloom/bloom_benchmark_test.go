// Package bloom_test benchmarks the Bloom Filter implementation.
package bloom_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/dewebprotocol/malt/core/arctable/bloom"
	"github.com/dewebprotocol/malt/core/kvstore/memory"
)

// ============================================================================
// StandardBloom Benchmarks
// ============================================================================

// BenchmarkStandardBloomAdd benchmarks single Add operation.
func BenchmarkStandardBloomAdd(b *testing.B) {
	bf := bloom.NewStandardBloom(10000, 0.01)
	item := []byte("test/path/123")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bf.Add(item)
	}
	b.StopTimer()
}

// BenchmarkStandardBloomAddUnique benchmarks Add with unique items.
func BenchmarkStandardBloomAddUnique(b *testing.B) {
	bf := bloom.NewStandardBloom(b.N, 0.01)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		item := []byte(fmt.Sprintf("path/%d", i))
		bf.Add(item)
	}
}

// BenchmarkStandardBloomTest benchmarks single Test operation (positive case).
func BenchmarkStandardBloomTestPositive(b *testing.B) {
	bf := bloom.NewStandardBloom(10000, 0.01)
	item := []byte("test/path/123")
	bf.Add(item)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bf.Test(item)
	}
}

// BenchmarkStandardBloomTestNegative benchmarks single Test operation (negative case).
func BenchmarkStandardBloomTestNegative(b *testing.B) {
	bf := bloom.NewStandardBloom(10000, 0.01)
	item := []byte("test/path/123")
	bf.Add([]byte("other/path"))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bf.Test(item)
	}
}

// BenchmarkStandardBloomTestBatch benchmarks batch Test operations.
func BenchmarkStandardBloomTestBatch(b *testing.B) {
	bf := bloom.NewStandardBloom(10000, 0.01)

	// Pre-populate with 1000 items
	for i := 0; i < 1000; i++ {
		bf.Add([]byte(fmt.Sprintf("path/%d", i)))
	}

	// Test batch
	items := make([][]byte, 100)
	for i := 0; i < 100; i++ {
		items[i] = []byte(fmt.Sprintf("path/%d", i%500))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, item := range items {
			bf.Test(item)
		}
	}
}

// BenchmarkStandardBloomMarshal benchmarks serialization.
func BenchmarkStandardBloomMarshal(b *testing.B) {
	bf := bloom.NewStandardBloom(10000, 0.01)

	// Pre-populate with 5000 items
	for i := 0; i < 5000; i++ {
		bf.Add([]byte(fmt.Sprintf("path/%d", i)))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = bf.MarshalBinary()
	}
}

// BenchmarkStandardBloomUnmarshal benchmarks deserialization.
func BenchmarkStandardBloomUnmarshal(b *testing.B) {
	bf := bloom.NewStandardBloom(10000, 0.01)

	// Pre-populate with 5000 items
	for i := 0; i < 5000; i++ {
		bf.Add([]byte(fmt.Sprintf("path/%d", i)))
	}

	data, err := bf.MarshalBinary()
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bf2 := &bloom.StandardBloom{}
		_ = bf2.UnmarshalBinary(data)
	}
}

// BenchmarkStandardBloomDifferentSizes benchmarks with different sizes.
func BenchmarkStandardBloomDifferentSizes(b *testing.B) {
	for _, size := range []int{100, 1000, 10000, 100000} {
		b.Run(fmt.Sprintf("items_%d", size), func(b *testing.B) {
			bf := bloom.NewStandardBloom(size, 0.01)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				item := []byte(fmt.Sprintf("path/%d", i%size))
				bf.Add(item)
				bf.Test(item)
			}
		})
	}
}

// BenchmarkStandardBloomDifferentFalsePositiveRates benchmarks with different FPR.
func BenchmarkStandardBloomDifferentFalsePositiveRates(b *testing.B) {
	for _, fpr := range []float64{0.001, 0.01, 0.05, 0.1} {
		b.Run(fmt.Sprintf("fpr_%0.3f", fpr), func(b *testing.B) {
			bf := bloom.NewStandardBloom(10000, fpr)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				item := []byte(fmt.Sprintf("path/%d", i))
				bf.Add(item)
				bf.Test(item)
			}
		})
	}
}

// ============================================================================
// BloomCache Benchmarks
// ============================================================================

func setupBloomCache(b *testing.B) (*bloom.BloomCache, context.Context) {
	kv := memory.New()
	ctx := context.Background()
	bc := bloom.NewBloomCache(kv, 100)
	return bc, ctx
}

// BenchmarkBloomCacheGetCached benchmarks Get with cache hit.
func BenchmarkBloomCacheGetCached(b *testing.B) {
	bc, ctx := setupBloomCache(b)

	// Pre-create namespace and add paths
	_ = bc.CreateNamespace(ctx, "test-namespace", nil)
	for i := 0; i < 1000; i++ {
		_ = bc.Add(ctx, "test-namespace", []string{fmt.Sprintf("path/%d", i)})
	}

	// Warm up cache
	_, _ = bc.Get(ctx, "test-namespace")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = bc.Get(ctx, "test-namespace")
	}
}

// BenchmarkBloomCacheGetUncached benchmarks Get without cache hit.
func BenchmarkBloomCacheGetUncached(b *testing.B) {
	bc, ctx := setupBloomCache(b)

	// Pre-create namespaces
	for i := 0; i < b.N; i++ {
		namespace := fmt.Sprintf("namespace-%d", i)
		_ = bc.CreateNamespace(ctx, namespace, nil)
		for j := 0; j < 100; j++ {
			_ = bc.Add(ctx, namespace, []string{fmt.Sprintf("path/%d", j)})
		}
	}

	// Clear cache to force kvstore load
	bc.Clear()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		namespace := fmt.Sprintf("namespace-%d", i)
		_, _ = bc.Get(ctx, namespace)
	}
}

// BenchmarkBloomCacheMightContainPositive benchmarks MightContain for existing path.
func BenchmarkBloomCacheMightContainPositive(b *testing.B) {
	bc, ctx := setupBloomCache(b)

	// Pre-create namespace and add paths
	_ = bc.CreateNamespace(ctx, "test-namespace", nil)
	paths := make([]string, 1000)
	for i := 0; i < 1000; i++ {
		paths[i] = fmt.Sprintf("path/%d", i)
	}
	_ = bc.Add(ctx, "test-namespace", paths)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bc.MightContain(ctx, "test-namespace", paths[i%1000])
	}
}

// BenchmarkBloomCacheMightContainNegative benchmarks MightContain for non-existing path.
func BenchmarkBloomCacheMightContainNegative(b *testing.B) {
	bc, ctx := setupBloomCache(b)

	// Pre-create namespace with some paths
	_ = bc.CreateNamespace(ctx, "test-namespace", nil)
	for i := 0; i < 100; i++ {
		_ = bc.Add(ctx, "test-namespace", []string{fmt.Sprintf("path/%d", i)})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Test path that definitely doesn't exist
		bc.MightContain(ctx, "test-namespace", fmt.Sprintf("nonexistent/%d", i))
	}
}

// BenchmarkBloomCacheMightContainBatch benchmarks batch checking.
func BenchmarkBloomCacheMightContainBatch(b *testing.B) {
	bc, ctx := setupBloomCache(b)

	// Pre-create namespace
	_ = bc.CreateNamespace(ctx, "test-namespace", nil)

	// Pre-populate paths
	for i := 0; i < 1000; i++ {
		_ = bc.Add(ctx, "test-namespace", []string{fmt.Sprintf("path/%d", i)})
	}

	// Prepare batch
	paths := make([]string, 100)
	for i := 0; i < 100; i++ {
		paths[i] = fmt.Sprintf("path/%d", i%500)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bc.MightContainBatch(ctx, "test-namespace", paths)
	}
}

// BenchmarkBloomCacheAdd benchmarks adding paths.
func BenchmarkBloomCacheAdd(b *testing.B) {
	bc, ctx := setupBloomCache(b)

	// Pre-create namespace
	_ = bc.CreateNamespace(ctx, "test-namespace", nil)

	paths := []string{"test/path/1", "test/path/2", "test/path/3"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = bc.Add(ctx, "test-namespace", paths)
	}
}

// BenchmarkBloomCacheAddBatch benchmarks adding many paths in one call.
func BenchmarkBloomCacheAddBatch(b *testing.B) {
	bc, ctx := setupBloomCache(b)

	// Pre-create namespace
	_ = bc.CreateNamespace(ctx, "test-namespace", nil)

	for _, batchSize := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("batch_%d", batchSize), func(b *testing.B) {
			paths := make([]string, batchSize)
			for i := 0; i < batchSize; i++ {
				paths[i] = fmt.Sprintf("path/%d", i)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = bc.Add(ctx, "test-namespace", paths)
			}
		})
	}
}

// BenchmarkBloomCacheCreateNamespace benchmarks namespace creation.
func BenchmarkBloomCacheCreateNamespace(b *testing.B) {
	bc, ctx := setupBloomCache(b)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		namespace := fmt.Sprintf("namespace-%d", i)
		_ = bc.CreateNamespace(ctx, namespace, nil)
	}
}

// ============================================================================
// LRU Cache Benchmarks
// ============================================================================

// BenchmarkCacheGet benchmarks BloomCache Get operation (cache hit path).
func BenchmarkCacheGet(b *testing.B) {
	ctx := context.Background()
	kv := memory.New()
	bc := bloom.NewBloomCache(kv, 100)

	// Pre-populate cache
	for i := 0; i < 100; i++ {
		namespace := fmt.Sprintf("namespace-%d", i)
		_ = bc.CreateNamespace(ctx, namespace, nil)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		namespace := fmt.Sprintf("namespace-%d", i%100)
		_, _ = bc.Get(ctx, namespace)
	}
}

// BenchmarkCacheSet benchmarks BloomCache Add operation (cache set path).
func BenchmarkCacheSet(b *testing.B) {
	ctx := context.Background()
	kv := memory.New()
	bc := bloom.NewBloomCache(kv, 100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		namespace := fmt.Sprintf("namespace-%d", i%100)
		_ = bc.CreateNamespace(ctx, namespace, nil)
		_ = bc.Add(ctx, namespace, []string{fmt.Sprintf("path-%d", i)})
	}
}

// BenchmarkCacheSetEvict benchmarks BloomCache with eviction.
func BenchmarkCacheSetEvict(b *testing.B) {
	ctx := context.Background()
	kv := memory.New()
	bc := bloom.NewBloomCache(kv, 10) // Small cache to trigger eviction

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		namespace := fmt.Sprintf("namespace-%d", i)
		_ = bc.CreateNamespace(ctx, namespace, nil)
		_ = bc.Add(ctx, namespace, []string{fmt.Sprintf("path-%d", i)})
	}
}
