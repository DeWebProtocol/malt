package ipa_test

import (
	"fmt"

	"github.com/dewebprotocol/malt/core/eat/memory"
	"github.com/dewebprotocol/malt/core/sce/commitment/ipa"
)

// ExampleNewScheme demonstrates basic usage of IPA commitment.
func ExampleNewScheme() {
	c, err := ipa.NewScheme()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	arcs := memory.NewInMemoryArcSet()
	target, _ := newPayloadCID([]byte("data"))
	arcs.Set("key", target)

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
	c, _ := ipa.NewScheme()

	arcs := memory.NewInMemoryArcSet()
	target, _ := newPayloadCID([]byte("content"))
	arcs.Set("path", target)

	root, _ := c.Commit(arcs)

	provedTarget, proof, _ := c.Prove(root, arcs, "path")

	fmt.Printf("Target matches: %v\n", provedTarget.Equals(target))
	fmt.Printf("Has proof: %v\n", len(proof) > 0)

	valid, _ := c.Verify(root, "path", target, proof)
	fmt.Printf("Valid: %v\n", valid)

	// Output:
	// Target matches: true
	// Has proof: true
	// Valid: true
}

// ExampleScheme_Update demonstrates the fast update feature of IPA.
func ExampleScheme_Update() {
	c, _ := ipa.NewScheme()

	arcs := memory.NewInMemoryArcSet()
	oldTarget, _ := newPayloadCID([]byte("old"))
	arcs.Set("data", oldTarget)

	root, _ := c.Commit(arcs)

	newTarget, _ := newPayloadCID([]byte("new"))
	newRoot, _ := c.Update(root, arcs, "data", oldTarget, newTarget)

	fmt.Printf("Update succeeded: %v\n", !newRoot.Equals(root))

	// Output:
	// Update succeeded: true
}

// ExampleScheme_ProveBatch demonstrates batch proof generation for IPA.
func ExampleScheme_ProveBatch() {
	c, _ := ipa.NewScheme()

	arcs := memory.NewInMemoryArcSet()
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

// ExampleScheme_ProveAggregate demonstrates aggregated proof for IPA.
func ExampleScheme_ProveAggregate() {
	c, _ := ipa.NewScheme()

	arcs := memory.NewInMemoryArcSet()
	t1, _ := newPayloadCID([]byte("data1"))
	t2, _ := newPayloadCID([]byte("data2"))
	arcs.Set("path1", t1)
	arcs.Set("path2", t2)

	root, _ := c.Commit(arcs)

	aggProof, _ := c.ProveAggregate(root, arcs, []string{"path1", "path2"})
	fmt.Printf("Aggregated proof for %d paths\n", len(aggProof.Paths))

	valid, _ := c.VerifyAggregate(root, aggProof)
	fmt.Printf("Aggregate valid: %v\n", valid)

	// Output:
	// Aggregated proof for 2 paths
	// Aggregate valid: true
}