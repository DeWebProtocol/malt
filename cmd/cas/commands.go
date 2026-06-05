package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(restartCmd)
}

var startCmd = &cobra.Command{
	Use:           "start",
	Short:         "Start the CAS daemon in the background",
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runStart,
}

var statusCmd = &cobra.Command{
	Use:           "status",
	Short:         "Show the CAS daemon status",
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runStatus,
}

var stopCmd = &cobra.Command{
	Use:           "stop",
	Short:         "Stop the managed CAS daemon",
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runStop,
}

var restartCmd = &cobra.Command{
	Use:           "restart",
	Short:         "Restart the managed CAS daemon",
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runRestart,
}

func runStart(cmd *cobra.Command, args []string) error {
	cfg, err := Load(configFile)
	if err != nil {
		return err
	}
	statePath, err := ResolveStatePath()
	if err != nil {
		return err
	}
	logPath, err := ResolveLogPath()
	if err != nil {
		return err
	}
	if listen != "" {
		cfg.Listen = listen
	}
	status, err := daemonStart(cmd.Context(), statePath, logPath, cfg, daemonOverridesFromGlobals())
	if err != nil {
		return err
	}
	printRunningStatus(os.Stdout, status)
	return nil
}

func runStatus(cmd *cobra.Command, args []string) error {
	cfg, err := Load(configFile)
	if err != nil {
		return err
	}
	statePath, err := ResolveStatePath()
	if err != nil {
		return err
	}
	status := daemonStatus(statePath, cfg)
	printStatus(os.Stdout, status)
	return nil
}

func runStop(cmd *cobra.Command, args []string) error {
	statePath, err := ResolveStatePath()
	if err != nil {
		return err
	}
	status, err := daemonStop(cmd.Context(), statePath)
	if errors.Is(err, ErrDaemonStateNotFound) {
		return fmt.Errorf("no managed CAS daemon state found")
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "CAS daemon stopped\n")
	if status.PID > 0 {
		fmt.Fprintf(os.Stdout, "pid: %d\n", status.PID)
	}
	return nil
}

func runRestart(cmd *cobra.Command, args []string) error {
	cfg, err := Load(configFile)
	if err != nil {
		return err
	}
	statePath, err := ResolveStatePath()
	if err != nil {
		return err
	}
	logPath, err := ResolveLogPath()
	if err != nil {
		return err
	}
	if listen != "" {
		cfg.Listen = listen
	}
	status, err := daemonRestart(cmd.Context(), statePath, logPath, cfg, daemonOverridesFromGlobals())
	if err != nil {
		return err
	}
	printRunningStatus(os.Stdout, status)
	return nil
}

func printRunningStatus(w io.Writer, status *DaemonStatus) {
	if status.Managed {
		fmt.Fprintf(w, "CAS daemon running\n")
	} else {
		fmt.Fprintf(w, "CAS daemon already running\n")
	}
	printStatusFields(w, status)
}

func printStatus(w io.Writer, status *DaemonStatus) {
	if status.Running {
		printRunningStatus(w, status)
		return
	}
	fmt.Fprintf(w, "CAS daemon stopped\n")
	printStatusFields(w, status)
	if status.HealthError != nil {
		fmt.Fprintf(w, "health_error: %v\n", status.HealthError)
	}
}

func printStatusFields(w io.Writer, status *DaemonStatus) {
	if status.PID > 0 {
		fmt.Fprintf(w, "pid: %d\n", status.PID)
	}
	if status.Listen != "" {
		fmt.Fprintf(w, "listen: %s\n", status.Listen)
	}
	if status.ConfigPath != "" {
		fmt.Fprintf(w, "config: %s\n", status.ConfigPath)
	}
	if status.StatePath != "" {
		fmt.Fprintf(w, "state: %s\n", status.StatePath)
	}
	if status.LogPath != "" {
		fmt.Fprintf(w, "log: %s\n", status.LogPath)
	}
	if !status.StartedAt.IsZero() {
		fmt.Fprintf(w, "started_at: %s\n", status.StartedAt.Format("2006-01-02T15:04:05Z07:00"))
	}
}
