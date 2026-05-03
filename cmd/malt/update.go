package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(updateCmd)
	updateCmd.AddCommand(updateBatchCmd)
	updateCmd.AddCommand(createCmd)
}

var updateCmd = &cobra.Command{
	Use:   "update <root> <path> <target>",
	Short: "Update an arc in a MALT structure",
	Args:  cobra.ExactArgs(3),
	RunE:  runUpdate,
}

func runUpdate(cmd *cobra.Command, args []string) error {
	client := mustDaemonClient()

	out, err := client.UpdateRoot(cmd.Context(), args[0], args[1], args[2])
	if err != nil {
		return daemonCommandError(err)
	}
	resp := &struct {
		OldRoot   string `json:"old_root"`
		NewRoot   string `json:"new_root"`
		Path      string `json:"path"`
		OldTarget string `json:"old_target"`
		NewTarget string `json:"new_target"`
		Op        string `json:"op"`
	}{out.OldRoot, out.NewRoot, out.Path, out.OldTarget, out.NewTarget, out.Op}

	printJSON(resp)
	fmt.Fprintf(os.Stderr, "arc %q updated (op: %s)\n", resp.Path, resp.Op)
	return nil
}

var updateBatchCmd = &cobra.Command{
	Use:   "batch <root> <path=target>...",
	Short: "Batch update multiple arcs",
	Args:  cobra.MinimumNArgs(2),
	RunE:  runBatchUpdate,
}

func runBatchUpdate(cmd *cobra.Command, args []string) error {
	client := mustDaemonClient()

	updates, err := parseUpdatePairs(args, true)
	if err != nil {
		return err
	}

	root := args[0]
	result, err := client.BatchUpdateRoot(cmd.Context(), root, updates)
	if err != nil {
		return daemonCommandError(err)
	}
	fmt.Printf("old_root: %s\n", result.OldRoot)
	fmt.Printf("new_root: %s\n", result.NewRoot)
	fmt.Fprintf(os.Stderr, "updated %d arc(s)\n", len(result.PerArc))
	return nil
}

var createCmd = &cobra.Command{
	Use:   "create <path=target>...",
	Short: "Create a new structure from arcs",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runCreate,
}

func runCreate(cmd *cobra.Command, args []string) error {
	client := mustDaemonClient()
	arcs, err := parseCreatePairs(args)
	if err != nil {
		return err
	}

	resp, err := client.CreateRootStructure(cmd.Context(), arcs)
	if err != nil {
		return daemonCommandError(err)
	}
	fmt.Println(resp.Root)
	fmt.Fprintf(os.Stderr, "structure created with %d arc(s)\n", len(arcs))
	return nil
}

func parseUpdatePairs(args []string, hasRoot bool) (map[string]string, error) {
	pairs := args
	if hasRoot {
		pairs = args[1:]
	}

	updates := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		parts := splitPair(pair)
		if parts == nil {
			return nil, fmt.Errorf("invalid update pair %q, expected path=target", pair)
		}
		updates[parts[0]] = parts[1]
	}
	return updates, nil
}

func parseCreatePairs(args []string) (map[string]string, error) {
	arcs := make(map[string]string, len(args))
	for _, pair := range args {
		parts := splitPair(pair)
		if parts == nil || parts[1] == "" {
			return nil, fmt.Errorf("invalid pair %q, expected path=target", pair)
		}
		arcs[parts[0]] = parts[1]
	}
	return arcs, nil
}

func splitPair(pair string) []string {
	for i := 0; i < len(pair); i++ {
		if pair[i] == '=' {
			return []string{pair[:i], pair[i+1:]}
		}
	}
	return nil
}
