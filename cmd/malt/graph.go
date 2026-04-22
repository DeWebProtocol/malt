package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func init() {
	graphCmd.AddCommand(graphCreateCmd)
	graphCmd.AddCommand(graphDeleteCmd)
	graphCmd.AddCommand(graphListCmd)
	graphCmd.AddCommand(graphFreezeCmd)
	graphCmd.AddCommand(graphGetCmd)
	rootCmd.AddCommand(graphCmd)
	graphCreateCmd.Flags().StringVar(&graphBackend, "backend", "", "backend type (default: daemon config)")
}

var graphCmd = &cobra.Command{
	Use:   "graph",
	Short: "Manage MALT graphs via the local daemon",
}

var graphCreateCmd = &cobra.Command{
	Use:   "create <id>",
	Short: "Create a new graph",
	Args:  cobra.ExactArgs(1),
	RunE:  runGraphCreate,
}

var graphBackend string

func runGraphCreate(cmd *cobra.Command, args []string) error {
	client := mustDaemonClient()
	meta, err := client.CreateGraph(cmd.Context(), args[0], graphBackend)
	if err != nil {
		return daemonCommandError(err)
	}
	printJSON(meta)
	return nil
}

var graphGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Get graph metadata",
	Args:  cobra.ExactArgs(1),
	RunE:  runGraphGet,
}

func runGraphGet(cmd *cobra.Command, args []string) error {
	client := mustDaemonClient()
	meta, err := client.GetGraph(cmd.Context(), args[0])
	if err != nil {
		return daemonCommandError(err)
	}
	printJSON(meta)
	return nil
}

var graphDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a graph",
	Args:  cobra.ExactArgs(1),
	RunE:  runGraphDelete,
}

func runGraphDelete(cmd *cobra.Command, args []string) error {
	client := mustDaemonClient()
	if err := client.DeleteGraph(cmd.Context(), args[0]); err != nil {
		return daemonCommandError(err)
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
	client := mustDaemonClient()
	graphs, err := client.ListGraphs(cmd.Context())
	if err != nil {
		return daemonCommandError(err)
	}

	if len(graphs) == 0 {
		fmt.Println("no graphs found")
		return nil
	}
	for _, g := range graphs {
		fmt.Printf("%s  root=%s  state=%s  arcs=%d  backend=%s\n", g.ID, g.Root, g.State, g.ArcCount, g.Backend)
	}
	return nil
}

var graphFreezeCmd = &cobra.Command{
	Use:   "freeze <id>",
	Short: "Freeze a graph",
	Args:  cobra.ExactArgs(1),
	RunE:  runGraphFreeze,
}

func runGraphFreeze(cmd *cobra.Command, args []string) error {
	client := mustDaemonClient()
	if err := client.FreezeGraph(cmd.Context(), args[0]); err != nil {
		return daemonCommandError(err)
	}
	fmt.Fprintf(os.Stdout, "graph %q frozen\n", args[0])
	return nil
}
