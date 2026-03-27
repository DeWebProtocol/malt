// Package main demonstrates basic MALT usage.
package main

import (
	"fmt"

	"github.com/dewebprotocol/malt/pkg/malt"
	"github.com/dewebprotocol/malt/pkg/types"
)

func main() {
	fmt.Println("=== MALT (Mutable structure LAyer on Top) Demo ===")
	fmt.Println()

	// Create MALT instance
	m, err := malt.New()
	if err != nil {
		panic(err)
	}
	defer m.Close()

	// Simulate target CIDs (in practice, these would come from CAS/IPFS)
	target1CID, _ := types.NewCID([]byte("document.pdf"))
	target2CID, _ := types.NewCID([]byte("image.png"))
	target3CID, _ := types.NewCID([]byte("data.json"))

	fmt.Println("Step 1: Create initial structure")
	fmt.Println("--------------------------------")

	// Create a structure with explicit arcs
	arcs := types.NewArcSetFromPairs(
		types.NewArcPair("document", target1CID),
		types.NewArcPair("image", target2CID),
	)

	comm, err := m.CreateStructure(arcs)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Created structure with commitment: %s\n", comm)
	fmt.Printf("  - document -> %s\n", target1CID)
	fmt.Printf("  - image -> %s\n", target2CID)
	fmt.Println()

	// Resolve and verify
	fmt.Println("Step 2: Resolve and verify arcs")
	fmt.Println("--------------------------------")

	resolved, proof, err := m.Resolve(comm, "document")
	if err != nil {
		panic(err)
	}

	valid, err := m.Verify(comm, "document", resolved, proof)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Resolved 'document': %s\n", resolved)
	fmt.Printf("Proof valid: %v\n", valid)
	fmt.Println()

	// Update an arc
	fmt.Println("Step 3: Update arc (localized update)")
	fmt.Println("--------------------------------------")

	newComm, err := m.UpdateArc(comm, "document", target3CID)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Updated 'document' -> %s\n", target3CID)
	fmt.Printf("New commitment: %s\n", newComm)
	fmt.Printf("Old commitment preserved: %s\n", comm)
	fmt.Println()

	// Verify new commitment
	resolved, proof, err = m.Resolve(newComm, "document")
	if err != nil {
		panic(err)
	}

	valid, _ = m.Verify(newComm, "document", resolved, proof)
	fmt.Printf("Resolved 'document' with new commitment: %s\n", resolved)
	fmt.Printf("Proof valid: %v\n", valid)
	fmt.Println()

	// Add new arc
	fmt.Println("Step 4: Add new arc")
	fmt.Println("-------------------")

	comm2, err := m.AddArc(newComm, "data", target3CID)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Added 'data' -> %s\n", target3CID)
	fmt.Printf("New commitment: %s\n", comm2)
	fmt.Println()

	// Get lineage
	fmt.Println("Step 5: Get commitment lineage")
	fmt.Println("-------------------------------")

	lineage, err := m.GetLineage(comm2)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Lineage (newest to oldest):\n")
	for i, c := range lineage {
		fmt.Printf("  %d. %s\n", i+1, c)
	}
	fmt.Println()

	// Stats
	fmt.Println("Statistics")
	fmt.Println("----------")
	stats := m.Stats()
	fmt.Printf("Total structures: %d\n", stats.StructureCount)

	fmt.Println()
	fmt.Println("=== Demo Complete ===")
}