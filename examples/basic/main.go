// Package main demonstrates basic MALT usage with functional options.
package main

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt/core/api"
	"github.com/dewebprotocol/malt/core/kvstore/badger"
	"github.com/dewebprotocol/malt/core/graph"
	"github.com/dewebprotocol/malt/core/sce/commitment/kzg"
	"github.com/dewebprotocol/malt/core/types/arcset"
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
	node, err := api.NewNode()
	if err != nil {
		panic(err)
	}
	defer node.Close()

	fmt.Printf("Node initialized\n")

	// Create graph
	g, err := node.NewGraph("demo-graph")
	if err != nil {
		panic(err)
	}
	ctx := context.Background()

	// Create structure
	target1, _ := newPayloadCID([]byte("document.pdf"))
	target2, _ := newPayloadCID([]byte("image.png"))
	snapshot := arcset.NewMapFrom(map[string]cid.Cid{
		"document": target1,
		"image":    target2,
	})

	root, err := g.Commit(ctx, snapshot)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Created structure: %s\n", root)
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
	node, err := api.NewNode(
		api.WithKVStore(kvStore),
	)
	if err != nil {
		panic(err)
	}
	defer node.Close()

	fmt.Printf("Node initialized with custom components\n")

	// Create graph with custom commitment scheme
	g, err := node.NewGraph("demo-graph", graph.WithCommitmentScheme(scheme))
	if err != nil {
		panic(err)
	}
	ctx := context.Background()

	// Create structure
	target1, _ := newPayloadCID([]byte("data.json"))
	snapshot := arcset.NewMapFrom(map[string]cid.Cid{"data": target1})

	root, err := g.Commit(ctx, snapshot)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Created structure: %s\n", root)
	fmt.Printf("  - data -> %s\n", target1)

	// Resolve and verify
	result, err := g.Resolver().Resolve(root, "data")
	if err != nil {
		panic(err)
	}

	proof := graph.NewTranscriptProof(result.Transcript)
	valid, _ := g.Verify(ctx, root, proof, result.Target)
	fmt.Printf("Resolved 'data': %s (valid: %v)\n", result.Target, valid)
}
