package kzg_test

import (
	"fmt"
	"testing"

	"github.com/dewebprotocol/malt/internal/sce"
	"github.com/dewebprotocol/malt/internal/sce/kzg"
	"github.com/dewebprotocol/malt/key"
)

func BenchmarkKZGCommit(b *testing.B) {
	c, _ := kzg.NewCommitment()

	benchmarks := []int{10, 100, 500, 1000}
	for _, n := range benchmarks {
		b.Run(fmt.Sprintf("arcs_%d", n), func(b *testing.B) {
			arcs := generateRandomArcSet(n)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, err := c.Commit(arcs)
				if err != nil {
					b.Fatalf("Commit failed: %v", err)
				}
			}
		})
	}
}

func BenchmarkKZGProve(b *testing.B) {
	c, _ := kzg.NewCommitment()
	arcs := generateRandomArcSet(100)
	root, err := c.Commit(arcs)
	if err != nil {
		b.Fatalf("Commit failed: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, err := c.Prove(root, arcs, "arc_0")
		if err != nil {
			b.Fatalf("Prove failed: %v", err)
		}
	}
}

func BenchmarkKZGVerify(b *testing.B) {
	c, _ := kzg.NewCommitment()
	arcs := generateRandomArcSet(100)
	root, err := c.Commit(arcs)
	if err != nil {
		b.Fatalf("Commit failed: %v", err)
	}
	target, proof, err := c.Prove(root, arcs, "arc_0")
	if err != nil {
		b.Fatalf("Prove failed: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := c.Verify(root, "arc_0", target, proof)
		if err != nil {
			b.Fatalf("Verify failed: %v", err)
		}
	}
}

func BenchmarkKZGUpdate(b *testing.B) {
	c, _ := kzg.NewCommitment()
	arcs := generateRandomArcSet(100)
	root, err := c.Commit(arcs)
	if err != nil {
		b.Fatalf("Commit failed: %v", err)
	}
	oldKey, _ := arcs.Get("arc_0")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		newKey := generateRandomKey()
		_, err := c.Update(root, arcs, "arc_0", oldKey, newKey)
		if err != nil {
			b.Fatalf("Update failed: %v", err)
		}
		oldKey = newKey
	}
}

// generateRandomArcSet creates an arc set with simple deterministic keys.
// Using simple keys avoids the scalar validation issues with random data.
func generateRandomArcSet(n int) *sce.MapArcSetView {
	arcs := sce.NewMapArcSetView()
	for i := 0; i < n; i++ {
		// Use simple deterministic data that won't cause scalar validation issues
		data := []byte{byte(i % 256), byte((i / 256) % 256), byte(i >> 16)}
		k, _ := key.NewPayloadCID(data)
		arcs.Add(fmt.Sprintf("arc_%d", i), k)
	}
	return arcs
}

func generateRandomKey() key.Key {
	// Use simple deterministic data
	data := []byte{0xAB, 0xCD, 0xEF}
	k, _ := key.NewPayloadCID(data)
	return k
}