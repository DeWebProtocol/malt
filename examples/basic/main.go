// Package main demonstrates basic MALT usage.
package main

import (
	"fmt"

	"github.com/dewebprotocol/malt/internal/eat/simple"
	"github.com/dewebprotocol/malt/internal/sce"
	scemock "github.com/dewebprotocol/malt/internal/sce/mock"
	malt "github.com/dewebprotocol/malt/malt"
	"github.com/dewebprotocol/malt/key"
)

func main() {
	fmt.Println("=== MALT (Mutable structure LAyer on Top) Demo ===")
	fmt.Println()

	// Create components
	e := simple.NewEAT()
	s := scemock.NewCommitment(256)

	// Simulate target CIDs (in practice, these would come from CAS/IPFS)
	target1CID, _ := key.NewPayloadCID([]byte("document.pdf"))
	target2CID, _ := key.NewPayloadCID([]byte("image.png"))
	target3CID, _ := key.NewPayloadCID([]byte("data.json"))

	fmt.Println("Step 1: Create initial structure")
	fmt.Println("--------------------------------")

	// Create a structure with explicit arcs
	arcs := sce.NewMapArcSetView()
	arcs.Add("document", target1CID)
	arcs.Add("image", target2CID)

	structure, err := malt.NewStructure(arcs, e, s)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Created structure with commitment: %s\n", structure.Root())
	fmt.Printf("  - document -> %s\n", target1CID)
	fmt.Printf("  - image -> %s\n", target2CID)
	fmt.Println()

	// Resolve and verify
	fmt.Println("Step 2: Resolve and verify arcs")
	fmt.Println("--------------------------------")

	resolved, proof, err := structure.Resolve("document")
	if err != nil {
		panic(err)
	}

	valid, err := structure.Verify("document", resolved, proof)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Resolved 'document': %s\n", resolved)
	fmt.Printf("Proof valid: %v\n", valid)
	fmt.Println()

	// Update an arc
	fmt.Println("Step 3: Update arc (localized update)")
	fmt.Println("--------------------------------------")

	updatedStructure, err := structure.Update("document", target3CID)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Updated 'document' -> %s\n", target3CID)
	fmt.Printf("New commitment: %s\n", updatedStructure.Root())
	fmt.Printf("Old commitment preserved: %s\n", structure.Root())
	fmt.Println()

	// Verify new commitment
	resolved, proof, err = updatedStructure.Resolve("document")
	if err != nil {
		panic(err)
	}

	valid, _ = updatedStructure.Verify("document", resolved, proof)
	fmt.Printf("Resolved 'document' with new commitment: %s\n", resolved)
	fmt.Printf("Proof valid: %v\n", valid)
	fmt.Println()

	// Add new arc by updating the arc set
	fmt.Println("Step 4: Add new arc")
	fmt.Println("-------------------")

	// Get current arcs and add new one
	newArcs := sce.NewMapArcSetView()
	currentArcs := updatedStructure.GetArcSet()
	iter := currentArcs.Iterate()
	for {
		p, k, ok := iter.Next()
		if !ok {
			break
		}
		newArcs.Add(p, k)
	}
	newArcs.Add("data", target3CID)

	// Create new structure with updated arcs
	structure2, err := malt.NewStructure(newArcs, e, s)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Added 'data' -> %s\n", target3CID)
	fmt.Printf("New commitment: %s\n", structure2.Root())
	fmt.Println()

	// Stats
	fmt.Println("Statistics")
	fmt.Println("----------")
	fmt.Printf("Arc count: %d\n", structure2.GetArcSet().Len())

	fmt.Println()
	fmt.Println("=== Demo Complete ===")
}