// Package main demonstrates basic MALT usage with functional options.
package main

import (
	"fmt"

	"github.com/dewebprotocol/malt/core/eat/memory"
	"github.com/dewebprotocol/malt/core/types/kvstore/badger"
	"github.com/dewebprotocol/malt/core/sce/commitment/kzg"
	malt "github.com/dewebprotocol/malt/malt"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// newPayloadCID creates a CID from data for testing.
func newPayloadCID(data []byte) (cid.Cid, error) {
	mhash, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		return cid.Cid{}, err
	}
	return cid.NewCidV1(cid.Raw, mhash), nil
}

func main() {
	fmt.Println("=== MALT (Mutable structure LAyer on Top) Demo ===")
	fmt.Println()

	// Method 1: Use defaults
	fmt.Println("Method 1: Default configuration")
	fmt.Println("--------------------------------")
	runWithDefaults()
	fmt.Println()

	// Method 2: Custom components with options
	fmt.Println("Method 2: Custom components (functional options)")
	fmt.Println("-------------------------------------------------")
	runWithOptions()
}

func runWithDefaults() {
	// Create node with defaults
	node, err := malt.NewNode()
	if err != nil {
		panic(err)
	}
	defer node.Close()

	fmt.Printf("Node initialized\n")

	// Create structure
	arcs := memory.NewInMemoryArcSet()
	target1, _ := newPayloadCID([]byte("document.pdf"))
	target2, _ := newPayloadCID([]byte("image.png"))
	arcs.Set("document", target1)
	arcs.Set("image", target2)

	structure, err := node.NewStructure(arcs)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Created structure: %s\n", structure.Root())
	fmt.Printf("  - document -> %s\n", target1)
	fmt.Printf("  - image -> %s\n", target2)
}

func runWithOptions() {
	// Create custom components with options
	kvStore, err := badger.New(
		badger.WithInMemory(true),
	)
	if err != nil {
		panic(err)
	}

	scheme, err := kzg.NewScheme()
	if err != nil {
		panic(err)
	}

	// Create node with custom components
	node, err := malt.NewNode(
		malt.WithKVStore(kvStore),
		malt.WithCommitment(scheme),
	)
	if err != nil {
		panic(err)
	}
	defer node.Close()

	fmt.Printf("Node initialized with custom components\n")

	// Create structure
	arcs := memory.NewInMemoryArcSet()
	target1, _ := newPayloadCID([]byte("data.json"))
	arcs.Set("data", target1)

	structure, err := node.NewStructure(arcs)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Created structure: %s\n", structure.Root())
	fmt.Printf("  - data -> %s\n", target1)

	// Resolve and verify
	resolved, proof, err := structure.Resolve("data")
	if err != nil {
		panic(err)
	}

	valid, _ := structure.Verify("data", resolved, proof)
	fmt.Printf("Resolved 'data': %s (valid: %v)\n", resolved, valid)
}