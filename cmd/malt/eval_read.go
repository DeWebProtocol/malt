package main

import (
	"os"

	"github.com/dewebprotocol/malt/internal/eval/readbench"
	"github.com/spf13/cobra"
)

var (
	evalReadBucket     = ""
	evalReadDepth      = readbench.DefaultDirectoryDepth
	evalReadSmallBytes = readbench.DefaultSmallFileBytes
	evalReadLargeBytes = readbench.DefaultLargeFileBytes
	evalReadRange      = readbench.DefaultRangeHeader
	evalReadIterations = readbench.DefaultIterations
)

func init() {
	rootCmd.AddCommand(evalReadCmd)
	evalReadCmd.Flags().StringVar(&evalReadBucket, "bucket", evalReadBucket, "bucket id for the deterministic read fixture (defaults to a fresh readbench-* bucket)")
	evalReadCmd.Flags().IntVar(&evalReadDepth, "depth", evalReadDepth, "directory depth for fixture paths")
	evalReadCmd.Flags().IntVar(&evalReadSmallBytes, "small-bytes", evalReadSmallBytes, "small raw file size in bytes")
	evalReadCmd.Flags().IntVar(&evalReadLargeBytes, "large-bytes", evalReadLargeBytes, "large list-backed file size in bytes")
	evalReadCmd.Flags().StringVar(&evalReadRange, "range", evalReadRange, "HTTP Range header for content_range reads")
	evalReadCmd.Flags().IntVar(&evalReadIterations, "iterations", evalReadIterations, "number of prooflist/content-range operation pairs")
}

var evalReadCmd = &cobra.Command{
	Use:   "eval-read",
	Short: "Run a MALT-only read benchmark and emit JSONL",
	Args:  cobra.NoArgs,
	RunE:  runEvalRead,
}

func runEvalRead(cmd *cobra.Command, args []string) error {
	cfg, err := loadRuntimeConfig()
	if err != nil {
		return err
	}

	runner := readbench.NewRunner(cfg.APIBaseURL())
	return runner.RunJSONL(cmd.Context(), readbench.RunConfig{
		Fixture: readbench.FixtureConfig{
			Bucket:         evalReadBucket,
			DirectoryDepth: evalReadDepth,
			SmallFileBytes: evalReadSmallBytes,
			LargeFileBytes: evalReadLargeBytes,
		},
		RangeHeader: evalReadRange,
		Iterations:  evalReadIterations,
	}, os.Stdout)
}
