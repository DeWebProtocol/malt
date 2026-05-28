package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/dewebprotocol/malt/config"
	daemonapp "github.com/dewebprotocol/malt/daemon"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(daemonCmd)
	daemonCmd.PersistentFlags().StringVar(&daemonListenOverride, "listen", "", "override daemon listen address")
	daemonCmd.AddCommand(daemonStartCmd)
	daemonCmd.AddCommand(daemonStatusCmd)
	daemonCmd.AddCommand(daemonStopCmd)
	daemonCmd.AddCommand(daemonRestartCmd)
}

var daemonListenOverride string

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run or manage the local MALT daemon",
	RunE:  runDaemon,
}

var daemonStartCmd = &cobra.Command{
	Use:           "start",
	Short:         "Start the local MALT daemon in the background",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runDaemonStart,
}

var daemonStatusCmd = &cobra.Command{
	Use:           "status",
	Short:         "Show the local MALT daemon status",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runDaemonStatus,
}

var daemonStopCmd = &cobra.Command{
	Use:           "stop",
	Short:         "Stop the managed local MALT daemon",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runDaemonStop,
}

var daemonRestartCmd = &cobra.Command{
	Use:           "restart",
	Short:         "Restart the managed local MALT daemon",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runDaemonRestart,
}

func runDaemon(cmd *cobra.Command, args []string) error {
	cfg, err := loadRuntimeConfig()
	if err != nil {
		return err
	}

	return daemonapp.Run(cfg, daemonapp.RunOptions{
		ListenOverride: daemonListenOverride,
		APILabel:       "malt daemon",
		Stdout:         os.Stdout,
		Stderr:         os.Stderr,
	})
}

func runDaemonStart(cmd *cobra.Command, args []string) error {
	cfg, err := loadDaemonLifecycleConfig()
	if err != nil {
		return err
	}
	manager, err := newDaemonLifecycleManager()
	if err != nil {
		return err
	}
	status, err := manager.Start(cmd.Context(), cfg)
	if err != nil {
		return err
	}
	printDaemonRunningStatus(os.Stdout, status)
	return nil
}

func runDaemonStatus(cmd *cobra.Command, args []string) error {
	cfg, err := loadDaemonLifecycleConfig()
	if err != nil {
		return err
	}
	manager, err := newDaemonLifecycleManager()
	if err != nil {
		return err
	}
	status, err := manager.Status(cmd.Context(), cfg)
	if err != nil {
		return err
	}
	printDaemonStatus(os.Stdout, status)
	if !status.Running {
		return fmt.Errorf("daemon is not running")
	}
	return nil
}

func runDaemonStop(cmd *cobra.Command, args []string) error {
	cfg, err := loadDaemonLifecycleConfig()
	if err != nil {
		return err
	}
	manager, err := newDaemonLifecycleManager()
	if err != nil {
		return err
	}
	status, err := manager.Stop(cmd.Context(), cfg)
	if errors.Is(err, daemonapp.ErrDaemonStateNotFound) {
		return fmt.Errorf("no managed daemon state found")
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "malt daemon stopped\n")
	if status.PID > 0 {
		fmt.Fprintf(os.Stdout, "pid: %d\n", status.PID)
	}
	return nil
}

func runDaemonRestart(cmd *cobra.Command, args []string) error {
	cfg, err := loadDaemonLifecycleConfig()
	if err != nil {
		return err
	}
	manager, err := newDaemonLifecycleManager()
	if err != nil {
		return err
	}
	status, err := manager.Restart(cmd.Context(), cfg)
	if err != nil {
		return err
	}
	printDaemonRunningStatus(os.Stdout, status)
	return nil
}

func loadDaemonLifecycleConfig() (*config.Config, error) {
	cfg, err := loadRuntimeConfig()
	if err != nil {
		return nil, err
	}
	if daemonListenOverride != "" {
		cfg.RPC.Listen = daemonListenOverride
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func newDaemonLifecycleManager() (*daemonapp.LifecycleManager, error) {
	configPath, err := config.ResolveConfigPath(cfgFile)
	if err != nil {
		return nil, err
	}
	statePath, err := daemonapp.ResolveDaemonStatePath(cfgFile)
	if err != nil {
		return nil, err
	}
	logPath, err := daemonapp.ResolveDaemonLogPath(cfgFile)
	if err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("determine executable: %w", err)
	}
	return daemonapp.NewLifecycleManager(daemonapp.LifecycleOptions{
		ConfigPath:     configPath,
		StatePath:      statePath,
		LogPath:        logPath,
		Executable:     exe,
		ForegroundArgs: daemonForegroundArgs(),
		Env:            os.Environ(),
	}), nil
}

func daemonForegroundArgs() []string {
	var args []string
	if cfgFile != "" {
		args = append(args, "--config", cfgFile)
	}
	args = append(args, "daemon")
	if daemonListenOverride != "" {
		args = append(args, "--listen", daemonListenOverride)
	}
	return args
}

func printDaemonRunningStatus(w io.Writer, status *daemonapp.DaemonStatus) {
	if status.Managed {
		fmt.Fprintf(w, "malt daemon running\n")
	} else {
		fmt.Fprintf(w, "malt daemon already running\n")
	}
	printDaemonStatusFields(w, status)
}

func printDaemonStatus(w io.Writer, status *daemonapp.DaemonStatus) {
	if status.Running {
		printDaemonRunningStatus(w, status)
		return
	}
	fmt.Fprintf(w, "malt daemon stopped\n")
	printDaemonStatusFields(w, status)
	if status.HealthError != nil {
		fmt.Fprintf(w, "health_error: %v\n", status.HealthError)
	}
}

func printDaemonStatusFields(w io.Writer, status *daemonapp.DaemonStatus) {
	if status.PID > 0 {
		fmt.Fprintf(w, "pid: %d\n", status.PID)
	}
	if status.Listen != "" {
		fmt.Fprintf(w, "listen: %s\n", status.Listen)
	}
	if status.BaseURL != "" {
		fmt.Fprintf(w, "api: %s\n", status.BaseURL)
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
