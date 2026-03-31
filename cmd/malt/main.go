// Package main provides a CLI tool for MALT.
package main

import (
	"fmt"
	"os"

	"github.com/dewebprotocol/malt/internal/eat/simple"
	"github.com/dewebprotocol/malt/internal/sce"
	scemock "github.com/dewebprotocol/malt/internal/sce/mock"
	"github.com/dewebprotocol/malt/key"
	malt "github.com/dewebprotocol/malt/malt"
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

	// Create components
	e := simple.NewEAT()
	s := scemock.NewCommitment(256)

	// Create target CIDs
	target1, _ := key.NewPayloadCID([]byte("target1"))
	target2, _ := key.NewPayloadCID([]byte("target2"))

	// Create arc set
	arcs := sce.NewMapArcSetView()
	arcs.Add("link1", target1)
	arcs.Add("link2", target2)

	// Create structure
	structure, err := malt.NewStructure(arcs, e, s)
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
}