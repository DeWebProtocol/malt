package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/dewebprotocol/malt/core/api"
	"github.com/dewebprotocol/malt/core/graph"
	cid "github.com/ipfs/go-cid"
	"github.com/spf13/viper"
)

// defaultNode and defaultGraph are lazily initialized and reused across commands.
var (
	defaultNode  *api.Node
	defaultGraph *graph.Graph
)

// makeNode creates and configures a MALT node from CLI flags.
func makeNode() (*api.Node, error) {
	var opts []api.Option

	if cfgFile != "" {
		opts = append(opts, api.WithConfigFile(cfgFile))
	}

	node, err := api.NewNode(opts...)
	if err != nil {
		return nil, fmt.Errorf("creating node: %w", err)
	}
	return node, nil
}

// mustNode creates a node or exits with an error.
// It reuses a cached defaultNode if available.
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

// mustGraph returns the default graph, creating it via mustNode if needed.
func mustGraph() *graph.Graph {
	if defaultGraph == nil {
		node := mustNode()
		var err error
		defaultGraph, err = node.NewGraph("default")
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
	}
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

// loadConfig reads the config file if specified.
func loadConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	}
	if err := viper.ReadInConfig(); err == nil {
		fmt.Fprintln(os.Stderr, "Using config file:", viper.ConfigFileUsed())
	}
}
