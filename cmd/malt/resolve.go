package main

import (
	"fmt"

	malt "github.com/dewebprotocol/malt"
	"github.com/dewebprotocol/malt/protocol"
	clientverifier "github.com/dewebprotocol/malt/sdk/verifier"
	cid "github.com/ipfs/go-cid"
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

	rawPath := ""
	if len(args) > 1 {
		rawPath = args[1]
	}
	root, err := cid.Parse(args[0])
	if err != nil {
		return err
	}
	segmentPath, err := malt.ParseSegmentPath(rawPath)
	if err != nil {
		return err
	}
	request, err := protocol.NewResolveRequest(malt.ResolveRequest{Root: root, Segments: segmentPath.Segments()})
	if err != nil {
		return err
	}
	result, err := client.ResolveContract(cmd.Context(), request)
	if err != nil {
		return daemonCommandError(err)
	}
	verifier, err := clientverifier.NewDefault()
	if err != nil {
		return fmt.Errorf("initialize local verifier: %w", err)
	}
	if err := verifier.VerifyResolve(cmd.Context(), protocol.ResolveVerification{Request: request, Result: *result}); err != nil {
		return fmt.Errorf("verify resolve result locally: %w", err)
	}

	return printResolveResult(cmd, result)
}

func printResolveResult(cmd *cobra.Command, result *protocol.ResolveResult) error {
	printJSON(result)
	return nil
}
