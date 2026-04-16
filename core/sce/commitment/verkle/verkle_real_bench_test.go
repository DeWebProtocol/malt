package verkle

import (
	"crypto/rand"
	"fmt"
	"testing"

	verkle "github.com/ethereum/go-verkle"
)

// BenchmarkRealVerkleBuild measures time to build a full Verkle tree from scratch.
func BenchmarkRealVerkleBuild(b *testing.B) {
	benchmarks := []int{10, 50, 100, 256, 512, 1024}
	for _, n := range benchmarks {
		b.Run(fmt.Sprintf("build_%d", n), func(b *testing.B) {
			arcs := makeTestArcSet(n)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				root := verkle.New()
				for _, entry := range arcs {
					root.Insert(entry.key, entry.value, nil)
				}
				root.Commit()
			}
		})
	}
}

// BenchmarkRealVerkleSerialize measures serialization time and reports storage size.
func BenchmarkRealVerkleSerialize(b *testing.B) {
	benchmarks := []int{10, 50, 100, 256, 512, 1024}
	for _, n := range benchmarks {
		b.Run(fmt.Sprintf("serialize_%d", n), func(b *testing.B) {
			arcs := makeTestArcSet(n)
			root := verkle.New()
			for _, entry := range arcs {
				root.Insert(entry.key, entry.value, nil)
			}
			root.Commit()

			// Measure size once
			sizeBytes := serializeTree(root.(*verkle.InternalNode))
			b.ReportMetric(float64(sizeBytes), "total_bytes")

			// Count nodes
			var nodeCount int
			countNodes(root, &nodeCount)
			b.ReportMetric(float64(nodeCount), "nodes")

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				serializeTree(root.(*verkle.InternalNode))
			}
		})
	}
}

// BenchmarkRealVerkleGet measures tree traversal time (proxy for proof generation).
// Get() traverses from root to leaf, which is the same traversal cost as GetProofItems.
func BenchmarkRealVerkleGet(b *testing.B) {
	benchmarks := []int{10, 50, 100, 256, 512, 1024}
	for _, n := range benchmarks {
		b.Run(fmt.Sprintf("get_%d", n), func(b *testing.B) {
			arcs := makeTestArcSet(n)
			targetKey := arcs[0].key

			// Build tree once
			root := verkle.New()
			for _, entry := range arcs {
				root.Insert(entry.key, entry.value, nil)
			}
			root.Commit()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, err := root.Get(targetKey, nil)
				if err != nil {
					b.Fatalf("Get failed: %v", err)
				}
			}
		})
	}
}

// BenchmarkRealVerkleFullCycle measures: build + serialize + rebuild from data.
// This simulates the "restart and rebuild" scenario.
func BenchmarkRealVerkleFullCycle(b *testing.B) {
	benchmarks := []int{10, 50, 100, 256, 512, 1024}
	for _, n := range benchmarks {
		b.Run(fmt.Sprintf("full_cycle_%d", n), func(b *testing.B) {
			arcs := makeTestArcSet(n)
			targetKey := arcs[0].key

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Phase 1: Build
				root := verkle.New()
				for _, entry := range arcs {
					root.Insert(entry.key, entry.value, nil)
				}
				root.Commit()

				// Phase 2: Traverse (proxy for proof)
				_, err := root.Get(targetKey, nil)
				if err != nil {
					b.Fatalf("Get failed: %v", err)
				}
			}
		})
	}
}

type testArcEntry struct {
	key   []byte
	value []byte
}

func makeTestArcSet(n int) []testArcEntry {
	entries := make([]testArcEntry, n)
	for i := 0; i < n; i++ {
		key := make([]byte, 32)
		rand.Read(key)
		value := make([]byte, 32)
		rand.Read(value)
		entries[i] = testArcEntry{key, value}
	}
	return entries
}

func serializeTree(root *verkle.InternalNode) int {
	total := 0
	collectNodes(root, &total)
	return total
}

func collectNodes(node verkle.VerkleNode, total *int) {
	switch n := node.(type) {
	case *verkle.InternalNode:
		for _, child := range n.Children() {
			collectNodes(child, total)
		}
		data, err := n.Serialize()
		if err == nil {
			*total += len(data)
		}
	case *verkle.LeafNode:
		data, err := n.Serialize()
		if err == nil {
			*total += len(data)
		}
	}
}

func countNodes(node verkle.VerkleNode, count *int) {
	*count++
	if internal, ok := node.(*verkle.InternalNode); ok {
		for _, child := range internal.Children() {
			if _, isLeaf := child.(*verkle.LeafNode); !isLeaf {
				countNodes(child, count)
			} else {
				*count++
			}
		}
	}
}
