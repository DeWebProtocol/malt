// Package main provides the primary MALT CLI.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/core/api"
	"github.com/dewebprotocol/malt/core/graph"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
	"github.com/spf13/cobra"
)

var (
	Version = "dev"
	cfgFile string
)

// newPayloadCID creates a CID from data for testing.
func newPayloadCID(data []byte) (cid.Cid, error) {
	mhash, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		return cid.Cid{}, err
	}
	return cid.NewCidV1(cid.Raw, mhash), nil
}

var rootCmd = &cobra.Command{
	Use:   "malt",
	Short: "MALT runtime CLI",
	Long: `MALT is an authenticated structure layer over immutable content-addressed storage.

Primary commands:
  init        Create ~/.malt/malt.json and choose local state paths
  daemon      Run the local MALT daemon
  graph       Managed graph lifecycle operations via the daemon
  resolve     Resolve a path via the daemon
  update      Mutate structure via the daemon
  prove       Resolve and print transcript evidence via the daemon
  verify      Verify a transcript via the daemon
  lineage     Query lineage via the daemon
  cas         Interact directly with the configured CAS endpoint

Developer commands such as demo, benchmark, eval, and replication remain available
for direct library-driven workflows and evaluation scaffolding.`,
	Version: Version,
}

var demoCmd = &cobra.Command{
	Use:   "demo",
	Short: "Run a demo showing MALT capabilities",
	Run:   runDemo,
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file (default: ~/.malt/malt.json)")
	rootCmd.AddCommand(demoCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runDemo(cmd *cobra.Command, args []string) {
	cfg, err := loadRuntimeConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	node, err := api.NewNode(api.WithConfig(cfg))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating node: %v\n", err)
		os.Exit(1)
	}
	defer node.Close()

	g, err := node.NewGraph("demo")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating graph: %v\n", err)
		os.Exit(1)
	}
	ctx := context.Background()

	fmt.Println("=== MALT Demo ===")
	fmt.Println()
	fmt.Printf("Configuration: %s\n", node.Config())
	fmt.Println()

	target1, _ := newPayloadCID([]byte("target1"))
	target2, _ := newPayloadCID([]byte("target2"))

	snapshot := arcset.NewSetFrom(map[string]cid.Cid{
		"link1": target1,
		"link2": target2,
	})

	root, err := g.Commit(ctx, snapshot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating structure: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Created structure: %s\n", root)
	fmt.Printf("  link1 -> %s\n", target1)
	fmt.Printf("  link2 -> %s\n", target2)
	fmt.Println()

	result, err := g.Resolver().Resolve(root, "link1")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving: %v\n", err)
		os.Exit(1)
	}

	resolved := result.Target
	proof := graph.NewTranscriptProof(result.Transcript)
	valid, _ := g.Verify(ctx, root, proof, resolved)
	fmt.Printf("Resolved link1: %s (valid: %v)\n", resolved, valid)

	newTarget, _ := newPayloadCID([]byte("new_target"))
	newRoot, delta, err := g.Update(ctx, root, map[string]cid.Cid{
		"link1": newTarget,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error updating: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Updated link1 -> %s\n", newTarget)
	fmt.Printf("New root: %s\n", newRoot)
	fmt.Printf("Changes: added=%v updated=%v deleted=%v\n", delta.Added, delta.Updated, delta.Deleted)

	fmt.Println()
	fmt.Println("=== Demo Complete ===")
}

func loadRuntimeConfig() (*config.Config, error) {
	if cfgFile != "" {
		return config.LoadFromFile(cfgFile)
	}
	return config.Load()
}
