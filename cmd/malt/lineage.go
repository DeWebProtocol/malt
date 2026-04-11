package main

import (
	"fmt"
	"os"

	"github.com/dewebprotocol/malt/core/lineage"
	"github.com/spf13/cobra"
)

func init() {
	lineageCmd.AddCommand(lineageGetCmd)
	lineageCmd.AddCommand(lineageAncestorsCmd)
	lineageCmd.AddCommand(lineageDescendantsCmd)
	lineageCmd.AddCommand(lineageListCmd)
	rootCmd.AddCommand(lineageCmd)
}

var lineageCmd = &cobra.Command{
	Use:   "lineage",
	Short: "Query version lineage information",
	Long: `Query version lineage information for MALT structure roots.

The lineage module tracks parent-child relationships between structure
versions, enabling ancestry traversal, depth tracking, and copy-on-write
optimizations.

Examples:
  malt lineage get <root>          # Get lineage record for a version
  malt lineage ancestors <root>    # Show ancestor chain
  malt lineage descendants <root>  # Show direct children
  malt lineage list                # List all lineage records`,
}

var (
	lineageMaxDepth int
)

func init() {
	lineageAncestorsCmd.Flags().IntVar(&lineageMaxDepth, "max-depth", 0, "Maximum depth to traverse (0 = unlimited)")
}

func mustLineageManager() *lineage.Manager {
	node := mustNode()
	return node.LineageManager()
}

// ===== lineage get =====

var lineageGetCmd = &cobra.Command{
	Use:   "get <root>",
	Short: "Get lineage record for a version",
	Args:  cobra.ExactArgs(1),
	RunE:  runLineageGet,
}

func runLineageGet(cmd *cobra.Command, args []string) error {
	root, err := parseCID(args[0])
	if err != nil {
		return err
	}

	mgr := mustLineageManager()
	rec, err := mgr.Get(cmd.Context(), root)
	if err != nil {
		return fmt.Errorf("get lineage: %w", err)
	}

	printJSON(map[string]interface{}{
		"root":      rec.Root.String(),
		"parent":    rec.Parent.String(),
		"depth":     rec.Depth,
		"arc_count": rec.ArcCount,
		"timestamp": rec.Timestamp,
	})
	return nil
}

// ===== lineage ancestors =====

var lineageAncestorsCmd = &cobra.Command{
	Use:   "ancestors <root>",
	Short: "Show ancestor chain from a version",
	Args:  cobra.ExactArgs(1),
	RunE:  runLineageAncestors,
}

func runLineageAncestors(cmd *cobra.Command, args []string) error {
	root, err := parseCID(args[0])
	if err != nil {
		return err
	}

	mgr := mustLineageManager()
	ancestors, err := mgr.Ancestors(cmd.Context(), root, lineageMaxDepth)
	if err != nil {
		return fmt.Errorf("get ancestors: %w", err)
	}

	if len(ancestors) == 0 {
		fmt.Println("No ancestors (root version)")
		return nil
	}

	fmt.Printf("Ancestors (%d):\n", len(ancestors))
	for i, a := range ancestors {
		fmt.Printf("  %d: %s\n", i+1, a)
	}
	return nil
}

// ===== lineage descendants =====

var lineageDescendantsCmd = &cobra.Command{
	Use:   "descendants <root>",
	Short: "Show direct children of a version",
	Args:  cobra.ExactArgs(1),
	RunE:  runLineageDescendants,
}

func runLineageDescendants(cmd *cobra.Command, args []string) error {
	root, err := parseCID(args[0])
	if err != nil {
		return err
	}

	mgr := mustLineageManager()
	children, err := mgr.Descendants(cmd.Context(), root)
	if err != nil {
		return fmt.Errorf("get descendants: %w", err)
	}

	if len(children) == 0 {
		fmt.Println("No descendants (leaf version)")
		return nil
	}

	fmt.Printf("Descendants (%d):\n", len(children))
	for i, c := range children {
		fmt.Printf("  %d: %s\n", i+1, c)
	}
	return nil
}

// ===== lineage list =====

var lineageListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all lineage records",
	RunE:  runLineageList,
}

func runLineageList(cmd *cobra.Command, args []string) error {
	mgr := mustLineageManager()
	records, err := mgr.List(cmd.Context())
	if err != nil {
		return fmt.Errorf("list lineage: %w", err)
	}

	if len(records) == 0 {
		fmt.Println("No lineage records")
		return nil
	}

	fmt.Printf("Lineage records (%d):\n", len(records))
	for _, rec := range records {
		parent := "root"
		if rec.Parent.Defined() {
			parent = rec.Parent.String()[:16] + "..."
		}
		fmt.Printf("  %s -> %s (depth=%d, arcs=%d)\n",
			rec.Root.String()[:16]+"...",
			parent,
			rec.Depth,
			rec.ArcCount,
		)
	}
	return nil
}

// ===== lineage count =====

var lineageCountCmd = &cobra.Command{
	Use:   "count",
	Short: "Count lineage records",
	RunE:  runLineageCount,
}

func init() {
	lineageCmd.AddCommand(lineageCountCmd)
}

func runLineageCount(cmd *cobra.Command, args []string) error {
	mgr := mustLineageManager()
	count := mgr.Count(cmd.Context())
	fmt.Fprintf(os.Stdout, "%d\n", count)
	return nil
}
