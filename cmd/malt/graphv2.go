// Package main provides CLI commands for MALT.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/dewebprotocol/malt/core/api"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
	"github.com/spf13/cobra"
)

// graphv2Cmd is a compatibility-named demo for the canonical graph-scoped MALT path.
var graphv2Cmd = &cobra.Command{
	Use:   "graphv2",
	Short: "Demonstrate graph-scoped MALT operations",
	Long: `Demonstrates the canonical MALT runtime path.

The command name is retained for compatibility, but the demo uses the same
Node + Graph model as the rest of the CLI:
- create a graph-scoped structure
- resolve and verify explicit arcs
- update arcs with localized root advancement`,
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

	fmt.Println("=== MALT Graph-Scoped Demo ===")
	fmt.Println()

	node, err := api.NewNode()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating node: %v\n", err)
		os.Exit(1)
	}
	defer node.Close()

	g, err := node.NewGraph("graphv2-demo")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating graph: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Graph ID: %s\n", g.ID())
	fmt.Printf("Bucket:   %s\n", g.BucketId())
	fmt.Println()

	// Create an initial structure the same way the main runtime does: commit an arc set.
	target1 := createTestCID("target1")
	target2 := createTestCID("target2")
	root, err := g.Commit(ctx, arcset.NewMapFrom(map[string]cid.Cid{
		"path1": target1,
		"path2": target2,
	}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error committing initial structure: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Initial structure root: %s\n", root)
	fmt.Println()

	fmt.Println("Testing Resolve + Verify...")
	target, proof, err := g.Resolve(ctx, root, "path1")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving: %v\n", err)
		os.Exit(1)
	}
	valid, err := g.Verify(ctx, root, proof, target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error verifying: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Resolved path1: %s\n", target)
	fmt.Printf("Transcript proof size: %d bytes\n", proof.Size())
	fmt.Printf("Verified: %v\n", valid)
	fmt.Println()

	fmt.Println("Testing BatchResolve...")
	results, _, err := g.BatchResolve(ctx, root, []string{"path1", "path2"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error batch resolving: %v\n", err)
		os.Exit(1)
	}
	for path, c := range results {
		fmt.Printf("  %s -> %s\n", path, c)
	}
	fmt.Println()

	fmt.Println("Testing Update...")
	newTarget := createTestCID("new_target")
	newRoot, delta, err := g.Update(ctx, root, map[string]cid.Cid{
		"path1": newTarget,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error updating: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Old root: %s\n", delta.OldRoot)
	fmt.Printf("New root: %s\n", delta.NewRoot)
	fmt.Printf("Updated paths: %v\n", delta.Updated)
	fmt.Printf("Rewrite amplification: %.1f\n", delta.RewriteAmplification)
	fmt.Println()

	// Test snapshot
	fmt.Println("Testing Snapshot...")
	snapshot, err := g.Snapshot(ctx, newRoot)
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
	fmt.Println("Key properties demonstrated:")
	fmt.Println("1. Graph-scoped explicit structure")
	fmt.Println("2. Root passed explicitly on read and write operations")
	fmt.Println("3. Transcript-based verification")
	fmt.Println("4. Localized root advancement for updates")
}
