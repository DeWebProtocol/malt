package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/dewebprotocol/malt/core/graph"
	"github.com/dewebprotocol/malt/core/kvstore/badger"
	"github.com/dewebprotocol/malt/core/replication"
	"github.com/spf13/cobra"
)

func init() {
	replicationCmd.AddCommand(replicationExportCmd)
	replicationCmd.AddCommand(replicationImportCmd)
	replicationCmd.AddCommand(replicationSyncCmd)
	replicationCmd.AddCommand(replicationDiffCmd)
	replicationCmd.AddCommand(replicationExportAllCmd)
	rootCmd.AddCommand(replicationCmd)
}

var replicationCmd = &cobra.Command{
	Use:   "replication",
	Short: "Export, import, and sync graph snapshots between nodes",
}

// --- Export ---

var exportOutput string

func init() {
	replicationExportCmd.Flags().StringVarP(&exportOutput, "output", "o", "", "Output file path (default: <graph-id>.snapshot.json)")
}

var replicationExportCmd = &cobra.Command{
	Use:   "export <graph-id>",
	Short: "Export a graph snapshot to a file",
	Args:  cobra.ExactArgs(1),
	RunE:  runReplicationExport,
}

func runReplicationExport(cmd *cobra.Command, args []string) error {
	node := mustNode()
	defer node.Close()

	gm := node.GraphManager()
	ctx := cmd.Context()

	g, err := gm.GetGraph(ctx, args[0])
	if err != nil {
		return fmt.Errorf("get graph %q: %w", args[0], err)
	}

	kv := node.KVStore()
	exporter := replication.NewExporter(kv, ctx)
	snap, err := exporter.Export(g)
	if err != nil {
		return fmt.Errorf("export graph: %w", err)
	}

	data, err := snap.Marshal()
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}

	outFile := exportOutput
	if outFile == "" {
		outFile = g.ID + ".snapshot.json"
	}

	if err := os.WriteFile(outFile, data, 0644); err != nil {
		return fmt.Errorf("write snapshot: %w", err)
	}

	fmt.Fprintf(os.Stdout, "Exported graph %q to %s\n", g.ID, outFile)
	fmt.Fprintf(os.Stdout, "  Entries: %d EAT, %d lineage, %d COW\n",
		len(snap.EATEntries), len(snap.LineageEntries), len(snap.COWEntries))
	fmt.Fprintf(os.Stdout, "  Checksum: %s\n", snap.Checksum)
	return nil
}

// --- Import ---

var replicationImportCmd = &cobra.Command{
	Use:   "import <snapshot-file>",
	Short: "Import a graph snapshot from a file",
	Args:  cobra.ExactArgs(1),
	RunE:  runReplicationImport,
}

func runReplicationImport(cmd *cobra.Command, args []string) error {
	node := mustNode()
	defer node.Close()

	data, err := os.ReadFile(args[0])
	if err != nil {
		return fmt.Errorf("read snapshot file: %w", err)
	}

	snap, err := replication.Unmarshal(data)
	if err != nil {
		return fmt.Errorf("unmarshal snapshot: %w", err)
	}

	kv := node.KVStore()
	importer := replication.NewImporter(kv, cmd.Context())
	count, err := importer.Import(snap)
	if err != nil {
		return fmt.Errorf("import snapshot: %w", err)
	}

	fmt.Fprintf(os.Stdout, "Imported graph %q: %d entries\n", snap.GraphID, count)
	return nil
}

// --- Sync ---

var replicationSyncCmd = &cobra.Command{
	Use:   "sync <graph-id> <target-kv-path>",
	Short: "Sync a graph to another KVStore",
	Long: `Sync exports a graph from the current node's KVStore and imports
it into a target KVStore at the specified path. The target KVStore
is a new BadgerDB instance created at the given path.`,
	Args: cobra.ExactArgs(2),
	RunE:  runReplicationSync,
}

