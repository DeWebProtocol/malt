// Package evalread provides the read-latency evaluation command.
package evalread

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/readbench"
	"github.com/dewebprotocol/malt/config"
	"github.com/spf13/cobra"
)

type options struct {
	cfgFile    string
	systemsCSV string
	fixture    string
	depth      int
	smallBytes int
	largeBytes int
	rangeValue string
	iterations int
	arcFlags   []string
	out        io.Writer
}

// NewCommand creates the unified `malt-eval read` subcommand.
func NewCommand() *cobra.Command {
	return newCommand("read", "Run a read benchmark across MALT and IPLD UnixFS baselines", os.Stdout)
}

func newCommand(use, short string, out io.Writer) *cobra.Command {
	opts := &options{
		systemsCSV: readbench.DefaultSystemsCSV,
		depth:      readbench.DefaultDirectoryDepth,
		smallBytes: readbench.DefaultSmallFileBytes,
		largeBytes: readbench.DefaultLargeFileBytes,
		rangeValue: readbench.DefaultRangeHeader,
		iterations: readbench.DefaultIterations,
		out:        out,
	}
	cmd := &cobra.Command{
		Use:         use,
		Short:       short,
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"malt.result_schema": readbench.ResultSchemaPath},
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd, opts)
		},
	}
	cmd.PersistentFlags().StringVarP(&opts.cfgFile, "config", "c", "", "config file (default: ~/.malt/malt.json)")
	cmd.Flags().StringVar(&opts.systemsCSV, "systems", opts.systemsCSV, "Comma-separated systems: maltflat, merkledag, hamt")
	cmd.Flags().StringVar(&opts.fixture, "fixture", opts.fixture, "name for the deterministic read fixture (defaults to a fresh readbench-* fixture)")
	cmd.Flags().IntVar(&opts.depth, "depth", opts.depth, "directory depth for fixture paths")
	cmd.Flags().IntVar(&opts.smallBytes, "small-bytes", opts.smallBytes, "small raw file size in bytes")
	cmd.Flags().IntVar(&opts.largeBytes, "large-bytes", opts.largeBytes, "large list-backed file size in bytes")
	cmd.Flags().StringVar(&opts.rangeValue, "range", opts.rangeValue, "HTTP Range header for content_range reads")
	cmd.Flags().IntVar(&opts.iterations, "iterations", opts.iterations, "number of prooflist/content-range operation pairs")
	cmd.Flags().StringArrayVar(&opts.arcFlags, "arc", nil, "Initial fixture arc as path=cid; repeatable and must include @payload")
	return cmd
}

func run(cmd *cobra.Command, opts *options) error {
	arcs, err := ParseArcFlags(opts.arcFlags)
	if err != nil {
		return err
	}
	systems, err := readbench.ParseSystemsCSV(opts.systemsCSV)
	if err != nil {
		return err
	}
	var apiBaseURL string
	if systemsInclude(systems, readbench.SystemMALTFlat) {
		cfg, err := loadConfig(opts.cfgFile)
		if err != nil {
			return err
		}
		apiBaseURL = cfg.APIBaseURL()
		if len(arcs) == 0 {
			return fmt.Errorf("--arc is required at least once and must include @payload")
		}
		if _, ok := arcs["@payload"]; !ok {
			return fmt.Errorf("--arc is required at least once and must include @payload")
		}
	}

	runner := readbench.NewRunner(apiBaseURL)
	return runner.RunJSONL(cmd.Context(), readbench.RunConfig{
		Systems: systems,
		Fixture: readbench.FixtureConfig{
			FixtureName:    opts.fixture,
			DirectoryDepth: opts.depth,
			SmallFileBytes: opts.smallBytes,
			LargeFileBytes: opts.largeBytes,
			Arcs:           arcs,
		},
		RangeHeader: opts.rangeValue,
		Iterations:  opts.iterations,
	}, opts.out)
}

func systemsInclude(systems []readbench.SystemName, target readbench.SystemName) bool {
	for _, system := range systems {
		if system == target {
			return true
		}
	}
	return false
}

func loadConfig(path string) (*config.Config, error) {
	if path != "" {
		return config.LoadFromFile(path)
	}
	return config.Load()
}

// ParseArcFlags parses repeatable path=cid fixture seed arcs.
func ParseArcFlags(flags []string) (map[string]string, error) {
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
