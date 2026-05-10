package main

import (
	"github.com/dewebprotocol/malt/httpapi"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(resolveCmd)
}

var resolveCmd = &cobra.Command{
	Use:   "resolve <root> [path]",
	Short: "Resolve a path through a MALT structure",
	Args:  cobra.RangeArgs(1, 2),
	RunE:  runResolve,
}

func runResolve(cmd *cobra.Command, args []string) error {
	client := mustDaemonClient()

	path := ""
	if len(args) > 1 {
		path = args[1]
	}
	result, err := client.ResolveRoot(cmd.Context(), args[0], path)
	if err != nil {
		return daemonCommandError(err)
	}

	return printResolveResult(cmd, result)
}

func printResolveResult(cmd *cobra.Command, result *httpapi.ResolveResponse) error {
	printJSON(result)
	return nil
}
