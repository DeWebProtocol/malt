package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/dewebprotocol/malt/core/api"
	"github.com/dewebprotocol/malt/core/graph"
	"github.com/dewebprotocol/malt/core/types/arcset"
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

func mustManagedGraph(graphID string, requireActive bool) (*graph.Graph, *graph.GraphMeta) {
	node := mustNode()
	ctx := context.Background()

	var (
		meta *graph.GraphMeta
		err  error
	)
	if requireActive {
		meta, err = node.GraphManager().RequireActive(ctx, graphID)
	} else {
		meta, err = node.GraphManager().GetGraph(ctx, graphID)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading graph %q: %v\n", graphID, err)
		os.Exit(1)
	}

	g, err := node.OpenGraph(ctx, graphID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening graph %q: %v\n", graphID, err)
		os.Exit(1)
	}

	return g, meta
}

func managedGraphHeadRoot(meta *graph.GraphMeta) (cid.Cid, error) {
	if meta == nil {
		return cid.Undef, fmt.Errorf("graph metadata is nil")
	}
	if !meta.Root.Defined() {
		return cid.Undef, fmt.Errorf("graph %q has no head root", meta.ID)
	}
	return meta.Root, nil
}

func countSnapshotArcs(snapshot arcset.Snapshot) (int, error) {
	count := 0
	iter := snapshot.Iterate()
	for {
		_, _, ok := iter.Next()
		if !ok {
			break
		}
		count++
	}
	return count, iter.Err()
}

func updateManagedGraphRoot(graphID string, g *graph.Graph, newRoot cid.Cid) error {
	node := mustNode()
	ctx := context.Background()

	snapshot, err := g.Snapshot(ctx, newRoot)
	if err != nil {
		return fmt.Errorf("snapshot new root: %w", err)
	}

	arcCount, err := countSnapshotArcs(snapshot)
	if err != nil {
		return fmt.Errorf("count arcs: %w", err)
	}

	_, err = node.GraphManager().UpdateGraph(ctx, graphID, newRoot, arcCount)
	return err
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
