package verkle_test

import (
	"fmt"

	"github.com/dewebprotocol/malt/internal/sce"
	"github.com/dewebprotocol/malt/internal/sce/verkle"
	"github.com/dewebprotocol/malt/key"
)

// ExampleNewCommitment demonstrates basic usage of Verkle commitment.
func ExampleNewCommitment() {
	c, err := verkle.NewCommitment()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	arcs := sce.NewMapArcSetView()
	target, _ := key.NewPayloadCID([]byte("my-data"))
	arcs.Add("data", target)

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
	c, _ := verkle.NewCommitment()

	arcs := sce.NewMapArcSetView()
	target, _ := key.NewPayloadCID([]byte("content"))
	arcs.Add("path", target)

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

// ExampleCommitment_Update demonstrates updating an arc.
func ExampleCommitment_Update() {
	c, _ := verkle.NewCommitment()

	arcs := sce.NewMapArcSetView()
	oldTarget, _ := key.NewPayloadCID([]byte("v1"))
	arcs.Add("item", oldTarget)

	root, _ := c.Commit(arcs)

	newTarget, _ := key.NewPayloadCID([]byte("v2"))
	newRoot, _ := c.Update(root, arcs, "item", oldTarget, newTarget)

	fmt.Printf("Roots differ: %v\n", !newRoot.Equals(root))

	// Output:
	// Roots differ: true
}

// ExampleCommitment_ProveBatch demonstrates batch proof generation for Verkle.
func ExampleCommitment_ProveBatch() {
	c, _ := verkle.NewCommitment()

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

// ExampleCommitment_ProveAggregate demonstrates aggregated proof for Verkle.
func ExampleCommitment_ProveAggregate() {
	c, _ := verkle.NewCommitment()

	arcs := sce.NewMapArcSetView()
	t1, _ := key.NewPayloadCID([]byte("data1"))
	t2, _ := key.NewPayloadCID([]byte("data2"))
	arcs.Add("path1", t1)
	arcs.Add("path2", t2)

	root, _ := c.Commit(arcs)

	aggProof, _ := c.ProveAggregate(root, arcs, []string{"path1", "path2"})
	fmt.Printf("Aggregated proof for %d paths\n", len(aggProof.Paths))

	valid, _ := c.VerifyAggregate(root, aggProof)
	fmt.Printf("Aggregate valid: %v\n", valid)

	// Output:
	// Aggregated proof for 2 paths
	// Aggregate valid: true
}