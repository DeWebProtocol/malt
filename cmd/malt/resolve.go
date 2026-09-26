package main

import (
	"errors"

	"github.com/spf13/cobra"
)

func init() {
	resolveCmd.Flags().String("layout", "", "UnixFS layout override (defaults to the selected Bucket layout, or hybrid-v1 without a Bucket)")
	rootCmd.AddCommand(resolveCmd)
}

var resolveCmd = &cobra.Command{
	Use:   "resolve <root> [path]",
	Short: "Resolve a path through a MALT structure",
	Args:  cobra.RangeArgs(1, 2),
	RunE:  runResolve,
}

func runResolve(cmd *cobra.Command, args []string) (resultErr error) {
	content, err := configuredContent(cmd, args[0])
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, content.Close()) }()
	app := content.UnixFS
	resolution, err := app.Resolve(cmd.Context(), args[0], optionalPath(args))
	if err != nil {
		return daemonCommandError(err)
	}
	printJSON(resolution.Authentication)
	return nil
}
