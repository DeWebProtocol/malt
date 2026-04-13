// Package main provides CLI commands for MALT.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/dewebprotocol/malt/core/deployment"
	"github.com/dewebprotocol/malt/core/kvstore/memory"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
	"github.com/spf13/cobra"
)

// graphv2Cmd demonstrates the new stateless Graph architecture.
var graphv2Cmd = &cobra.Command{
	Use:   "graphv2",
	Short: "Demonstrate the new stateless Graph architecture",
	Long: `Demonstrates the refactored MALT architecture:
- Deployment as composition factory
- Stateless Graph with root as parameter
- Storage injection (ArcStore, ContentStore)
- Batch operations with AggregatedProof`,
	Run: runGraphv2Demo,
}

func init() {
	rootCmd.AddCommand(graphv2Cmd)
}

// createTestCID creates a test CID from data.
func createTestCID(data string) cid.Cid {
	mhash, _ := mh.Sum([]byte(data), mh.SHA2_256, -1)
	return cid.NewCidV1(cid.Raw, mhash)
}

func runGraphv2Demo(cmd *cobra.Command, args []string) {
	ctx := context.Background()

	fmt.Println("=== MALT Graphv2 Architecture Demo ===")
	fmt.Println()

	// Create deployment (composition factory)
	kv := memory.New()
	d := deployment.NewMemoryDeployment(kv)
	defer d.Close()

	fmt.Printf("Deployment: %s\n", d.Name())
	fmt.Printf("  ArcStore:    %T\n", d.ArcStore())
	fmt.Printf("  ContentStore: %T\n", d.ContentStore())
	fmt.Printf("  Backend:    %s\n", d.CommitmentBackend().Name())
	fmt.Println()

	// Initialize empty graph
	root, err := d.InitializeGraph(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing graph: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Initial root (empty graph): %s\n", root)
	fmt.Println()

	// Create Graph instance
	graph, err := d.CreateGraph()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating graph: %v\n", err)
		os.Exit(1)
	}

	// Test update
	fmt.Println("Testing Update...")
	newRoot, delta, err := graph.Update(ctx, root, map[string]cid.Cid{
		"path1": createTestCID("target1"),
		"path2": createTestCID("target2"),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error updating: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Update result:\n")
	fmt.Printf("  Old root: %s\n", delta.OldRoot)
	fmt.Printf("  New root: %s\n", delta.NewRoot)
	fmt.Printf("  Added: %v\n", delta.Added)
	fmt.Printf("  Rewrite amplification: %.1f\n", delta.RewriteAmplification)
	fmt.Println()

	// Test resolve
	fmt.Println("Testing Resolve...")
	target, proof, err := graph.Resolve(ctx, newRoot, "path1")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Resolve result:\n")
	fmt.Printf("  Path: path1\n")
	fmt.Printf("  Target: %s\n", target)
	fmt.Printf("  Proof size: %d bytes\n", proof.Size())
	fmt.Println()

	// Test batch resolve
	fmt.Println("Testing BatchResolve...")
	results, aggProof, err := graph.BatchResolve(ctx, newRoot, []string{"path1", "path2"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error batch resolving: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("BatchResolve result:\n")
	for path, cid := range results {
		fmt.Printf("  %s -> %s\n", path, cid)
	}
	if aggProof != nil {
		fmt.Printf("  Aggregated proof size: %d bytes\n", aggProof.Size())
	}
	fmt.Println()

	// Test snapshot
	fmt.Println("Testing Snapshot...")
	snapshot, err := graph.Snapshot(ctx, newRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error snapshotting: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Snapshot result:\n")
	fmt.Printf("  Arc count: %d\n", snapshot.Len())
	iter := snapshot.Iterate()
	for {
		path, c, ok := iter.Next()
		if !ok {
			break
		}
		fmt.Printf("  %s -> %s\n", path, c)
	}
	iter.Close()
	fmt.Println()

	fmt.Println("=== Demo Complete ===")
	fmt.Println()
	fmt.Println("Key benefits of new architecture:")
	fmt.Println("1. Stateless Graph: root passed as parameter, no concurrency control needed")
	fmt.Println("2. Storage injection: flexible deployment configurations")
	fmt.Println("3. Rewrite amplification = 1.0: localized updates")
	fmt.Println("4. Batch operations: efficient bulk resolution and proof generation")
}