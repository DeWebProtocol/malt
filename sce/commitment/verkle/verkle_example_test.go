package verkle_test

import (
	"fmt"

	"github.com/dewebprotocol/malt/arcset"
	"github.com/dewebprotocol/malt/sce/commitment/verkle"
	"github.com/dewebprotocol/malt/key"
)

// ExampleNewScheme demonstrates basic usage of Verkle commitment.
func ExampleNewScheme() {
	c, err := verkle.NewScheme()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	arcs := arcset.NewMap()
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

// ExampleScheme_Prove demonstrates proof generation and verification.
func ExampleScheme_Prove() {
	c, _ := verkle.NewScheme()

	arcs := arcset.NewMap()
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

// ExampleScheme_Update demonstrates updating an arc.
func ExampleScheme_Update() {
	c, _ := verkle.NewScheme()

	arcs := arcset.NewMap()
	oldTarget, _ := key.NewPayloadCID([]byte("v1"))
	arcs.Add("item", oldTarget)

	root, _ := c.Commit(arcs)

	newTarget, _ := key.NewPayloadCID([]byte("v2"))
	newRoot, _ := c.Update(root, arcs, "item", oldTarget, newTarget)

	fmt.Printf("Roots differ: %v\n", !newRoot.Equals(root))

	// Output:
	// Roots differ: true
}

// ExampleScheme_ProveBatch demonstrates batch proof generation for Verkle.
func ExampleScheme_ProveBatch() {
	c, _ := verkle.NewScheme()

	arcs := arcset.NewMap()
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

// ExampleScheme_ProveAggregate demonstrates aggregated proof for Verkle.
func ExampleScheme_ProveAggregate() {
	c, _ := verkle.NewScheme()

	arcs := arcset.NewMap()
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