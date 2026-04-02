package memory

import (
	"fmt"
	"testing"

	cid "github.com/ipfs/go-cid"
)

// === BucketedInMemoryEAT Benchmarks ===

func BenchmarkBucketedInMemoryEATPut(b *testing.B) {
	eat := NewBucketedInMemoryEAT()
	root := newBenchCID([]byte("root"))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		path := fmt.Sprintf("arc_%d", i)
		target := newBenchCID([]byte(path))
		eat.Put(root, path, target)
	}
}

func BenchmarkBucketedInMemoryEATGet(b *testing.B) {
	eat := NewBucketedInMemoryEAT()
	root := newBenchCID([]byte("root"))

	// Pre-populate
	for i := 0; i < 10000; i++ {
		path := fmt.Sprintf("arc_%d", i)
		target := newBenchCID([]byte(path))
		eat.Put(root, path, target)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		path := fmt.Sprintf("arc_%d", i%10000)
		eat.Get(root, path)
	}
}

func BenchmarkBucketedInMemoryEATGetParallel(b *testing.B) {
	eat := NewBucketedInMemoryEAT()
	root := newBenchCID([]byte("root"))

	// Pre-populate
	for i := 0; i < 10000; i++ {
		path := fmt.Sprintf("arc_%d", i)
		target := newBenchCID([]byte(path))
		eat.Put(root, path, target)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			path := fmt.Sprintf("arc_%d", i%10000)
			eat.Get(root, path)
			i++
		}
	})
}

func BenchmarkBucketedInMemoryEATPutParallel(b *testing.B) {
	eat := NewBucketedInMemoryEAT()
	root := newBenchCID([]byte("root"))

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			path := fmt.Sprintf("arc_%d", i)
			target := newBenchCID([]byte(path))
			eat.Put(root, path, target)
			i++
		}
	})
}

func BenchmarkBucketedInMemoryEATMultiBucket(b *testing.B) {
	eat := NewBucketedInMemoryEAT()

	// Create multiple buckets
	roots := make([]cid.Cid, 10)
	for i := 0; i < 10; i++ {
		roots[i] = newBenchCID([]byte(fmt.Sprintf("root_%d", i)))
	}

	// Pre-populate all buckets
	for _, root := range roots {
		for i := 0; i < 1000; i++ {
			path := fmt.Sprintf("arc_%d", i)
			target := newBenchCID([]byte(path))
			eat.Put(root, path, target)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rootIdx := i % 10
		pathIdx := i % 1000
		path := fmt.Sprintf("arc_%d", pathIdx)
		eat.Get(roots[rootIdx], path)
	}
}

func BenchmarkBucketedInMemoryEATView(b *testing.B) {
	eat := NewBucketedInMemoryEAT()
	root := newBenchCID([]byte("root"))

	// Pre-populate
	for i := 0; i < 1000; i++ {
		path := fmt.Sprintf("arc_%d", i)
		target := newBenchCID([]byte(path))
		eat.Put(root, path, target)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		view := eat.View(root)
		_ = view
	}
}

func BenchmarkBucketedInMemoryEATViewAndGet(b *testing.B) {
	eat := NewBucketedInMemoryEAT()
	root := newBenchCID([]byte("root"))

	// Pre-populate
	for i := 0; i < 1000; i++ {
		path := fmt.Sprintf("arc_%d", i)
		target := newBenchCID([]byte(path))
		eat.Put(root, path, target)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		view := eat.View(root)
		path := fmt.Sprintf("arc_%d", i%1000)
		view.Get(path)
	}
}