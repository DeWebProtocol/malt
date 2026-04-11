package main

import (
	"context"
	"fmt"
	"os"

	"github.com/dewebprotocol/malt/sidecar"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(sidecarCmd)
}

var sidecarCmd = &cobra.Command{
	Use:   "sidecar",
	Short: "Run MALT as a sidecar alongside a local IPFS daemon",
	Long: `Run MALT as a sidecar process co-located with a local IPFS daemon.

The sidecar uses the local IPFS daemon API for both read and write
operations, unlike the gateway which uses an HTTP gateway (read-only).

This deployment mode is suitable when you want MALT to work alongside
an existing IPFS node, providing verifiable mutable structures with
full read+write CAS access.

Examples:
  malt sidecar
  malt sidecar --ipfs-api http://localhost:5001 --http-addr :8082
  malt sidecar --ipfs-api /ip4/127.0.0.1/tcp/5001 --kv-store badger --kv-path ./malt-data`,
	RunE: runSidecar,
}

var (
	sidecarIPFSAPI  string
	sidecarHTTPAddr string
	sidecarKVStore  string
	sidecarKVPath   string
	sidecarCommit   string
	sidecarEAT      string
)

func init() {
	sidecarCmd.Flags().StringVar(&sidecarIPFSAPI, "ipfs-api", "http://localhost:5001", "Local IPFS daemon API URL")
	sidecarCmd.Flags().StringVar(&sidecarHTTPAddr, "http-addr", "localhost:8082", "HTTP listen address")
	sidecarCmd.Flags().StringVar(&sidecarKVStore, "kv-store", "memory", "KVStore type (memory/badger)")
	sidecarCmd.Flags().StringVar(&sidecarKVPath, "kv-path", "", "BadgerDB data path")
	sidecarCmd.Flags().StringVar(&sidecarCommit, "commitment", "kzg", "Commitment type (kzg/verkle/ipa)")
	sidecarCmd.Flags().StringVar(&sidecarEAT, "eat", "overwrite", "EAT type (overwrite/versioned)")
}

func runSidecar(cmd *cobra.Command, args []string) error {
	cfg := sidecar.Config{
		IPFSAPIURL:     sidecarIPFSAPI,
		HTTPAddr:       sidecarHTTPAddr,
		KVStoreType:    sidecarKVStore,
		CommitmentType: sidecarCommit,
		EATType:        sidecarEAT,
	}

	// Use badger path if specified
	if sidecarKVPath != "" && sidecarKVStore == "badger" {
		// KV store path configuration would be handled via node options
		fmt.Fprintf(os.Stderr, "Warning: badger KVStore path not yet supported via sidecar CLI\n")
	}

	fmt.Println("Starting MALT sidecar...")
	fmt.Printf("  IPFS API:   %s\n", cfg.IPFSAPIURL)
	fmt.Printf("  HTTP Addr:  %s\n", cfg.HTTPAddr)
	fmt.Printf("  KVStore:    %s\n", cfg.KVStoreType)
	fmt.Printf("  Commitment: %s\n", cfg.CommitmentType)
	fmt.Printf("  EAT:        %s\n", cfg.EATType)
	fmt.Println()

	sc, err := sidecar.NewSidecar(cfg)
	if err != nil {
		return fmt.Errorf("failed to create sidecar: %w", err)
	}
	defer sc.Close()

	ctx := context.Background()
	if err := sc.Start(ctx); err != nil {
		return fmt.Errorf("sidecar error: %w", err)
	}

	return nil
}
