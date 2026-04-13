// Package main provides an HTTP Gateway for MALT.
// It exposes MALT resolution, graph management, and write-side operations via HTTP endpoints.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/core/api"
	"github.com/dewebprotocol/malt/core/cas/ipfslocal"
	"github.com/dewebprotocol/malt/gateway"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	Version = "dev"
	cfgFile string
	listen  string
	ipfsAPI string
)

func main() {
	config.Init()

	var rootCmd = &cobra.Command{
		Use:   "malt-gateway",
		Short: "MALT HTTP Gateway",
		Long: `HTTP Gateway for MALT (Mutable structure LAyer on Top).

Provides HTTP endpoints for:
- Graph management (POST /graph, GET /graph/{id}, DELETE /graph/{id}, GET /graphs)
- Hybrid resolution (GET /resolve/{root}/{path}, POST /resolve)
- Proof queries (GET /proof/{root}/{path})
- Arc queries (GET /arc/{root}/{path}, GET /snapshot/{root})
- Content fetch (GET /content/{cid})
- Write operations (POST /update/{root}/{path}, POST /update/batch/{root})
- Structure creation (POST /structure)
- Verification (POST /verify)
- Health check (GET /health)

By default, the gateway uses a mock CAS for testing. Use --ipfs-api to connect
to a local IPFS daemon for full read+write CAS access (the former "sidecar" mode).

Examples:
  malt-gateway                          # mock CAS, :8080
  malt-gateway --ipfs-api :5001         # local IPFS daemon, :8080
  malt-gateway --ipfs-api :5001 -l :9090  # local IPFS, custom port`,
		Version: Version,
		Run:     runGateway,
	}

	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file")
	rootCmd.PersistentFlags().StringVarP(&listen, "listen", "l", ":8080", "listen address")
	rootCmd.PersistentFlags().StringVar(&ipfsAPI, "ipfs-api", "", "Local IPFS daemon API URL (enables full read+write CAS)")

	viper.BindPFlag("listen", rootCmd.PersistentFlags().Lookup("listen"))

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runGateway(cmd *cobra.Command, args []string) {
	// Build MALT node options
	var nodeOpts []api.Option
	if cfgFile != "" {
		nodeOpts = append(nodeOpts, api.WithConfigFile(cfgFile))
	}

	// If --ipfs-api is specified, use local IPFS daemon as CAS (full read+write)
	if ipfsAPI != "" {
		casClient := ipfslocal.NewClient("http://" + ipfsAPI)
		nodeOpts = append(nodeOpts, api.WithCAS(casClient))
		log.Printf("Using local IPFS daemon as CAS: %s", ipfsAPI)
	}

	node, err := api.NewNode(nodeOpts...)
	if err != nil {
		log.Fatalf("Failed to create MALT node: %v", err)
	}

	// Create adapter for the gateway
	adapter := gateway.NewNodeAdapter(node)
	defer adapter.Close()

	log.Printf("MALT Gateway v%s starting...", Version)
	log.Printf("Configuration: %s", adapter.Config())

	// Create gateway server
	srv := gateway.NewServer(adapter, listen)

	// Handle graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	// Start server in goroutine
	go func() {
		log.Printf("Listening on %s", listen)
		if err := srv.Start(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	// Wait for shutdown signal
	<-stop
	log.Println("Shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("Shutdown error: %v", err)
	}
	log.Println("Gateway stopped")
}
