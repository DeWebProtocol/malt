// Package main demonstrates basic MALT usage.
package main

import (
	"fmt"

	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/internal/sce"
	malt "github.com/dewebprotocol/malt/malt"
	"github.com/dewebprotocol/malt/key"
)

func main() {
	fmt.Println("=== MALT (Mutable structure LAyer on Top) Demo ===")
	fmt.Println()

	// Method 1: Use default configuration
	fmt.Println("Method 1: Default configuration")
	fmt.Println("--------------------------------")
	runWithConfig(config.DefaultConfig())
	fmt.Println()

	// Method 2: Custom configuration
	fmt.Println("Method 2: Custom configuration")
	fmt.Println("-------------------------------")
	cfg := &config.Config{
		CommitmentType: "mock",
		KVStoreType:    "memory",
		EATType:        "simple",
		CASType:        "mock",
		KVStore: config.KVStoreConfig{
			InMemory: true,
		},
		Commitment: config.CommitmentConfig{
			VectorSize: 256,
		},
		CAS: config.CASConfig{
			GatewayURL: "https://ipfs.io/ipfs",
			Timeout:    "30s",
		},
	}
	runWithConfig(cfg)
}

func runWithConfig(cfg *config.Config) {
	// Create node - this initializes all components based on config
	node, err := malt.NewNode(cfg)
	if err != nil {
		panic(err)
	}
	defer node.Close()

	fmt.Printf("Node initialized with: %s\n", cfg)

	// Simulate target CIDs
	target1CID, _ := key.NewPayloadCID([]byte("document.pdf"))
	target2CID, _ := key.NewPayloadCID([]byte("image.png"))
	target3CID, _ := key.NewPayloadCID([]byte("data.json"))

	fmt.Println("\nStep 1: Create initial structure")
	fmt.Println("--------------------------------")

	// Create a structure with explicit arcs
	arcs := sce.NewMapArcSetView()
	arcs.Add("document", target1CID)
	arcs.Add("image", target2CID)

	// Use node to create structure (injects EAT and commitment automatically)
	structure, err := node.NewStructure(arcs)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Created structure with commitment: %s\n", structure.Root())
	fmt.Printf("  - document -> %s\n", target1CID)
	fmt.Printf("  - image -> %s\n", target2CID)

	// Resolve and verify
	fmt.Println("\nStep 2: Resolve and verify arcs")
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

	// Update an arc
	fmt.Println("\nStep 3: Update arc (localized update)")
	fmt.Println("--------------------------------------")

	updatedStructure, err := structure.Update("document", target3CID)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Updated 'document' -> %s\n", target3CID)
	fmt.Printf("New commitment: %s\n", updatedStructure.Root())
	fmt.Printf("Old commitment preserved: %s\n", structure.Root())

	// Add new arc
	fmt.Println("\nStep 4: Add new arc")
	fmt.Println("-------------------")

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

	structure2, err := node.NewStructure(newArcs)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Added 'data' -> %s\n", target3CID)
	fmt.Printf("New commitment: %s\n", structure2.Root())

	// Stats
	fmt.Println("\nStatistics")
	fmt.Println("----------")
	fmt.Printf("Arc count: %d\n", structure2.GetArcSet().Len())

	fmt.Println("\n=== Demo Complete ===")
}