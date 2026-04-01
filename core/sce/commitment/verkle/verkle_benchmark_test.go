package verkle_test

import (
	"crypto/rand"
	"fmt"
	"testing"

	"github.com/dewebprotocol/malt/types/arcset"
	"github.com/dewebprotocol/malt/core/sce/commitment/verkle"
	"github.com/dewebprotocol/malt/key"
)

func BenchmarkVerkleCommit(b *testing.B) {
	c, _ := verkle.NewScheme()

	benchmarks := []int{10, 50, 100}
	for _, n := range benchmarks {
		b.Run(fmt.Sprintf("arcs_%d", n), func(b *testing.B) {
			arcs := generateRandomVerkleArcSet(n)
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

func BenchmarkVerkleProve(b *testing.B) {
	c, _ := verkle.NewScheme()
	arcs := generateRandomVerkleArcSet(50)
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

func BenchmarkVerkleVerify(b *testing.B) {
	c, _ := verkle.NewScheme()
	arcs := generateRandomVerkleArcSet(50)
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

func BenchmarkVerkleUpdate(b *testing.B) {
	c, _ := verkle.NewScheme()
	arcs := generateRandomVerkleArcSet(50)
	root, err := c.Commit(arcs)
	if err != nil {
		b.Fatalf("Commit failed: %v", err)
	}
	oldKey, _ := arcs.Get("arc_0")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		newKey := generateRandomVerkleKey()
		_, err := c.Update(root, arcs, "arc_0", oldKey, newKey)
		if err != nil {
			b.Fatalf("Update failed: %v", err)
		}
		oldKey = newKey
	}
}

func generateRandomVerkleArcSet(n int) *arcset.Map {
	arcs := arcset.NewMap()
	for i := 0; i < n; i++ {
		arcs.Add(fmt.Sprintf("arc_%d", i), generateRandomVerkleKey())
	}
	return arcs
}

func generateRandomVerkleKey() key.Key {
	data := make([]byte, 32)
	rand.Read(data)
	k, _ := key.NewPayloadCID(data)
	return k
}