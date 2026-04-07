package verkle_test

import (
	"fmt"

	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/sce/commitment/verkle"
	cid "github.com/ipfs/go-cid"
)

// ExampleNewScheme demonstrates basic usage of Verkle commitment.
func ExampleNewScheme() {
	c, err := verkle.NewScheme()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	target, _ := newPayloadCID([]byte("my-data"))
	arcs := arcset.NewMapFrom(map[string]cid.Cid{"data": target})

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
	c, _ := verkle.NewScheme()

	target, _ := newPayloadCID([]byte("content"))
	arcs := arcset.NewMapFrom(map[string]cid.Cid{"path": target})

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

// ExampleScheme_Update demonstrates updating an arc.
func ExampleScheme_Update() {
	c, _ := verkle.NewScheme()

	oldTarget, _ := newPayloadCID([]byte("v1"))
	arcs := arcset.NewMapFrom(map[string]cid.Cid{"item": oldTarget})

	root, _ := c.Commit(arcs)

	newTarget, _ := newPayloadCID([]byte("v2"))
	newRoot, _ := c.Update(root, arcs, "item", oldTarget, newTarget)

	fmt.Printf("Roots differ: %v\n", !newRoot.Equals(root))

	// Output:
	// Roots differ: true
}

// ExampleScheme_BatchProve demonstrates Batch proof generation for Verkle.
func ExampleScheme_BatchProve() {
	c, _ := verkle.NewScheme()

	t1, _ := newPayloadCID([]byte("data1"))
	t2, _ := newPayloadCID([]byte("data2"))
	arcs := arcset.NewMapFrom(map[string]cid.Cid{"path1": t1, "path2": t2})

	root, _ := c.Commit(arcs)

	proofs, _ := c.BatchProve(root, arcs, []string{"path1", "path2"})
	fmt.Printf("Generated %d proofs\n", len(proofs))

	valid, _ := c.BatchVerify(root, proofs)
	fmt.Printf("Batch valid: %v\n", valid)

	// Output:
	// Generated 2 proofs
	// Batch valid: true
}

// ExampleScheme_AggregateProve demonstrates aggregated proof for Verkle.
func ExampleScheme_AggregateProve() {
	c, _ := verkle.NewScheme()

	t1, _ := newPayloadCID([]byte("data1"))
	t2, _ := newPayloadCID([]byte("data2"))
	arcs := arcset.NewMapFrom(map[string]cid.Cid{"path1": t1, "path2": t2})

	root, _ := c.Commit(arcs)

	aggProof, _ := c.AggregateProve(root, arcs, []string{"path1", "path2"})
	fmt.Printf("Aggregated proof for %d paths\n", len(aggProof.Paths))

	valid, _ := c.AggregateVerify(root, aggProof)
	fmt.Printf("Aggregate valid: %v\n", valid)

	// Output:
	// Aggregated proof for 2 paths
	// Aggregate valid: true
}