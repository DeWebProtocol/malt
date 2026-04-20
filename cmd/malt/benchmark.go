package main

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt/eval"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(benchmarkCmd)
}

var benchmarkCmd = &cobra.Command{
	Use:   "benchmark [append|random|bulk]",
	Short: "Run a quick benchmark for a single workload",
	Long: `Run a benchmark for a specific workload type with default settings.
Use "eval" for comprehensive multi-configuration evaluation.

Examples:
  malt benchmark append
  malt benchmark random --arcs 1000 --rounds 50
  malt benchmark bulk --backend kzg`,
	Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
	ValidArgs: []string{"append", "random", "bulk"},
	RunE:      runBenchmark,
}

var (
	benchArcs    string
	benchRounds  int
	benchBackend string
	benchEATType string
	benchCSV     string
	benchSeed    int64
)

func init() {
	benchmarkCmd.Flags().StringVar(&benchArcs, "arcs", "100,1000", "Arc counts (comma-separated)")
	benchmarkCmd.Flags().IntVar(&benchRounds, "rounds", 100, "Number of update rounds")
	benchmarkCmd.Flags().StringVar(&benchBackend, "backend", "kzg", "Backend type (kzg)")
	benchmarkCmd.Flags().StringVar(&benchEATType, "eat", "overwrite", "EAT type (overwrite/versioned/bloom)")
	benchmarkCmd.Flags().StringVar(&benchCSV, "csv", "", "Export results to CSV file")
	benchmarkCmd.Flags().Int64Var(&benchSeed, "seed", 42, "Random seed")
}

func runBenchmark(cmd *cobra.Command, args []string) error {
	workload := args[0]

	// Parse arc counts
	var arcCounts []int
	for _, s := range splitCSV(benchArcs) {
		var n int
		if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
			return fmt.Errorf("invalid arc count %q: %w", s, err)
		}
		arcCounts = append(arcCounts, n)
	}

	// Create benchmark components
	tc, err := eval.NewTestComponentsWithEAT(
		eval.BackendType(benchBackend),
		eval.EATType(benchEATType),
		"bench",
	)
	if err != nil {
		return fmt.Errorf("create components: %w", err)
	}

	cfg := &eval.BenchmarkConfig{
		ArcCounts:    arcCounts,
		UpdateRounds: benchRounds,
		RandomSeed:   benchSeed,
		Backend:      eval.BackendType(benchBackend),
		EATType:      eval.EATType(benchEATType),
	}

	runner := eval.NewBenchmarkRunner(cfg, tc.BucketID, tc.EAT, tc.Semantic, tc.CAS)
	ctx := context.Background()

	fmt.Printf("Running %s benchmark: backend=%s, eat=%s, arcs=%v, rounds=%d\n\n",
		workload, cfg.Backend, cfg.EATType, arcCounts, benchRounds)

	var results map[int]*eval.Metrics
	switch workload {
	case "append":
		results, err = runner.RunAppendBenchmark(ctx)
	case "random":
		results, err = runner.RunRandomBenchmark(ctx)
	case "bulk":
		results, err = runner.RunBulkBenchmark(ctx)
	default:
		return fmt.Errorf("unknown workload: %s", workload)
	}
	if err != nil {
		return fmt.Errorf("benchmark failed: %w", err)
	}

	eval.PrintResults(results, workload)

	// Export CSV if requested
	if benchCSV != "" {
		rows, err := eval.ExportCSV(results, workload, cfg.Backend, cfg.EATType, benchCSV)
		if err != nil {
			return fmt.Errorf("export CSV: %w", err)
		}
		fmt.Printf("\nCSV exported: %s (%d rows)\n", benchCSV, rows)
	}

	return nil
}
