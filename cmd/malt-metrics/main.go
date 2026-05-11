// Package main provides the MALT metrics utility.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	daemonclient "github.com/dewebprotocol/malt/client"
	"github.com/dewebprotocol/malt/config"
	"github.com/spf13/cobra"
)

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "malt-metrics",
	Short: "Inspect daemon evaluation metrics",
}

var snapshotCmd = &cobra.Command{
	Use:   "snapshot",
	Short: "Print daemon evaluation metrics",
	Args:  cobra.NoArgs,
	RunE:  runSnapshot,
}

var resetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Reset daemon evaluation metrics",
	Args:  cobra.NoArgs,
	RunE:  runReset,
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file (default: ~/.malt/malt.json)")
	rootCmd.AddCommand(snapshotCmd, resetCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runSnapshot(cmd *cobra.Command, args []string) error {
	client, err := daemonClient()
	if err != nil {
		return err
	}
	resp, err := client.MetricsSnapshot(cmd.Context())
	if err != nil {
		return err
	}
	return printJSON(resp)
}

func runReset(cmd *cobra.Command, args []string) error {
	client, err := daemonClient()
	if err != nil {
		return err
	}
	resp, err := client.ResetMetrics(cmd.Context())
	if err != nil {
		return err
	}
	return printJSON(resp)
}

func daemonClient() (*daemonclient.Client, error) {
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

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
