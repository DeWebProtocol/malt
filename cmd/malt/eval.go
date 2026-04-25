package main

import (
	"context"
	"fmt"
	"os"

	"github.com/dewebprotocol/malt/eval"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(evalCmd)
}

var evalCmd = &cobra.Command{
	Use:   "eval",
	Short: "Run comprehensive evaluation across backends and ArcTable types",
	Long: `Run the full evaluation matrix: backends x ArcTable types x workloads x arc counts.
Generates detailed metrics, CSV exports, and JSON results.

Examples:
  malt eval
  malt eval --arcs 50,100,500 --rounds 200
  malt eval --backend kzg --arctable overwrite --workload append
  malt eval --output results.json --csv-dir ./results`,
	RunE: runEval,
}

var (
	evalArcs         string
	evalRounds       int
	evalBackend      string
	evalArcTableType string
	evalWorkload     string
	evalOutput       string
	evalCSVDir       string
	evalSeed         int64
)

func init() {
	evalCmd.Flags().StringVar(&evalArcs, "arcs", "50,100,200", "Arc counts to test (comma-separated)")
	evalCmd.Flags().IntVar(&evalRounds, "rounds", 100, "Number of update rounds")
	evalCmd.Flags().StringVar(&evalBackend, "backend", "", "Single backend (kzg), default: all")
	evalCmd.Flags().StringVar(&evalArcTableType, "arctable", "", "Single ArcTable type (overwrite/versioned/bloom), default: all")
	evalCmd.Flags().StringVar(&evalWorkload, "workload", "", "Single workload (append/random/bulk), default: all")
	evalCmd.Flags().StringVar(&evalOutput, "output", "", "JSON output file path")
	evalCmd.Flags().StringVar(&evalCSVDir, "csv-dir", "", "Directory for CSV exports")
	evalCmd.Flags().Int64Var(&evalSeed, "seed", 42, "Random seed")
}

func runEval(cmd *cobra.Command, args []string) error {
	// Parse arc counts
	var arcCounts []int
	for _, s := range splitCSV(evalArcs) {
		var n int
		if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
			return fmt.Errorf("invalid arc count %q: %w", s, err)
		}
		arcCounts = append(arcCounts, n)
	}
	if len(arcCounts) == 0 {
		return fmt.Errorf("at least one arc count is required")
	}

	// Build eval config
	cfg := &eval.EvalConfig{
		ArcCounts:    arcCounts,
		UpdateRounds: evalRounds,
		RandomSeed:   evalSeed,
		CSVDir:       evalCSVDir,
	}

	// Filter backends
	if evalBackend != "" {
		bt := eval.BackendType(evalBackend)
		cfg.Backends = []eval.BackendType{bt}
	}

	// Filter ArcTable types
	if evalArcTableType != "" {
		et := eval.ArcTableType(evalArcTableType)
		cfg.ArcTableTypes = []eval.ArcTableType{et}
	}

	// Filter workloads
	if evalWorkload != "" {
		cfg.Workloads = []string{evalWorkload}
	}

	runner := eval.NewEvalRunner(cfg, "eval")
	ctx := context.Background()

	fmt.Println("Starting evaluation...")
	allResults, err := runner.RunAll(ctx)
	if err != nil {
		return fmt.Errorf("evaluation failed: %w", err)
	}

	// Print results
	eval.PrintFullResults(allResults)
	eval.PrintSummaryStats(allResults)

	// Generate LaTeX if running all workloads
	if evalWorkload == "" {
		for _, w := range []string{"append", "random", "bulk"} {
			fmt.Println(eval.GenerateLatexTable(allResults, w))
		}
	}

	// Export JSON
	if evalOutput != "" {
		if err := eval.ExportJSON(allResults, evalOutput); err != nil {
			return fmt.Errorf("export JSON: %w", err)
		}
		fmt.Fprintf(os.Stderr, "JSON results written to %s\n", evalOutput)
	}

	return nil
}

// splitCSV splits a comma-separated string and trims whitespace.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	var parts []string
	for _, p := range splitByChar(s, ',') {
		trimmed := ""
		for _, c := range p {
			if c != ' ' && c != '\t' {
				trimmed += string(c)
			}
		}
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return parts
}

// splitByChar splits a string by a delimiter character.
func splitByChar(s string, delim byte) []string {
	var result []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == delim {
			result = append(result, s[start:i])
			start = i + 1
		}
	}
	result = append(result, s[start:])
	return result
}
