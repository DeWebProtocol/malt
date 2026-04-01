package ipa_test

import (
	"fmt"

	"github.com/dewebprotocol/malt/internal/sce"
	"github.com/dewebprotocol/malt/internal/sce/ipa"
	"github.com/dewebprotocol/malt/key"
)

// ExampleNewCommitment demonstrates basic usage of IPA commitment.
func ExampleNewCommitment() {
	c, err := ipa.NewCommitment()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	arcs := sce.NewMapArcSetView()
	target, _ := key.NewPayloadCID([]byte("data"))
	arcs.Add("key", target)

	root, err := c.Commit(arcs)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	fmt.Printf("Committed: %v\n", root != nil)

	// Output:
	// Committed: true
}

// ExampleNewCommitment_options demonstrates using functional options.
func ExampleNewCommitment_options() {
	c, err := ipa.NewCommitment(
		ipa.WithVectorSize(128),
	)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	arcs := sce.NewMapArcSetView()
	for i := 0; i < 50; i++ {
		target, _ := key.NewPayloadCID([]byte{byte(i)})
		arcs.Add(fmt.Sprintf("item_%d", i), target)
	}

	root, _ := c.Commit(arcs)
	fmt.Printf("Committed 50 items: %v\n", root != nil)

	// Output:
	// Committed 50 items: true
}

// ExampleCommitment_Update demonstrates the fast update feature of IPA.
func ExampleCommitment_Update() {
	c, _ := ipa.NewCommitment()

	arcs := sce.NewMapArcSetView()
	oldTarget, _ := key.NewPayloadCID([]byte("old"))
	arcs.Add("data", oldTarget)

	root, _ := c.Commit(arcs)

	newTarget, _ := key.NewPayloadCID([]byte("new"))
	newRoot, _ := c.Update(root, arcs, "data", oldTarget, newTarget)

	fmt.Printf("Update succeeded: %v\n", !newRoot.Equals(root))

	// Output:
	// Update succeeded: true
}