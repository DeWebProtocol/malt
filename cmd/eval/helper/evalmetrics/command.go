// Package evalmetrics provides daemon evaluation metrics commands.
package evalmetrics

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/dewebprotocol/malt/config"
	daemonclient "github.com/dewebprotocol/malt/sdk/client"
	"github.com/spf13/cobra"
)

type options struct {
	cfgFile string
	out     io.Writer
}

// NewCommand creates the unified `malt-eval metrics` subcommand.
func NewCommand() *cobra.Command {
	return newCommand("metrics", "Inspect daemon evaluation metrics", os.Stdout)
}

func newCommand(use, short string, out io.Writer) *cobra.Command {
	opts := &options{out: out}
	root := &cobra.Command{
		Use:   use,
		Short: short,
	}
	root.PersistentFlags().StringVarP(&opts.cfgFile, "config", "c", "", "config file (default: ~/.malt/malt.json)")
	root.AddCommand(
		&cobra.Command{
			Use:   "snapshot",
			Short: "Print daemon evaluation metrics",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return runSnapshot(cmd, opts)
			},
		},
		&cobra.Command{
			Use:   "reset",
			Short: "Reset daemon evaluation metrics",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return runReset(cmd, opts)
			},
		},
	)
	return root
}

func runSnapshot(cmd *cobra.Command, opts *options) error {
	client, err := daemonClient(opts.cfgFile)
	if err != nil {
		return err
	}
	resp, err := client.MetricsSnapshot(cmd.Context())
	if err != nil {
		return err
	}
	return printJSON(opts.out, resp)
}

func runReset(cmd *cobra.Command, opts *options) error {
	client, err := daemonClient(opts.cfgFile)
	if err != nil {
		return err
	}
	resp, err := client.ResetMetrics(cmd.Context())
	if err != nil {
		return err
	}
	return printJSON(opts.out, resp)
}

func daemonClient(cfgFile string) (*daemonclient.Client, error) {
	var (
		cfg *config.Config
		err error
	)
	if cfgFile != "" {
		cfg, err = config.LoadFromFile(cfgFile)
	} else {
		cfg, err = config.Load()
	}
	if err != nil {
		return nil, err
	}
	return daemonclient.New(cfg), nil
}

func printJSON(out io.Writer, v any) error {
	if out == nil {
		return fmt.Errorf("output writer is nil")
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
