package verkle_test

import (
	"crypto/rand"
	"fmt"
	"testing"

	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/sce/commitment/verkle"
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
	oldKey, _ := arcs.Get(arcset.CanonicalizePath("arc_0"))

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

func generateRandomVerkleArcSet(n int) *arcset.Set {
	arcsMap := make(map[string]cid.Cid)
	for i := 0; i < n; i++ {
		arcsMap[fmt.Sprintf("arc_%d", i)] = generateRandomVerkleKey()
	}
	return arcset.NewSetFrom(arcsMap)
}

func generateRandomVerkleKey() cid.Cid {
	data := make([]byte, 32)
	rand.Read(data)
	k, _ := newPayloadCIDBench(data)
	return k
}
