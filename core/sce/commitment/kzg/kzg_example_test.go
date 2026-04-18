package kzg_test

import (
	"fmt"

	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/sce/commitment/kzg"
	cid "github.com/ipfs/go-cid"
)

// ExampleNewScheme demonstrates basic usage of KZG commitment.
func ExampleNewScheme() {
	c, err := kzg.NewScheme()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	target1, _ := newPayloadCID([]byte("document.pdf"))
	target2, _ := newPayloadCID([]byte("image.png"))
	arcs := arcset.NewSetFrom(map[string]cid.Cid{"document": target1, "image": target2})

	root, err := c.Commit(arcs)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	fmt.Printf("Committed: %v\n", root.Defined())

	// Output:
	// Committed: true
}

// ExampleScheme_Prove demonstrates proof generation and verification.
func ExampleScheme_Prove() {
	c, _ := kzg.NewScheme()

	target, _ := newPayloadCID([]byte("my-data"))
	arcs := arcset.NewSetFrom(map[string]cid.Cid{"data": target})

	root, _ := c.Commit(arcs)

	provedTarget, proof, err := c.Prove(root, arcs, "data")
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	fmt.Printf("Proved target: %v\n", provedTarget.Equals(target))
	fmt.Printf("Wrapped legacy proof: %v\n", len(proof) > kzg.ProofSize)

	valid, _ := c.Verify(root, "data", target, proof)
	fmt.Printf("Proof valid: %v\n", valid)

	// Output:
	// Proved target: true
	// Wrapped legacy proof: true
	// Proof valid: true
}

// ExampleScheme_Update demonstrates updating an arc with a new target.
func ExampleScheme_Update() {
	c, _ := kzg.NewScheme()

	oldTarget, _ := newPayloadCID([]byte("version-1"))
	arcs := arcset.NewSetFrom(map[string]cid.Cid{"file": oldTarget})

	root, _ := c.Commit(arcs)
	fmt.Printf("Initial root created: %v\n", root.Defined())

	newTarget, _ := newPayloadCID([]byte("version-2"))
	newRoot, err := c.Update(root, arcs, "file", oldTarget, newTarget)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	fmt.Printf("Roots differ: %v\n", !newRoot.Equals(root))

	// Output:
	// Initial root created: true
	// Roots differ: true
}

// ExampleScheme_BatchUpdate demonstrates updating multiple arcs at once.
func ExampleScheme_BatchUpdate() {
	c, _ := kzg.NewScheme()

	target1, _ := newPayloadCID([]byte("data-1"))
	target2, _ := newPayloadCID([]byte("data-2"))
	arcs := arcset.NewSetFrom(map[string]cid.Cid{"a": target1, "b": target2})

	root, _ := c.Commit(arcs)

	newTarget1, _ := newPayloadCID([]byte("updated-1"))
	newTarget2, _ := newPayloadCID([]byte("updated-2"))

	updates := map[string]struct {
		Old cid.Cid
		New cid.Cid
	}{
		"a": {Old: target1, New: newTarget1},
		"b": {Old: target2, New: newTarget2},
	}

	newRoot, _ := c.BatchUpdate(root, arcs, updates)
	fmt.Printf("Batch updated: %v\n", !newRoot.Equals(root))

	// Output:
	// Batch updated: true
}

// ExampleScheme_BatchProve demonstrates Batch proof generation.
func ExampleScheme_BatchProve() {
	c, _ := kzg.NewScheme()

	t1, _ := newPayloadCID([]byte("data1"))
	t2, _ := newPayloadCID([]byte("data2"))
	arcs := arcset.NewSetFrom(map[string]cid.Cid{"path1": t1, "path2": t2})

	root, _ := c.Commit(arcs)

	proofs, _ := c.BatchProve(root, arcs, []string{"path1", "path2"})
	fmt.Printf("Generated %d proofs\n", len(proofs))

	valid, _ := c.BatchVerify(root, proofs)
	fmt.Printf("Batch valid: %v\n", valid)

	// Output:
	// Generated 2 proofs
	// Batch valid: true
}

// ExampleScheme_AggregateProve demonstrates aggregated proof generation.
func ExampleScheme_AggregateProve() {
	c, _ := kzg.NewScheme()

	t1, _ := newPayloadCID([]byte("data1"))
	t2, _ := newPayloadCID([]byte("data2"))
	arcs := arcset.NewSetFrom(map[string]cid.Cid{"path1": t1, "path2": t2})

	root, _ := c.Commit(arcs)

	aggProof, _ := c.AggregateProve(root, arcs, []string{"path1", "path2"})
	fmt.Printf("Aggregated proof for %d paths\n", len(aggProof.Paths))
	fmt.Printf("Stored proofs: %d\n", len(aggProof.Proofs))

	valid, _ := c.AggregateVerify(root, aggProof)
	fmt.Printf("Aggregate valid: %v\n", valid)

	// Output:
	// Aggregated proof for 2 paths
	// Stored proofs: 2
	// Aggregate valid: true
}
