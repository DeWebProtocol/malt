package ipa_test

import (
	"fmt"

	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/sce/commitment/ipa"
	cid "github.com/ipfs/go-cid"
)

// ExampleNewScheme demonstrates basic usage of IPA commitment.
func ExampleNewScheme() {
	c, err := ipa.NewScheme()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	target, _ := newPayloadCID([]byte("data"))
	arcs := arcset.NewSetFrom(map[string]cid.Cid{"key": target})

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

	target, _ := newPayloadCID([]byte("content"))
	arcs := arcset.NewSetFrom(map[string]cid.Cid{"path": target})

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

	oldTarget, _ := newPayloadCID([]byte("old"))
	arcs := arcset.NewSetFrom(map[string]cid.Cid{"data": oldTarget})

	root, _ := c.Commit(arcs)

	newTarget, _ := newPayloadCID([]byte("new"))
	newRoot, _ := c.Update(root, arcs, "data", oldTarget, newTarget)

	fmt.Printf("Update succeeded: %v\n", !newRoot.Equals(root))

	// Output:
	// Update succeeded: true
}

// ExampleScheme_BatchProve demonstrates Batch proof generation for IPA.
func ExampleScheme_BatchProve() {
	c, _ := ipa.NewScheme()

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

// ExampleScheme_AggregateProve demonstrates aggregated proof for IPA.
func ExampleScheme_AggregateProve() {
	c, _ := ipa.NewScheme()

	t1, _ := newPayloadCID([]byte("data1"))
	t2, _ := newPayloadCID([]byte("data2"))
	arcs := arcset.NewSetFrom(map[string]cid.Cid{"path1": t1, "path2": t2})

	root, _ := c.Commit(arcs)

	aggProof, _ := c.AggregateProve(root, arcs, []string{"path1", "path2"})
	fmt.Printf("Aggregated proof for %d paths\n", len(aggProof.Paths))

	valid, _ := c.AggregateVerify(root, aggProof)
	fmt.Printf("Aggregate valid: %v\n", valid)

	// Output:
	// Aggregated proof for 2 paths
	// Aggregate valid: true
}