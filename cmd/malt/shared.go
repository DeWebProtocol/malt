package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	daemonclient "github.com/dewebprotocol/malt/client"
	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/core/api"
	"github.com/dewebprotocol/malt/core/graph"
	cid "github.com/ipfs/go-cid"
)

// defaultNode and defaultGraph are lazily initialized and reused by commands
// that need direct in-process graph access.
var (
	defaultNode   *api.Node
	defaultGraph  *graph.Graph
	defaultClient *daemonclient.Client
)

func loadRuntimeConfig() (*config.Config, error) {
	if cfgFile != "" {
		return config.LoadFromFile(cfgFile)
	}
	return config.Load()
}

// makeNode creates and configures a MALT node from config.
func makeNode() (*api.Node, error) {
	cfg, err := loadRuntimeConfig()
	if err != nil {
		return nil, err
	}
	return api.NewNode(api.WithConfig(cfg))
}

// mustNode creates a node or exits with an error.
func mustNode() *api.Node {
	if defaultNode == nil {
		var err error
		defaultNode, err = makeNode()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}
	return defaultNode
}

// mustGraph returns the default graph for direct in-process commands.
func mustGraph() *graph.Graph {
	if defaultGraph == nil {
		node := mustNode()
		var err error
		defaultGraph, err = node.OpenGraph(context.Background(), "default")
		if err == graph.ErrNotFound {
			defaultGraph, err = node.NewGraph("default")
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating graph: %v\n", err)
			os.Exit(1)
		}
	}
	return defaultGraph
}

// cleanupNode closes the default node if it was created.
func cleanupNode() {
	if defaultNode != nil {
		_ = defaultNode.Close()
		defaultNode = nil
		defaultGraph = nil
	}
	defaultClient = nil
}

func mustDaemonClient() *daemonclient.Client {
	if defaultClient == nil {
		cfg, err := loadRuntimeConfig()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
			os.Exit(1)
		}
		defaultClient = daemonclient.New(cfg)
	}
	return defaultClient
}

// parseCID parses a CID string or returns an error.
func parseCID(s string) (cid.Cid, error) {
	c, err := cid.Decode(s)
	if err != nil {
		return cid.Undef, fmt.Errorf("invalid CID %q: %w", s, err)
	}
	return c, nil
}

// printJSON marshals and prints a value as JSON.
func printJSON(v interface{}) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func daemonCommandError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *daemonclient.Error
	if errors.As(err, &apiErr) {
		return fmt.Errorf("daemon request failed: %s", apiErr.Message)
	}
	return fmt.Errorf("daemon unavailable or config invalid: %w", err)
}
