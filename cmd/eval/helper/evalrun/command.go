// Package evalrun provides evaluation plan runner commands.
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
	resultDir string
	runID     string
	resume    bool
}

// NewCommand creates `malt-eval run`.
func NewCommand(registry framework.Registry) *cobra.Command {
	opts := &options{}
	cmd := &cobra.Command{
		Use:         "run",
		Short:       "Run an evaluation plan and write structured result/work directories",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"malt.plan_schema": PlanSchemaPath},
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd, registry, opts)
		},
	}
	cmd.Flags().StringVar(&opts.planPath, "plan", opts.planPath, "Evaluation plan JSON file")
	cmd.Flags().StringVar(&opts.resultDir, "out", opts.resultDir, "Override result directory")
	cmd.Flags().StringVar(&opts.resultDir, "result-dir", opts.resultDir, "Override result directory")
	cmd.Flags().StringVar(&opts.outputDir, "output-dir", opts.outputDir, "Override disposable output workspace directory")
	cmd.Flags().StringVar(&opts.runID, "run-id", opts.runID, "Override run identifier")
	cmd.Flags().BoolVar(&opts.resume, "resume", opts.resume, "Resume an interrupted run by preserving raw results and output workspace files")
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
	if strings.TrimSpace(opts.resultDir) != "" {
		plan.OverrideResultDir(opts.resultDir)
	}
	if strings.TrimSpace(opts.runID) != "" {
		plan.OverrideRunID(opts.runID)
	}
	if opts.resume {
		plan.EnableResume()
	}
	return framework.Run(cmd.Context(), plan, registry, framework.RunOptions{Stderr: cmd.ErrOrStderr()})
}
