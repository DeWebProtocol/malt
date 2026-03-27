// Package main provides a CLI tool for MALT.
package main

import (
	"fmt"
	"os"

	"github.com/dewebprotocol/malt/pkg/malt"
	"github.com/dewebprotocol/malt/pkg/types"
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
	fmt.Println("  malt <command> [args]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  demo    Run a demo showing MALT capabilities")
	fmt.Println("  help    Show this help message")
}

func runDemo() {
	fmt.Println("=== MALT Demo ===")
	fmt.Println()

	m, err := malt.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating MALT: %v\n", err)
		os.Exit(1)
	}
	defer m.Close()

	// Create target CIDs
	target1, _ := types.NewCID([]byte("target1"))
	target2, _ := types.NewCID([]byte("target2"))

	// Create structure
	arcs := types.NewArcSetFromPairs(
		types.NewArcPair("link1", target1),
		types.NewArcPair("link2", target2),
	)

	comm, err := m.CreateStructure(arcs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating structure: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Created structure: %s\n", comm)
	fmt.Printf("  link1 -> %s\n", target1)
	fmt.Printf("  link2 -> %s\n", target2)
	fmt.Println()

	// Resolve
	resolved, proof, err := m.Resolve(comm, "link1")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving: %v\n", err)
		os.Exit(1)
	}

	valid, _ := m.Verify(comm, "link1", resolved, proof)
	fmt.Printf("Resolved link1: %s (valid: %v)\n", resolved, valid)
}