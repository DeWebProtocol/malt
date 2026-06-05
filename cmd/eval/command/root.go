// Package command assembles the unified MALT evaluation CLI.
package command

import (
	"github.com/dewebprotocol/malt/cmd/eval/helper/evalmetrics"
	"github.com/dewebprotocol/malt/cmd/eval/helper/evalread"
	"github.com/dewebprotocol/malt/cmd/eval/helper/evalrun"
	"github.com/dewebprotocol/malt/cmd/eval/helper/evalschema"
	"github.com/dewebprotocol/malt/cmd/eval/helper/evalsuites"
	"github.com/dewebprotocol/malt/cmd/eval/helper/evalsummary"
	"github.com/dewebprotocol/malt/cmd/eval/helper/evalwrite"
	"github.com/spf13/cobra"
)

// NewRootCommand creates the top-level malt-eval command.
func NewRootCommand() *cobra.Command {
	registry := evalsuites.NewRegistry()
	cmd := &cobra.Command{
		Use:   "malt-eval",
		Short: "Run MALT evaluation workloads",
		Args:  cobra.NoArgs,
		RunE:  evalrun.RunIsolated(registry),
	}
	cmd.Flags().String("plan", "", "Evaluation plan JSON file")
	_ = cmd.MarkFlagRequired("plan")

	cmd.AddCommand(
		evalrun.NewCommand(registry),
		evalschema.NewCommand(),
		evalsummary.NewCommand(),
		evalread.NewCommand(),
		evalwrite.NewCommand(),
		evalmetrics.NewCommand(),
	)
	return cmd
}
