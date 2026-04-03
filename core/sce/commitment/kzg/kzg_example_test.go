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

	arcs := arcset.NewMap()
	target1, _ := newPayloadCID([]byte("document.pdf"))
	target2, _ := newPayloadCID([]byte("image.png"))
	arcs.Set("document", target1)
	arcs.Set("image", target2)

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

	arcs := arcset.NewMap()
	target, _ := newPayloadCID([]byte("my-data"))
	arcs.Set("data", target)

	root, _ := c.Commit(arcs)

	provedTarget, proof, err := c.Prove(root, arcs, "data")
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	fmt.Printf("Proved target: %v\n", provedTarget.Equals(target))
	fmt.Printf("Proof length: %d bytes\n", len(proof))

	valid, _ := c.Verify(root, "data", target, proof)
	fmt.Printf("Proof valid: %v\n", valid)

	// Output:
	// Proved target: true
	// Proof length: 84 bytes
	// Proof valid: true
}

// ExampleScheme_Update demonstrates updating an arc with a new target.
func ExampleScheme_Update() {
	c, _ := kzg.NewScheme()

	arcs := arcset.NewMap()
	oldTarget, _ := newPayloadCID([]byte("version-1"))
	arcs.Set("file", oldTarget)

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

	arcs := arcset.NewMap()
	target1, _ := newPayloadCID([]byte("data-1"))
	target2, _ := newPayloadCID([]byte("data-2"))
	arcs.Set("a", target1)
	arcs.Set("b", target2)

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

// ExampleScheme_ProveBatch demonstrates batch proof generation.
func ExampleScheme_ProveBatch() {
	c, _ := kzg.NewScheme()

	arcs := arcset.NewMap()
	t1, _ := newPayloadCID([]byte("data1"))
	t2, _ := newPayloadCID([]byte("data2"))
	arcs.Set("path1", t1)
	arcs.Set("path2", t2)

	root, _ := c.Commit(arcs)

	proofs, _ := c.ProveBatch(root, arcs, []string{"path1", "path2"})
	fmt.Printf("Generated %d proofs\n", len(proofs))

	valid, _ := c.VerifyBatch(root, proofs)
	fmt.Printf("Batch valid: %v\n", valid)

	// Output:
	// Generated 2 proofs
	// Batch valid: true
}

// ExampleScheme_ProveAggregate demonstrates aggregated proof generation.
func ExampleScheme_ProveAggregate() {
	c, _ := kzg.NewScheme()

	arcs := arcset.NewMap()
	t1, _ := newPayloadCID([]byte("data1"))
	t2, _ := newPayloadCID([]byte("data2"))
	arcs.Set("path1", t1)
	arcs.Set("path2", t2)

	root, _ := c.Commit(arcs)

	aggProof, _ := c.ProveAggregate(root, arcs, []string{"path1", "path2"})
	fmt.Printf("Aggregated proof for %d paths\n", len(aggProof.Paths))
	fmt.Printf("Proof data size: %d bytes\n", len(aggProof.ProofData))

	valid, _ := c.VerifyAggregate(root, aggProof)
	fmt.Printf("Aggregate valid: %v\n", valid)

	// Output:
	// Aggregated proof for 2 paths
	// Proof data size: 160 bytes
	// Aggregate valid: true
}