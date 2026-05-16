// Package evalsummary provides the evaluation summary command.
package evalsummary

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dewebprotocol/malt/internal/eval/summary"
	"github.com/spf13/cobra"
)

type options struct {
	inputDir string
	outDir   string
}

// NewCommand creates `malt-eval summarize`.
func NewCommand() *cobra.Command {
	opts := &options{}
	cmd := &cobra.Command{
		Use:   "summarize",
		Short: "Summarize framework raw evaluation envelopes into figure CSVs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(opts)
		},
	}
	cmd.Flags().StringVar(&opts.inputDir, "input", opts.inputDir, "Evaluation run directory containing raw JSONL envelopes")
	cmd.Flags().StringVar(&opts.outDir, "out", opts.outDir, "Summary output directory (default: <input>/summary)")
	return cmd
}

func run(opts *options) error {
	if opts == nil {
		return fmt.Errorf("options are nil")
	}
	inputDir := strings.TrimSpace(opts.inputDir)
	if inputDir == "" {
		return fmt.Errorf("--input is required")
	}
	outDir := strings.TrimSpace(opts.outDir)
	if outDir == "" {
		outDir = filepath.Join(inputDir, "summary")
	}
	return summary.Summarize(inputDir, outDir)
}
