package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/dewebprotocol/malt/httpapi"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(resolveCmd)
	resolveCmd.Flags().BoolP("verbose", "v", false, "Show resolution transcript")
	resolveCmd.Flags().StringVarP(&resolveBucketID, "bucket", "b", "", "Resolve from the managed bucket head instead of an explicit root")
}

var resolveBucketID string

var resolveCmd = &cobra.Command{
	Use:   "resolve [<root>] [path]",
	Short: "Resolve a path through a MALT structure",
	Args: func(cmd *cobra.Command, args []string) error {
		if resolveBucketID != "" {
			return cobra.RangeArgs(0, 1)(cmd, args)
		}
		return cobra.RangeArgs(1, 2)(cmd, args)
	},
	RunE: runResolve,
}

func runResolve(cmd *cobra.Command, args []string) error {
	client := mustDaemonClient()

	var (
		result *httpapi.ResolveResponse
		err    error
	)

	if resolveBucketID != "" {
		path := ""
		if len(args) > 0 {
			path = args[0]
		}
		result, err = client.ResolveBucket(cmd.Context(), resolveBucketID, path)
	} else {
		path := ""
		if len(args) > 1 {
			path = args[1]
		}
		result, err = client.ResolveRoot(cmd.Context(), args[0], path)
	}
	if err != nil {
		return daemonCommandError(err)
	}

	return printResolveResult(cmd, result)
}

func printResolveResult(cmd *cobra.Command, result *httpapi.ResolveResponse) error {
	verbose, _ := cmd.Flags().GetBool("verbose")
	if verbose {
		printJSON(map[string]interface{}{
			"target": result.Target,
			"steps":  len(result.Transcript),
		})
		fmt.Fprintf(os.Stderr, "\nResolution transcript:\n")
		for i, step := range result.Transcript {
			fmt.Fprintf(os.Stderr, "  [%d] %s -> %s (evidence: %s)\n", i, step.Path, step.Target, step.Kind)
		}
	} else {
		fmt.Println(result.Target)
		fmt.Fprintf(os.Stderr, "resolved via %d step(s)\n", len(result.Transcript))
	}

	if len(result.Transcript) > 0 {
		matched := make([]string, len(result.Transcript))
		for i, step := range result.Transcript {
			matched[i] = step.Path
		}
		fmt.Fprintf(os.Stderr, "path matched: %s\n", strings.Join(matched, " -> "))
	}
	return nil
}
