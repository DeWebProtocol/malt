package main

import (
	"context"
	"fmt"
	"os"

	"github.com/dewebprotocol/malt/core/graph"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(updateCmd)
}

var (
	updateGraphID string
	batchGraphID  string
	createGraphID string
)

var updateCmd = &cobra.Command{
	Use:   "update [<root>] <path> <target>",
	Short: "Update an arc in a MALT structure",
	Long: `Update (insert/replace/delete) an arc at the given path.
If target is empty, the arc is deleted.

Examples:
  malt update bafy... data/file.txt bafy...
  malt update bafy... old/link ""
  malt update --graph my-graph data/file.txt bafy...`,
	Args: func(cmd *cobra.Command, args []string) error {
		if updateGraphID != "" {
			return cobra.ExactArgs(2)(cmd, args)
		}
		return cobra.ExactArgs(3)(cmd, args)
	},
	RunE: runUpdate,
}

func runUpdate(cmd *cobra.Command, args []string) error {
	var (
		g         *graph.Graph
		rootCid   cid.Cid
		err       error
		path      string
		targetStr string
	)

	if updateGraphID != "" {
		var meta *graph.GraphMeta
		g, meta = mustManagedGraph(updateGraphID, true)
		rootCid, err = managedGraphHeadRoot(meta)
		if err != nil {
			return err
		}
		path = args[0]
		targetStr = args[1]
	} else {
		g = mustGraph()
		rootCid, err = parseCID(args[0])
		if err != nil {
			return err
		}
		path = args[1]
		targetStr = args[2]
	}
	defer cleanupNode()

	var newTarget cid.Cid
	if targetStr != "" {
		newTarget, err = parseCID(targetStr)
		if err != nil {
			return err
		}
	}

	w := g.Writer()
	ctx := context.Background()

	result, err := w.UpdateArc(ctx, g.BucketId(), rootCid, path, newTarget)
	if err != nil {
		return fmt.Errorf("update failed: %w", err)
	}
	if updateGraphID != "" {
		if err := updateManagedGraphRoot(updateGraphID, g, result.NewRoot); err != nil {
			return fmt.Errorf("update graph metadata: %w", err)
		}
	}

	printJSON(map[string]interface{}{
		"old_root":   result.OldRoot.String(),
		"new_root":   result.NewRoot.String(),
		"path":       result.Path,
		"old_target": result.OldTarget.String(),
		"new_target": result.NewTarget.String(),
		"op":         result.Op.String(),
	})

	fmt.Fprintf(os.Stderr, "arc %q updated (op: %s)\n", path, result.Op.String())
	return nil
}

var updateBatchCmd = &cobra.Command{
	Use:   "batch [<root>] <path=target>...",
	Short: "Batch update multiple arcs",
	Long: `Update multiple arcs in a single operation.
Use path=target pairs. Empty target deletes the arc.

Examples:
  malt batch bafy... a=bafyaaa b=bafybbb c=""
  malt batch --graph my-graph a=bafyaaa b=bafybbb`,
	Args: func(cmd *cobra.Command, args []string) error {
		if batchGraphID != "" {
			return cobra.MinimumNArgs(1)(cmd, args)
		}
		return cobra.MinimumNArgs(2)(cmd, args)
	},
	RunE: runBatchUpdate,
}

func runBatchUpdate(cmd *cobra.Command, args []string) error {
	var (
		g       *graph.Graph
		rootCid cid.Cid
		err     error
		pairs   []string
	)

	if batchGraphID != "" {
		var meta *graph.GraphMeta
		g, meta = mustManagedGraph(batchGraphID, true)
		rootCid, err = managedGraphHeadRoot(meta)
		if err != nil {
			return err
		}
		pairs = args
	} else {
		g = mustGraph()
		rootCid, err = parseCID(args[0])
		if err != nil {
			return err
		}
		pairs = args[1:]
	}
	defer cleanupNode()

	updates := make(map[string]cid.Cid)
	for _, pair := range pairs {
		eq := 0
		for i := 0; i < len(pair); i++ {
			if pair[i] == '=' {
				eq = i
				break
			}
		}
		if eq == 0 {
			return fmt.Errorf("invalid update pair %q, expected path=target", pair)
		}
		path := pair[:eq]
		targetStr := pair[eq+1:]

		if targetStr == "" {
			updates[path] = cid.Undef
		} else {
			t, err := parseCID(targetStr)
			if err != nil {
				return err
			}
			updates[path] = t
		}
	}

	w := g.Writer()
	ctx := context.Background()

	result, err := w.BatchUpdateArcs(ctx, g.BucketId(), rootCid, updates)
	if err != nil {
		return fmt.Errorf("batch update failed: %w", err)
	}
	if batchGraphID != "" {
		if err := updateManagedGraphRoot(batchGraphID, g, result.NewRoot); err != nil {
			return fmt.Errorf("update graph metadata: %w", err)
		}
	}

	fmt.Printf("old_root: %s\n", result.OldRoot)
	fmt.Printf("new_root: %s\n", result.NewRoot)
	fmt.Fprintf(os.Stderr, "updated %d arc(s)\n", len(result.PerArc))
	return nil
}

var createCmd = &cobra.Command{
	Use:   "create <path=target>...",
	Short: "Create a new structure from arcs",
	Long: `Create a new MALT structure from path-to-target pairs.

Examples:
  malt create data=bafyaaa meta=bafybbb
  malt create --graph my-graph data=bafyaaa meta=bafybbb`,
	Args: cobra.MinimumNArgs(1),
	RunE: runCreate,
}

func runCreate(cmd *cobra.Command, args []string) error {
	var g *graph.Graph
	if createGraphID != "" {
		var meta *graph.GraphMeta
		g, meta = mustManagedGraph(createGraphID, true)
		_ = meta
	} else {
		g = mustGraph()
	}
	defer cleanupNode()

	arcs := make(map[string]cid.Cid)
	for _, pair := range args {
		eq := 0
		for i := 0; i < len(pair); i++ {
			if pair[i] == '=' {
				eq = i
				break
			}
		}
		if eq == 0 {
			return fmt.Errorf("invalid pair %q, expected path=target", pair)
		}
		path := pair[:eq]
		targetStr := pair[eq+1:]

		t, err := parseCID(targetStr)
		if err != nil {
			return err
		}
		arcs[path] = t
	}

	w := g.Writer()
	ctx := context.Background()
	snapshot := arcset.NewSetFrom(arcs)

	rootCid, err := w.CreateStructure(ctx, g.BucketId(), snapshot)
	if err != nil {
		return fmt.Errorf("create structure failed: %w", err)
	}
	if createGraphID != "" {
		if err := updateManagedGraphRoot(createGraphID, g, rootCid); err != nil {
			return fmt.Errorf("update graph metadata: %w", err)
		}
	}

	fmt.Println(rootCid.String())
	fmt.Fprintf(os.Stderr, "structure created with %d arc(s)\n", len(arcs))
	return nil
}

func init() {
	updateCmd.AddCommand(updateBatchCmd)
	updateCmd.AddCommand(createCmd)
	updateCmd.Flags().StringVar(&updateGraphID, "graph", "", "Update the managed graph head instead of providing an explicit root")
	updateBatchCmd.Flags().StringVar(&batchGraphID, "graph", "", "Batch update the managed graph head instead of providing an explicit root")
	createCmd.Flags().StringVar(&createGraphID, "graph", "", "Create or replace the managed graph head")
}
