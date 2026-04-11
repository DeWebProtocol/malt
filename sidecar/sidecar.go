// Package sidecar provides a MALT sidecar deployment mode.
// The sidecar runs alongside a local IPFS daemon, using its API
// for CAS operations (both read and write).
//
// This is a distinct deployment mode from the gateway:
//   - Gateway: read-focused, uses HTTP gateway (read-only)
//   - Sidecar: full read+write, uses local IPFS daemon API
//
// The sidecar co-locates EAT+SCE per graph, providing the same
// authentication guarantees as the gateway but with write support
// through the local IPFS node.
package sidecar

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dewebprotocol/malt/core/api"
	"github.com/dewebprotocol/malt/core/cas/ipfslocal"
	"github.com/dewebprotocol/malt/gateway"
)

// Config holds sidecar configuration.
type Config struct {
	// IPFSAPIURL is the local IPFS daemon API endpoint.
	// Default: "http://localhost:5001"
	IPFSAPIURL string

	// HTTPAddr is the address the sidecar HTTP server listens on.
	// Default: "localhost:8082"
	HTTPAddr string

	// KVStoreType is the KVStore backend to use.
	// Default: "memory"
	KVStoreType string

	// CommitmentType is the commitment scheme to use.
	// Default: "kzg"
	CommitmentType string

	// EATType is the EAT implementation to use.
	// Default: "overwrite"
	EATType string
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		IPFSAPIURL:     "http://localhost:5001",
		HTTPAddr:       "localhost:8082",
		KVStoreType:    "memory",
		CommitmentType: "kzg",
		EATType:        "overwrite",
	}
}

// Sidecar runs a MALT node co-located with a local IPFS daemon.
type Sidecar struct {
	cfg    Config
	node   *api.Node
	server *http.Server
}

// NewSidecar creates a new Sidecar with the given configuration.
func NewSidecar(cfg Config) (*Sidecar, error) {
	if cfg.IPFSAPIURL == "" {
		cfg.IPFSAPIURL = "http://localhost:5001"
	}
	if cfg.HTTPAddr == "" {
		cfg.HTTPAddr = "localhost:8082"
	}
	if cfg.KVStoreType == "" {
		cfg.KVStoreType = "memory"
	}
	if cfg.CommitmentType == "" {
		cfg.CommitmentType = "kzg"
	}
	if cfg.EATType == "" {
		cfg.EATType = "overwrite"
	}

	// Create local IPFS CAS client
	casClient := ipfslocal.NewClient(cfg.IPFSAPIURL)

	// Create MALT node with local IPFS CAS
	node, err := api.NewNode(
		api.WithCAS(casClient),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create MALT node: %w", err)
	}

	// Create gateway on top of the node
	adapter := gateway.NewNodeAdapter(node)
	gw := gateway.NewServer(adapter, "sidecar:0")

	// Create HTTP server
	server := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: gw.Handler(),
	}

	return &Sidecar{
		cfg:    cfg,
		node:   node,
		server: server,
	}, nil
}

// Start begins serving HTTP requests.
// It blocks until the server is shut down.
func (s *Sidecar) Start(ctx context.Context) error {
	// Create a context that cancels on SIGINT/SIGTERM
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sigCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	// Start HTTP server in background
	errCh := make(chan error, 1)
	go func() {
		fmt.Printf("MALT sidecar listening on http://%s\n", s.cfg.HTTPAddr)
		fmt.Printf("IPFS API: %s\n", s.cfg.IPFSAPIURL)
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	// Wait for shutdown or error
	select {
	case <-ctx.Done():
		fmt.Println("\nShutting down sidecar...")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := s.server.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "HTTP server shutdown error: %v\n", err)
		}
		return nil
	case err := <-errCh:
		return fmt.Errorf("HTTP server error: %w", err)
	}
}

// Node returns the underlying MALT node.
func (s *Sidecar) Node() *api.Node {
	return s.node
}

// Close releases all resources.
func (s *Sidecar) Close() error {
	if s.node != nil {
		return s.node.Close()
	}
	return nil
}
