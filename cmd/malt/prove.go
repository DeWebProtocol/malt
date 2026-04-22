package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/dewebprotocol/malt/httpapi"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(proveCmd)
	proveCmd.Flags().BoolP("json", "j", false, "Output as JSON")
	proveCmd.Flags().StringVar(&proveGraphID, "graph", "", "Generate a proof from the managed graph head instead of an explicit root")
}

var proveGraphID string

var proveCmd = &cobra.Command{
	Use:   "prove [<root>] <path>",
	Short: "Generate a proof for a path resolution",
	Args: func(cmd *cobra.Command, args []string) error {
		if proveGraphID != "" {
			return cobra.ExactArgs(1)(cmd, args)
		}
		return cobra.ExactArgs(2)(cmd, args)
	},
	RunE: runProve,
}

func runProve(cmd *cobra.Command, args []string) error {
	client := mustDaemonClient()

	var (
		root   string
		path   string
		result *httpapi.ResolveResponse
		err    error
	)

	if proveGraphID != "" {
		path = args[0]
		result, err = client.ProveGraph(cmd.Context(), proveGraphID, path)
		root = "<managed-graph-head>"
	} else {
		root = args[0]
		path = args[1]
		result, err = client.ProveRoot(cmd.Context(), root, path)
	}
	if err != nil {
		return daemonCommandError(err)
	}

	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		payload := map[string]any{
			"root":   root,
			"target": result.Target,
			"steps":  result.Transcript,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(payload)
	}

	fmt.Fprintf(os.Stdout, "root:   %s\n", root)
	fmt.Fprintf(os.Stdout, "target: %s\n", result.Target)
	fmt.Fprintf(os.Stdout, "path:   %s\n\n", path)
	for i, step := range result.Transcript {
		fmt.Fprintf(os.Stdout, "[%d] %s -> %s\n", i, step.Path, step.Target)
		fmt.Fprintf(os.Stdout, "    evidence: %s (base64)\n", step.Evidence)
		fmt.Fprintf(os.Stdout, "    kind:     %s\n", step.Kind)
	}
	return nil
}
