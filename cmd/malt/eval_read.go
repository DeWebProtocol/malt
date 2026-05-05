package main

import (
	"fmt"
	"os"

	"github.com/dewebprotocol/malt/internal/eval/readbench"
	"github.com/spf13/cobra"
)

var (
	evalReadFixture    = ""
	evalReadDepth      = readbench.DefaultDirectoryDepth
	evalReadSmallBytes = readbench.DefaultSmallFileBytes
	evalReadLargeBytes = readbench.DefaultLargeFileBytes
	evalReadRange      = readbench.DefaultRangeHeader
	evalReadIterations = readbench.DefaultIterations
	evalReadArcFlags   []string
	evalReadArcs       map[string]string
)

func init() {
	rootCmd.AddCommand(evalReadCmd)
	evalReadCmd.Flags().StringVar(&evalReadFixture, "fixture", evalReadFixture, "name for the deterministic read fixture (defaults to a fresh readbench-* fixture)")
	evalReadCmd.Flags().IntVar(&evalReadDepth, "depth", evalReadDepth, "directory depth for fixture paths")
	evalReadCmd.Flags().IntVar(&evalReadSmallBytes, "small-bytes", evalReadSmallBytes, "small raw file size in bytes")
	evalReadCmd.Flags().IntVar(&evalReadLargeBytes, "large-bytes", evalReadLargeBytes, "large list-backed file size in bytes")
	evalReadCmd.Flags().StringVar(&evalReadRange, "range", evalReadRange, "HTTP Range header for content_range reads")
	evalReadCmd.Flags().IntVar(&evalReadIterations, "iterations", evalReadIterations, "number of prooflist/content-range operation pairs")
	evalReadCmd.Flags().StringArrayVar(&evalReadArcFlags, "arc", nil, "Initial fixture arc as path=cid; repeatable and must include @payload")
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
	fixtureConfig := readbench.FixtureConfig{
		FixtureName:    evalReadFixture,
		DirectoryDepth: evalReadDepth,
		SmallFileBytes: evalReadSmallBytes,
		LargeFileBytes: evalReadLargeBytes,
	}
	arcs, err := evalReadFixtureArcs()
	if err != nil {
		return err
	}
	if len(arcs) == 0 {
		return fmt.Errorf("--arc is required at least once and must include @payload")
	}
	fixtureConfig.Arcs = arcs
	if _, ok := fixtureConfig.Arcs["@payload"]; !ok {
		return fmt.Errorf("--arc is required at least once and must include @payload")
	}
	return runner.RunJSONL(cmd.Context(), readbench.RunConfig{
		Fixture:     fixtureConfig,
		RangeHeader: evalReadRange,
		Iterations:  evalReadIterations,
	}, os.Stdout)
}

func evalReadFixtureArcs() (map[string]string, error) {
	if len(evalReadArcFlags) > 0 {
		return parseCreatePairs(evalReadArcFlags)
	}
	if len(evalReadArcs) > 0 {
		return evalReadArcs, nil
	}
	return nil, nil
}
