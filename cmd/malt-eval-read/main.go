// Package main provides the MALT read benchmark utility.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/internal/eval/readbench"
	"github.com/spf13/cobra"
)

var (
	cfgFile        string
	evalFixture    = ""
	evalDepth      = readbench.DefaultDirectoryDepth
	evalSmallBytes = readbench.DefaultSmallFileBytes
	evalLargeBytes = readbench.DefaultLargeFileBytes
	evalRange      = readbench.DefaultRangeHeader
	evalIterations = readbench.DefaultIterations
	evalArcFlags   []string
)

var rootCmd = &cobra.Command{
	Use:   "malt-eval-read",
	Short: "Run a MALT-only read benchmark and emit JSONL",
	Args:  cobra.NoArgs,
	RunE:  runEvalRead,
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file (default: ~/.malt/malt.json)")
	rootCmd.Flags().StringVar(&evalFixture, "fixture", evalFixture, "name for the deterministic read fixture (defaults to a fresh readbench-* fixture)")
	rootCmd.Flags().IntVar(&evalDepth, "depth", evalDepth, "directory depth for fixture paths")
	rootCmd.Flags().IntVar(&evalSmallBytes, "small-bytes", evalSmallBytes, "small raw file size in bytes")
	rootCmd.Flags().IntVar(&evalLargeBytes, "large-bytes", evalLargeBytes, "large list-backed file size in bytes")
	rootCmd.Flags().StringVar(&evalRange, "range", evalRange, "HTTP Range header for content_range reads")
	rootCmd.Flags().IntVar(&evalIterations, "iterations", evalIterations, "number of prooflist/content-range operation pairs")
	rootCmd.Flags().StringArrayVar(&evalArcFlags, "arc", nil, "Initial fixture arc as path=cid; repeatable and must include @payload")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runEvalRead(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	arcs, err := parseArcFlags(evalArcFlags)
	if err != nil {
		return err
	}
	if len(arcs) == 0 {
		return fmt.Errorf("--arc is required at least once and must include @payload")
	}
	if _, ok := arcs["@payload"]; !ok {
		return fmt.Errorf("--arc is required at least once and must include @payload")
	}

	runner := readbench.NewRunner(cfg.APIBaseURL())
	return runner.RunJSONL(cmd.Context(), readbench.RunConfig{
		Fixture: readbench.FixtureConfig{
			FixtureName:    evalFixture,
			DirectoryDepth: evalDepth,
			SmallFileBytes: evalSmallBytes,
			LargeFileBytes: evalLargeBytes,
			Arcs:           arcs,
		},
		RangeHeader: evalRange,
		Iterations:  evalIterations,
	}, os.Stdout)
}

func loadConfig() (*config.Config, error) {
	if cfgFile != "" {
		return config.LoadFromFile(cfgFile)
	}
	return config.Load()
}

func parseArcFlags(flags []string) (map[string]string, error) {
	arcs := make(map[string]string, len(flags))
	for _, flag := range flags {
		key, value, ok := strings.Cut(flag, "=")
		if !ok {
			return nil, fmt.Errorf("invalid --arc %q, expected path=cid", flag)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			return nil, fmt.Errorf("invalid --arc %q, expected non-empty path and cid", flag)
		}
		arcs[key] = value
	}
	return arcs, nil
}
