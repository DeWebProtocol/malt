package main

import (
	"errors"
	"fmt"

	localruntime "github.com/dewebprotocol/malt-client/internal/runtime"
	unixfs "github.com/dewebprotocol/malt-client/unixfs"
	"github.com/spf13/cobra"
)

var statCmd = &cobra.Command{
	Use:   "stat <trusted-root|alias> [path]",
	Short: "Inspect a UnixFS path after local proof and payload verification",
	Args:  cobra.RangeArgs(1, 2),
	RunE:  runStat,
}

var catCmd = &cobra.Command{
	Use:   "cat <trusted-root|alias> [path]",
	Short: "Write locally verified UnixFS file bytes to stdout",
	Args:  cobra.RangeArgs(1, 2),
	RunE:  runCat,
}

var rmCmd = &cobra.Command{
	Use:   "rm <trusted-root|alias> <path>",
	Short: "Materialize an unaccepted root with one UnixFS path removed",
	Args:  cobra.ExactArgs(2),
	RunE:  runRemove,
}

func init() {
	catCmd.Flags().Uint64("offset", 0, "range start in bytes (requires --length)")
	catCmd.Flags().Uint64("length", 0, "range length in bytes (requires --offset)")
	statCmd.Flags().String("layout", "", "UnixFS layout override (defaults to the selected Bucket layout, or hybrid-v1 without a Bucket)")
	catCmd.Flags().String("layout", "", "UnixFS layout override (defaults to the selected Bucket layout, or hybrid-v1 without a Bucket)")
	rmCmd.Flags().String("layout", "", "UnixFS layout override (defaults to the selected Bucket layout, or hybrid-v1 without a Bucket)")
	rootCmd.AddCommand(statCmd, catCmd, rmCmd)
}

func configuredContent(cmd *cobra.Command, selector string) (*localruntime.Content, error) {
	services, err := configuredRuntimeServices()
	if err != nil {
		return nil, err
	}
	layout, err := cmd.Flags().GetString("layout")
	if err != nil {
		return nil, err
	}
	return services.OpenContent(cmd.Context(), selector, layout)
}

func runStat(cmd *cobra.Command, args []string) (resultErr error) {
	content, err := configuredContent(cmd, args[0])
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, content.Close()) }()
	app := content.UnixFS
	stat, err := app.Stat(cmd.Context(), args[0], optionalPath(args))
	if err != nil {
		return daemonCommandError(err)
	}
	printJSON(stat)
	return nil
}

func runCat(cmd *cobra.Command, args []string) (resultErr error) {
	offsetSet := cmd.Flags().Changed("offset")
	lengthSet := cmd.Flags().Changed("length")
	if offsetSet != lengthSet {
		return fmt.Errorf("--offset and --length must be provided together")
	}
	content, err := configuredContent(cmd, args[0])
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, content.Close()) }()
	app := content.UnixFS
	var result *unixfs.ReadResult
	if offsetSet {
		offset, _ := cmd.Flags().GetUint64("offset")
		length, _ := cmd.Flags().GetUint64("length")
		result, err = app.ReadFileRange(cmd.Context(), args[0], optionalPath(args), offset, length)
	} else {
		result, err = app.ReadFile(cmd.Context(), args[0], optionalPath(args))
	}
	if err != nil {
		return daemonCommandError(err)
	}
	_, err = cmd.OutOrStdout().Write(result.Body)
	return err
}

func runRemove(cmd *cobra.Command, args []string) (resultErr error) {
	content, err := configuredContent(cmd, args[0])
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, content.Close()) }()
	result, err := content.RemovePath(cmd.Context(), args[0], args[1])
	if err != nil {
		return daemonCommandError(err)
	}
	printJSON(result)
	return nil
}

func optionalPath(args []string) string {
	if len(args) > 1 {
		return args[1]
	}
	return ""
}