func runReplicationSync(cmd *cobra.Command, args []string) error {
	graphID := args[0]
	targetPath := args[1]

	// Source node
	srcNode := mustNode()
	defer srcNode.Close()

	srcGM := srcNode.GraphManager()
	ctx := cmd.Context()

	srcGraph, err := srcGM.GetGraph(ctx, graphID)
	if err != nil {
		return fmt.Errorf("get source graph: %w", err)
	}

	// Create target KVStore
	targetKV, err := badger.New(badger.WithPath(targetPath))
	if err != nil {
		return fmt.Errorf("create target KVStore: %w", err)
	}
	defer targetKV.Close()

	// Ensure target graph exists
	tgtStore := graph.NewStore(targetKV)
	tgtMgr := graph.NewManager(tgtStore)

	_, err = tgtMgr.GetGraph(ctx, graphID)
	if err != nil {
		// Create it with same config
		_, err := tgtMgr.CreateGraph(ctx, graphID, srcGraph.Backend, srcGraph.EATType)
		if err != nil && err != graph.ErrAlreadyExists {
			return fmt.Errorf("create target graph: %w", err)
		}
	}

	// Sync using source KV and target KV directly
	syncer := replication.NewSyncer(srcNode.KVStore(), targetKV, ctx)
	result, err := syncer.Sync()
	if err != nil {
		return fmt.Errorf("sync: %w", err)
	}

	fmt.Fprintf(os.Stdout, "Synced graph %q to %s: %d entries imported, %d skipped\n",
		graphID, targetPath, result.Imported, result.Skipped)
	return nil
}

// --- Diff ---

var diffOutputFormat string

func init() {
	replicationDiffCmd.Flags().StringVarP(&diffOutputFormat, "format", "f", "text", "Output format: text/json")
}

var replicationDiffCmd = &cobra.Command{
	Use:   "diff <graph-id> <other-kv-path>",
	Short: "Compare a graph with another KVStore",
	Args:  cobra.ExactArgs(2),
	RunE:  runReplicationDiff,
}

func runReplicationDiff(cmd *cobra.Command, args []string) error {
	graphID := args[0]
	otherPath := args[1]

	// Source node
	srcNode := mustNode()
	defer srcNode.Close()

	ctx := cmd.Context()

	// Load other KVStore
	otherKV, err := badger.New(badger.WithPath(otherPath))
	if err != nil {
		return fmt.Errorf("open other KVStore: %w", err)
	}
	defer otherKV.Close()

	syncer := replication.NewSyncer(srcNode.KVStore(), otherKV, ctx)
	diff, err := syncer.Diff()
	if err != nil {
		return fmt.Errorf("diff: %w", err)
	}

	if diffOutputFormat == "json" {
		printJSON(diff)
		return nil
	}

	fmt.Fprintf(os.Stdout, "Diff for graph %q:\n", graphID)
	fmt.Fprintf(os.Stdout, "  Missing in other: %d\n", len(diff.MissingInTarget))
	for _, k := range diff.MissingInTarget {
		fmt.Fprintf(os.Stdout, "    - %s\n", k)
	}
	fmt.Fprintf(os.Stdout, "  Extra in other: %d\n", len(diff.ExtraInTarget))
	for _, k := range diff.ExtraInTarget {
		fmt.Fprintf(os.Stdout, "    + %s\n", k)
	}
	fmt.Fprintf(os.Stdout, "  Mismatched: %d\n", len(diff.Mismatched))
	for _, k := range diff.Mismatched {
		fmt.Fprintf(os.Stdout, "    ~ %s\n", k)
	}

	return nil
}

// --- Export All ---

var exportAllOutput string

func init() {
	replicationExportAllCmd.Flags().StringVarP(&exportAllOutput, "output", "o", "", "Output directory (default: current directory)")
}

var replicationExportAllCmd = &cobra.Command{
	Use:   "export-all",
	Short: "Export all active graphs to snapshot files",
	RunE:  runReplicationExportAll,
}

func runReplicationExportAll(cmd *cobra.Command, args []string) error {
	node := mustNode()
	defer node.Close()

	kv := node.KVStore()
	exporter := replication.NewExporter(kv, cmd.Context())

	store := graph.NewStore(kv)
	graphs, err := store.List(cmd.Context())
	if err != nil {
		return fmt.Errorf("list graphs: %w", err)
	}

	outDir := exportAllOutput
	if outDir == "" {
		outDir = "."
	}

	count := 0
	for _, g := range graphs {
		if g.IsDeleted() {
			continue
		}
		snap, err := exporter.Export(g)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to export %s: %v\n", g.ID, err)
			continue
		}

		outFile := filepath.Join(outDir, g.ID+".snapshot.json")
		data, err := snap.Marshal()
		if err != nil {
			return fmt.Errorf("marshal %s: %w", g.ID, err)
		}
		if err := os.WriteFile(outFile, data, 0644); err != nil {
			return fmt.Errorf("write %s: %w", outFile, err)
		}
		fmt.Fprintf(os.Stdout, "Exported %s -> %s\n", g.ID, outFile)
		count++
	}

	fmt.Fprintf(os.Stdout, "Exported %d graphs\n", count)
	return nil
}
