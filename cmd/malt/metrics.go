package main

import (
	"encoding/json"
	"os"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(metricsCmd)
	metricsCmd.AddCommand(metricsSnapshotCmd, metricsResetCmd)
}

var metricsCmd = &cobra.Command{
	Use:   "metrics",
	Short: "Inspect daemon evaluation metrics",
}

var metricsSnapshotCmd = &cobra.Command{
	Use:   "snapshot",
	Short: "Print daemon evaluation metrics",
	Args:  cobra.NoArgs,
	RunE:  runMetricsSnapshot,
}

var metricsResetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Reset daemon evaluation metrics",
	Args:  cobra.NoArgs,
	RunE:  runMetricsReset,
}

func runMetricsSnapshot(cmd *cobra.Command, args []string) error {
	resp, err := mustDaemonClient().MetricsSnapshot(cmd.Context())
	if err != nil {
		return daemonCommandError(err)
	}
	return printMetricsResponse(resp)
}

func runMetricsReset(cmd *cobra.Command, args []string) error {
	resp, err := mustDaemonClient().ResetMetrics(cmd.Context())
	if err != nil {
		return daemonCommandError(err)
	}
	return printMetricsResponse(resp)
}

func printMetricsResponse(resp any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(resp)
}
