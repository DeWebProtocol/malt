package ipa_test

import (
	"crypto/rand"
	"fmt"
	"testing"

	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/sce/commitment/ipa"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// newPayloadCIDBench creates a CID from data for testing.
func newPayloadCIDBench(data []byte) (cid.Cid, error) {
	mhash, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		return cid.Cid{}, err
	}
	return cid.NewCidV1(cid.Raw, mhash), nil
}

func BenchmarkIPACommit(b *testing.B) {
	c, _ := ipa.NewScheme()

	benchmarks := []int{10, 50, 100, 200}
	for _, n := range benchmarks {
		b.Run(fmt.Sprintf("arcs_%d", n), func(b *testing.B) {
			arcs := generateRandomIPAArcSet(n)
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

func BenchmarkIPAProve(b *testing.B) {
	c, _ := ipa.NewScheme()
	arcs := generateRandomIPAArcSet(100)
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

func BenchmarkIPAVerify(b *testing.B) {
	c, _ := ipa.NewScheme()
	arcs := generateRandomIPAArcSet(100)
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

func BenchmarkIPAUpdate(b *testing.B) {
	c, _ := ipa.NewScheme()
	arcs := generateRandomIPAArcSet(100)
	root, err := c.Commit(arcs)
	if err != nil {
		b.Fatalf("Commit failed: %v", err)
	}
	oldKey, _ := arcs.Get("arc_0")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		newKey := generateRandomIPAKey()
		_, err := c.Update(root, arcs, "arc_0", oldKey, newKey)
		if err != nil {
			b.Fatalf("Update failed: %v", err)
		}
		oldKey = newKey
	}
}

func generateRandomIPAArcSet(n int) *arcset.Map {
	arcs := arcset.NewMap()
	for i := 0; i < n; i++ {
		arcs.Add(fmt.Sprintf("arc_%d", i), generateRandomIPAKey())
	}
	return arcs
}

func generateRandomIPAKey() cid.Cid {
	data := make([]byte, 32)
	rand.Read(data)
	k, _ := newPayloadCIDBench(data)
	return k
}