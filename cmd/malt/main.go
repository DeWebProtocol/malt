// Package main provides a CLI tool for MALT.
package main

import (
	"fmt"
	"os"

	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/internal/sce"
	malt "github.com/dewebprotocol/malt/malt"
	"github.com/dewebprotocol/malt/key"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]

	switch cmd {
	case "demo":
		runDemo()
	case "help":
		printUsage()
	default:
		fmt.Printf("Unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("MALT - Mutable structure LAyer on Top")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  malt <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  demo    Run a demo showing MALT capabilities")
	fmt.Println("  help    Show this help message")
	fmt.Println()
	fmt.Println("Options (for demo and future commands):")
	fmt.Println("  -config <path>        Config file path (JSON)")
	fmt.Println("  -commitment <type>    Commitment type: mock/kzg/verkle/ipa")
	fmt.Println("  -kvstore <type>       KVStore type: memory/badger")
	fmt.Println("  -eat <type>           EAT type: simple/versioned")
	fmt.Println("  -cas <type>           CAS type: mock/ipfs-gateway")
	fmt.Println("  -kv-path <path>       BadgerDB database path")
	fmt.Println("  -ipfs-gateway <url>   IPFS gateway URL")
	fmt.Println()
	fmt.Println("Environment variables:")
	fmt.Println("  MALT_COMMITMENT       Commitment type")
	fmt.Println("  MALT_KVSTORE          KVStore type")
	fmt.Println("  MALT_EAT              EAT type")
	fmt.Println("  MALT_CAS              CAS type")
	fmt.Println("  MALT_KV_PATH          BadgerDB path")
	fmt.Println("  MALT_IPFS_GATEWAY     IPFS gateway URL")
}

func runDemo() {
	// Load configuration from file, env, and flags
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("=== MALT Demo ===")
	fmt.Println()
	fmt.Printf("Configuration: %s\n", cfg)
	fmt.Println()

	// Create node with injected dependencies
	node, err := malt.NewNode(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating node: %v\n", err)
		os.Exit(1)
	}
	defer node.Close()

	// Create target CIDs
	target1, _ := key.NewPayloadCID([]byte("target1"))
	target2, _ := key.NewPayloadCID([]byte("target2"))

	// Create arc set
	arcs := sce.NewMapArcSetView()
	arcs.Add("link1", target1)
	arcs.Add("link2", target2)

	// Create structure using node
	structure, err := node.NewStructure(arcs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating structure: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Created structure: %s\n", structure.Root())
	fmt.Printf("  link1 -> %s\n", target1)
	fmt.Printf("  link2 -> %s\n", target2)
	fmt.Println()

	// Resolve
	resolved, proof, err := structure.Resolve("link1")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving: %v\n", err)
		os.Exit(1)
	}

	valid, _ := structure.Verify("link1", resolved, proof)
	fmt.Printf("Resolved link1: %s (valid: %v)\n", resolved, valid)

	// Update
	newTarget, _ := key.NewPayloadCID([]byte("new_target"))
	newStructure, err := structure.Update("link1", newTarget)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error updating: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Updated link1 -> %s\n", newTarget)
	fmt.Printf("New root: %s\n", newStructure.Root())

	fmt.Println()
	fmt.Println("=== Demo Complete ===")
}