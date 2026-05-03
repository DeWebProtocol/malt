package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(proveCmd)
	proveCmd.Flags().BoolP("json", "j", false, "Output as JSON")
}

var proveCmd = &cobra.Command{
	Use:   "prove <root> <path>",
	Short: "Generate a proof for a path resolution",
	Args:  cobra.ExactArgs(2),
	RunE:  runProve,
}

func runProve(cmd *cobra.Command, args []string) error {
	client := mustDaemonClient()

	result, err := client.ProveRoot(cmd.Context(), args[0], args[1])
	if err != nil {
		return daemonCommandError(err)
	}

	root := args[0]
	path := args[1]

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
