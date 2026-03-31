// Package main provides a CLI tool for MALT using Cobra.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/internal/sce"
	malt "github.com/dewebprotocol/malt/malt"
	"github.com/dewebprotocol/malt/key"
)

var (
	// Version information (set at build time)
	Version = "dev"
	cfgFile string
)

var rootCmd = &cobra.Command{
	Use:   "malt",
	Short: "MALT - Mutable structure LAyer on Top",
	Long: `MALT (Mutable structure LAyer on Top) provides verifiable, evolvable
structures on top of content-addressable storage.

It enables mutable references on immutable content-addressed data structures,
supporting cryptographic proofs and efficient updates.`,
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

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version number",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("malt version %s\n", Version)
	},
}

func init() {
	// Initialize config
	config.Init()

	// Add persistent flags (available to all subcommands)
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file")
	rootCmd.PersistentFlags().String("commitment", "", "commitment type: mock/kzg/verkle/ipa")
	rootCmd.PersistentFlags().String("kvstore", "", "KVStore type: memory/badger")
	rootCmd.PersistentFlags().String("eat", "", "EAT type: simple/versioned")
	rootCmd.PersistentFlags().String("cas", "", "CAS type: mock/ipfs-gateway")
	rootCmd.PersistentFlags().String("kv-path", "", "BadgerDB database path")
	rootCmd.PersistentFlags().String("ipfs-gateway", "", "IPFS gateway URL")
	rootCmd.PersistentFlags().Int("vector-size", 0, "vector size for commitment schemes")

	// Bind flags to viper
	viper.BindPFlag("commitment_type", rootCmd.PersistentFlags().Lookup("commitment"))
	viper.BindPFlag("kvstore_type", rootCmd.PersistentFlags().Lookup("kvstore"))
	viper.BindPFlag("eat_type", rootCmd.PersistentFlags().Lookup("eat"))
	viper.BindPFlag("cas_type", rootCmd.PersistentFlags().Lookup("cas"))
	viper.BindPFlag("kvstore.path", rootCmd.PersistentFlags().Lookup("kv-path"))
	viper.BindPFlag("cas.gateway_url", rootCmd.PersistentFlags().Lookup("ipfs-gateway"))
	viper.BindPFlag("commitment.vector_size", rootCmd.PersistentFlags().Lookup("vector-size"))

	// Add subcommands
	rootCmd.AddCommand(demoCmd)
	rootCmd.AddCommand(versionCmd)
}

func main() {
	cobra.OnInitialize(initConfig)
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func initConfig() {
	if cfgFile != "" {
		// Use config file from the flag
		viper.SetConfigFile(cfgFile)
	}

	// If a config file is found, read it in
	if err := viper.ReadInConfig(); err == nil {
		fmt.Fprintln(os.Stderr, "Using config file:", viper.ConfigFileUsed())
	}
}

func runDemo(cmd *cobra.Command, args []string) {
	// Load configuration
	cfg, err := config.LoadConfig()
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