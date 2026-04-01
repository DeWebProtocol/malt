package kzg_test

import (
	"fmt"

	"github.com/dewebprotocol/malt/internal/sce"
	"github.com/dewebprotocol/malt/internal/sce/kzg"
	"github.com/dewebprotocol/malt/key"
)

// ExampleNewCommitment demonstrates basic usage of KZG commitment.
func ExampleNewCommitment() {
	c, err := kzg.NewCommitment()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	arcs := sce.NewMapArcSetView()
	target1, _ := key.NewPayloadCID([]byte("document.pdf"))
	target2, _ := key.NewPayloadCID([]byte("image.png"))
	arcs.Add("document", target1)
	arcs.Add("image", target2)

	root, err := c.Commit(arcs)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	fmt.Printf("Committed: %v\n", root != nil)

	// Output:
	// Committed: true
}

// ExampleCommitment_Prove demonstrates proof generation and verification.
func ExampleCommitment_Prove() {
	c, _ := kzg.NewCommitment()

	arcs := sce.NewMapArcSetView()
	target, _ := key.NewPayloadCID([]byte("my-data"))
	arcs.Add("data", target)

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

// ExampleCommitment_Update demonstrates updating an arc with a new target.
func ExampleCommitment_Update() {
	c, _ := kzg.NewCommitment()

	arcs := sce.NewMapArcSetView()
	oldTarget, _ := key.NewPayloadCID([]byte("version-1"))
	arcs.Add("file", oldTarget)

	root, _ := c.Commit(arcs)
	fmt.Printf("Initial root created: %v\n", root != nil)

	newTarget, _ := key.NewPayloadCID([]byte("version-2"))
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

// ExampleCommitment_BatchUpdate demonstrates updating multiple arcs at once.
func ExampleCommitment_BatchUpdate() {
	c, _ := kzg.NewCommitment()

	arcs := sce.NewMapArcSetView()
	target1, _ := key.NewPayloadCID([]byte("data-1"))
	target2, _ := key.NewPayloadCID([]byte("data-2"))
	arcs.Add("a", target1)
	arcs.Add("b", target2)

	root, _ := c.Commit(arcs)

	newTarget1, _ := key.NewPayloadCID([]byte("updated-1"))
	newTarget2, _ := key.NewPayloadCID([]byte("updated-2"))

	updates := map[string]struct {
		Old key.Key
		New key.Key
	}{
		"a": {Old: target1, New: newTarget1},
		"b": {Old: target2, New: newTarget2},
	}

	newRoot, _ := c.BatchUpdate(root, arcs, updates)
	fmt.Printf("Batch updated: %v\n", !newRoot.Equals(root))

	// Output:
	// Batch updated: true
}

// ExampleNewCommitment_options demonstrates using functional options.
func ExampleNewCommitment_options() {
	c, err := kzg.NewCommitment(
		kzg.WithVectorSize(4096),
		kzg.WithCacheSize(2000),
	)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	fmt.Printf("Created KZG commitment with custom options\n")

	arcs := sce.NewMapArcSetView()
	target, _ := key.NewPayloadCID([]byte("test"))
	arcs.Add("test", target)

	root, _ := c.Commit(arcs)
	fmt.Printf("Committed: %v\n", root != nil)

	// Output:
	// Created KZG commitment with custom options
	// Committed: true
}

// ExampleCommitment_ProveBatch demonstrates batch proof generation.
func ExampleCommitment_ProveBatch() {
	c, _ := kzg.NewCommitment()

	arcs := sce.NewMapArcSetView()
	t1, _ := key.NewPayloadCID([]byte("data1"))
	t2, _ := key.NewPayloadCID([]byte("data2"))
	arcs.Add("path1", t1)
	arcs.Add("path2", t2)

	root, _ := c.Commit(arcs)

	proofs, _ := c.ProveBatch(root, arcs, []string{"path1", "path2"})
	fmt.Printf("Generated %d proofs\n", len(proofs))

	valid, _ := c.VerifyBatch(root, proofs)
	fmt.Printf("Batch valid: %v\n", valid)

	// Output:
	// Generated 2 proofs
	// Batch valid: true
}

// ExampleCommitment_ProveAggregate demonstrates aggregated proof generation.
func ExampleCommitment_ProveAggregate() {
	c, _ := kzg.NewCommitment()

	arcs := sce.NewMapArcSetView()
	t1, _ := key.NewPayloadCID([]byte("data1"))
	t2, _ := key.NewPayloadCID([]byte("data2"))
	arcs.Add("path1", t1)
	arcs.Add("path2", t2)

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