// Package evalrun provides the unified evaluation plan runner command.
package evalrun

import (
	"fmt"
	"strings"

	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/framework"
	"github.com/spf13/cobra"
)

const PlanSchemaPath = "cmd/eval/schemas/run-plan.schema.json"

type options struct {
	planPath  string
	outputDir string
	runID     string
}

// NewCommand creates `malt-eval run`.
func NewCommand(registry framework.Registry) *cobra.Command {
	opts := &options{}
	cmd := &cobra.Command{
		Use:         "run",
		Short:       "Run an evaluation plan and write a structured result directory",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"malt.plan_schema": PlanSchemaPath},
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd, registry, opts)
		},
	}
	cmd.Flags().StringVar(&opts.planPath, "plan", opts.planPath, "Evaluation plan JSON file")
	cmd.Flags().StringVar(&opts.outputDir, "out", opts.outputDir, "Override output directory")
	cmd.Flags().StringVar(&opts.runID, "run-id", opts.runID, "Override run identifier")
	return cmd
}

func run(cmd *cobra.Command, registry framework.Registry, opts *options) error {
	if opts == nil {
		return fmt.Errorf("options are nil")
	}
	if strings.TrimSpace(opts.planPath) == "" {
		return fmt.Errorf("--plan is required")
	}
	plan, err := framework.LoadPlan(opts.planPath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(opts.outputDir) != "" {
		plan.OverrideOutputDir(opts.outputDir)
	}
	if strings.TrimSpace(opts.runID) != "" {
		plan.OverrideRunID(opts.runID)
	}
	return framework.Run(cmd.Context(), plan, registry, framework.RunOptions{})
}
