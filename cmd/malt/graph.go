package main

import (
	"fmt"
	"os"

	"github.com/dewebprotocol/malt/core/graph"
	"github.com/spf13/cobra"
)

func init() {
	graphCmd.AddCommand(graphCreateCmd)
	graphCmd.AddCommand(graphDeleteCmd)
	graphCmd.AddCommand(graphListCmd)
	graphCmd.AddCommand(graphFreezeCmd)
	graphCmd.AddCommand(graphGetCmd)
	rootCmd.AddCommand(graphCmd)
}

var graphCmd = &cobra.Command{
	Use:   "graph",
	Short: "Manage MALT graphs (buckets)",
}

var graphCreateCmd = &cobra.Command{
	Use:   "create <id>",
	Short: "Create a new graph",
	Args:  cobra.ExactArgs(1),
	RunE:  runGraphCreate,
}

var graphBackend string
var graphEATType string

func canonicalCLIeATType(t string) string {
	switch t {
	case "simple":
		return "overwrite"
	default:
		return t
	}
}

func init() {
	graphCreateCmd.Flags().StringVar(&graphBackend, "backend", "", "Backend type: kzg (default: node config)")
	graphCreateCmd.Flags().StringVar(&graphEATType, "eat", "", "Required EAT type metadata (must match node config)")
}

func runGraphCreate(cmd *cobra.Command, args []string) error {
	node := mustNode()
	defer node.Close()

	ctx := cmd.Context()

	if graphEATType != "" && canonicalCLIeATType(graphEATType) != canonicalCLIeATType(node.Config().EATType) {
		return fmt.Errorf("graph eat_type %q must match node config %q", graphEATType, node.Config().EATType)
	}

	g, err := node.CreateManagedGraph(ctx, args[0], graphBackend)
	if err != nil {
		if err == graph.ErrAlreadyExists {
			return fmt.Errorf("graph %q already exists", args[0])
		}
		return fmt.Errorf("failed to create graph: %w", err)
	}

	printJSON(map[string]interface{}{
		"id":         g.ID,
		"root":       g.Root.String(),
		"created_at": g.CreatedAt,
		"backend":    g.Backend,
		"eat_type":   g.EATType,
		"arc_count":  g.ArcCount,
		"state":      g.State,
	})
	return nil
}

var graphGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Get graph metadata",
	Args:  cobra.ExactArgs(1),
	RunE:  runGraphGet,
}

func runGraphGet(cmd *cobra.Command, args []string) error {
	node := mustNode()
	defer node.Close()

	gm := node.GraphManager()
	ctx := cmd.Context()

	g, err := gm.GetGraph(ctx, args[0])
	if err != nil {
		if err == graph.ErrNotFound || err == graph.ErrDeleted {
			return fmt.Errorf("graph %q not found", args[0])
		}
		return fmt.Errorf("failed to get graph: %w", err)
	}

	printJSON(map[string]interface{}{
		"id":         g.ID,
		"root":       g.Root.String(),
		"created_at": g.CreatedAt,
		"backend":    g.Backend,
		"eat_type":   g.EATType,
		"arc_count":  g.ArcCount,
		"state":      g.State,
	})
	return nil
}

var graphDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a graph",
	Args:  cobra.ExactArgs(1),
	RunE:  runGraphDelete,
}

func runGraphDelete(cmd *cobra.Command, args []string) error {
	node := mustNode()
	defer node.Close()

	gm := node.GraphManager()
	ctx := cmd.Context()

	if err := gm.DeleteGraph(ctx, args[0]); err != nil {
		return fmt.Errorf("failed to delete graph: %w", err)
	}

	fmt.Fprintf(os.Stdout, "graph %q deleted\n", args[0])
	return nil
}

var graphListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all graphs",
	RunE:  runGraphList,
}

func runGraphList(cmd *cobra.Command, args []string) error {
	node := mustNode()
	defer node.Close()

	gm := node.GraphManager()
	ctx := cmd.Context()

	graphs, err := gm.ListGraphs(ctx)
	if err != nil {
		return fmt.Errorf("failed to list graphs: %w", err)
	}

	if len(graphs) == 0 {
		fmt.Println("no graphs found")
		return nil
	}

	for _, g := range graphs {
		fmt.Printf("%s  root=%s  state=%s  arcs=%d  backend=%s\n",
			g.ID, g.Root.String(), g.State, g.ArcCount, g.Backend)
	}
	return nil
}

var graphFreezeCmd = &cobra.Command{
	Use:   "freeze <id>",
	Short: "Freeze a graph (make immutable)",
	Args:  cobra.ExactArgs(1),
	RunE:  runGraphFreeze,
}

func runGraphFreeze(cmd *cobra.Command, args []string) error {
	node := mustNode()
	defer node.Close()

	gm := node.GraphManager()
	ctx := cmd.Context()

	if err := gm.FreezeGraph(ctx, args[0]); err != nil {
		return fmt.Errorf("failed to freeze graph: %w", err)
	}

	fmt.Fprintf(os.Stdout, "graph %q frozen\n", args[0])
	return nil
}
