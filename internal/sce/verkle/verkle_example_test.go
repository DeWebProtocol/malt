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