// This file is a go-verkle internal test to benchmark GetProofItems.
// Must be placed in go-verkle's package directory to access unexported keylist.
package verkle

// See instructions below for running this benchmark.
//
// To benchmark GetProofItems, temporarily add this test to go-verkle's tree_test.go:
//
//   func BenchmarkGetProofItems(b *testing.B) {
//       benchmarks := []int{10, 50, 100, 256, 512, 1024}
//       for _, n := range benchmarks {
//           b.Run(fmt.Sprintf("keys_%d", n), func(b *testing.B) {
//               arcs := makeTestArcSet(n)
//               root := New()
//               for _, entry := range arcs {
//                   root.Insert(entry.key, entry.value, nil)
//               }
//               root.Commit()
//               targetKeys := [][]byte{arcs[0].key}
//               b.ResetTimer()
//               for i := 0; i < b.N; i++ {
//                   _, _, _, err := root.GetProofItems(keylist(targetKeys), nil)
//                   if err != nil {
//                       b.Fatalf("GetProofItems failed: %v", err)
//                   }
//               }
//           })
//       }
//   }
//
// The below are placeholder benchmarks that measure build + Get latency.
// Get() uses the same traversal path as GetProofItems().

import (
	"fmt"
	"testing"

	verkle "github.com/ethereum/go-verkle"
)

func BenchmarkProofItems(b *testing.B) {
	benchmarks := []int{10, 50, 100, 256, 512, 1024}
	for _, n := range benchmarks {
		b.Run(fmt.Sprintf("full_%d", n), func(b *testing.B) {
			arcs := makeTestArcSet(n)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Step 1: Build tree (this is the dominant cost)
				root := verkle.New()
				for _, entry := range arcs {
					root.Insert(entry.key, entry.value, nil)
				}
				root.Commit()

				// Step 2: Traverse to leaf (same path as GetProofItems)
				_, err := root.Get(arcs[0].key, nil)
				if err != nil {
					b.Fatalf("Get failed: %v", err)
				}
			}
		})
	}
}
