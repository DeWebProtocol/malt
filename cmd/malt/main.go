// Package main provides a CLI tool for MALT using Cobra.
package main

import (
	"fmt"
	"os"

	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/core/api"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
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
	Short: "MALT - Mutable structure LAyer on Top",
	Long: `MALT (Mutable structure LAyer on Top) provides verifiable, evolvable
structures on top of content-addressed storage.

It enables mutable references on immutable content-addressed data structures,
supporting cryptographic proofs and efficient updates.

Commands:
  graph       Manage graphs (create, delete, list, freeze)
  resolve     Resolve paths through MALT structures
  update      Update arcs in structures
  prove       Generate proofs for path resolution
  verify      Verify resolution transcripts
  benchmark   Run quick benchmarks
  eval        Run comprehensive evaluation
  replication Export, import, and sync graph snapshots`,
	Version: Version,
}

var demoCmd = &cobra.Command{
	Use:   "demo",
	Short: "Run a demo showing MALT capabilities",
	Long: `Demonstrates the core features of MALT:
- Creating structures with explicit arcs
- Resolving and verifying arcs
- Localized updates with new commitments`,
	Run: runDemo,
}

func init() {
	config.Init()

	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file")
	rootCmd.PersistentFlags().String("commitment", "", "commitment type: kzg/verkle/ipa")
	rootCmd.PersistentFlags().String("kvstore", "", "KVStore type: memory/badger")
	rootCmd.PersistentFlags().String("eat", "", "EAT type: simple/versioned")
	rootCmd.PersistentFlags().String("cas", "", "CAS type: mock/ipfs-gateway")
	rootCmd.PersistentFlags().String("kv-path", "", "BadgerDB database path")
	rootCmd.PersistentFlags().String("ipfs-gateway", "", "IPFS gateway URL")
	rootCmd.PersistentFlags().Int("vector-size", 0, "vector size for commitment schemes")

	viper.BindPFlag("commitment_type", rootCmd.PersistentFlags().Lookup("commitment"))
	viper.BindPFlag("kvstore_type", rootCmd.PersistentFlags().Lookup("kvstore"))
	viper.BindPFlag("eat_type", rootCmd.PersistentFlags().Lookup("eat"))
	viper.BindPFlag("cas_type", rootCmd.PersistentFlags().Lookup("cas"))
	viper.BindPFlag("kvstore.path", rootCmd.PersistentFlags().Lookup("kv-path"))
	viper.BindPFlag("cas.gateway_url", rootCmd.PersistentFlags().Lookup("ipfs-gateway"))
	viper.BindPFlag("commitment.vector_size", rootCmd.PersistentFlags().Lookup("vector-size"))

	rootCmd.AddCommand(demoCmd)
}

func main() {
	cobra.OnInitialize(loadConfig)
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runDemo(cmd *cobra.Command, args []string) {
	// Build node options
	var nodeOpts []api.Option

	if cfgFile != "" {
		nodeOpts = append(nodeOpts, api.WithConfigFile(cfgFile))
	}

	// Create node
	node, err := api.NewNode(nodeOpts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating node: %v\n", err)
		os.Exit(1)
	}
	defer node.Close()

	fmt.Println("=== MALT Demo ===")
	fmt.Println()
	fmt.Printf("Configuration: %s\n", node.Config())
	fmt.Println()

	// Create target CIDs
	target1, _ := newPayloadCID([]byte("target1"))
	target2, _ := newPayloadCID([]byte("target2"))

	// Create arc set
	arcs := arcset.NewMapFrom(map[string]cid.Cid{
		"link1": target1,
		"link2": target2,
	})

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
	newTarget, _ := newPayloadCID([]byte("new_target"))
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
