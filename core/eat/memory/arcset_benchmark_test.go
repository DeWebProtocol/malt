package memory

import (
	"fmt"
	"sync"
	"testing"

	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// newBenchCID creates a CID from data for benchmarking.
func newBenchCID(data []byte) cid.Cid {
	mhash, _ := mh.Sum(data, mh.SHA2_256, -1)
	return cid.NewCidV1(cid.Raw, mhash)
}

// === InMemoryArcSet Benchmarks ===

func BenchmarkInMemoryArcSetSet(b *testing.B) {
	arcs := NewInMemoryArcSet()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		path := fmt.Sprintf("arc_%d", i)
		target := newBenchCID([]byte(path))
		arcs.Set(path, target)
	}
}

func BenchmarkInMemoryArcSetGet(b *testing.B) {
	arcs := NewInMemoryArcSet()

	// Pre-populate
	for i := 0; i < 10000; i++ {
		path := fmt.Sprintf("arc_%d", i)
		target := newBenchCID([]byte(path))
		arcs.Set(path, target)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		path := fmt.Sprintf("arc_%d", i%10000)
		arcs.Get(path)
	}
}

func BenchmarkInMemoryArcSetGetParallel(b *testing.B) {
	arcs := NewInMemoryArcSet()

	// Pre-populate
	for i := 0; i < 10000; i++ {
		path := fmt.Sprintf("arc_%d", i)
		target := newBenchCID([]byte(path))
		arcs.Set(path, target)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			path := fmt.Sprintf("arc_%d", i%10000)
			arcs.Get(path)
			i++
		}
	})
}

func BenchmarkInMemoryArcSetSetParallel(b *testing.B) {
	arcs := NewInMemoryArcSet()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			path := fmt.Sprintf("arc_%d", i)
			target := newBenchCID([]byte(path))
			arcs.Set(path, target)
			i++
		}
	})
}

func BenchmarkInMemoryArcSetIterate(b *testing.B) {
	arcs := NewInMemoryArcSet()

	// Pre-populate
	for i := 0; i < 1000; i++ {
		path := fmt.Sprintf("arc_%d", i)
		target := newBenchCID([]byte(path))
		arcs.Set(path, target)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		it := arcs.Iterate()
		for {
			_, _, ok := it.Next()
			if !ok {
				break
			}
		}
	}
}

func BenchmarkInMemoryArcSetMixedReadWrite(b *testing.B) {
	arcs := NewInMemoryArcSet()

	// Pre-populate
	for i := 0; i < 5000; i++ {
		path := fmt.Sprintf("arc_%d", i)
		target := newBenchCID([]byte(path))
		arcs.Set(path, target)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// 80% reads, 20% writes
		if i%5 == 0 {
			path := fmt.Sprintf("arc_%d", i%5000)
			target := newBenchCID([]byte(fmt.Sprintf("updated_%d", i)))
			arcs.Set(path, target)
		} else {
			path := fmt.Sprintf("arc_%d", i%5000)
			arcs.Get(path)
		}
	}
}

func BenchmarkInMemoryArcSetMixedParallel(b *testing.B) {
	arcs := NewInMemoryArcSet()

	// Pre-populate
	for i := 0; i < 5000; i++ {
		path := fmt.Sprintf("arc_%d", i)
		target := newBenchCID([]byte(path))
		arcs.Set(path, target)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if i%5 == 0 {
				// Write
				path := fmt.Sprintf("arc_%d", i%5000)
				target := newBenchCID([]byte(fmt.Sprintf("updated_%d", i)))
				arcs.Set(path, target)
			} else {
				// Read
				path := fmt.Sprintf("arc_%d", i%5000)
				arcs.Get(path)
			}
			i++
		}
	})
}

// Concurrent access benchmark with multiple goroutines
func BenchmarkInMemoryArcSetConcurrentReadWrite(b *testing.B) {
	for numWriters := 1; numWriters <= 8; numWriters *= 2 {
		b.Run(fmt.Sprintf("writers-%d", numWriters), func(b *testing.B) {
			arcs := NewInMemoryArcSet()

			// Pre-populate
			for i := 0; i < 10000; i++ {
				path := fmt.Sprintf("arc_%d", i)
				target := newBenchCID([]byte(path))
				arcs.Set(path, target)
			}

			var wg sync.WaitGroup
			opsPerWriter := b.N / numWriters

			b.ResetTimer()
			for w := 0; w < numWriters; w++ {
				wg.Add(1)
				go func(writerID int) {
					defer wg.Done()
					for i := 0; i < opsPerWriter; i++ {
						if i%10 == 0 {
							// Write
							path := fmt.Sprintf("arc_%d", (writerID*opsPerWriter+i)%10000)
							target := newBenchCID([]byte(fmt.Sprintf("w_%d", i)))
							arcs.Set(path, target)
						} else {
							// Read
							path := fmt.Sprintf("arc_%d", i%10000)
							arcs.Get(path)
						}
					}
				}(w)
			}
			wg.Wait()
		})
	}
}