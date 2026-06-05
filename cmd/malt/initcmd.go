package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/dewebprotocol/malt/config"
	"github.com/spf13/cobra"
)

var (
	initForce          bool
	initNonInteractive bool
	initStateRoot      string
	initRPCListen      string
	initCASBaseURL     string
	initKVStoreType    string
)

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().BoolVar(&initForce, "force", false, "overwrite an existing config file")
	initCmd.Flags().BoolVar(&initNonInteractive, "non-interactive", false, "do not prompt; use defaults and explicit flags")
	initCmd.Flags().StringVar(&initStateRoot, "state-root", "", "override the local state root")
	initCmd.Flags().StringVar(&initRPCListen, "listen", "", "daemon listen address")
	initCmd.Flags().StringVar(&initCASBaseURL, "cas-base-url", "", "CAS base URL (e.g. http://127.0.0.1:4318)")
	initCmd.Flags().StringVar(&initKVStoreType, "kvstore-type", "", "state KV store type: badger, memory, or fs")
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Create ~/.malt/malt.json",
	RunE:  runInit,
}

func runInit(cmd *cobra.Command, args []string) error {
	cfg := config.DefaultConfig()

	configPath, err := config.ResolveConfigPath(cfgFile)
	if err != nil {
		return err
	}
	if _, err := os.Stat(configPath); err == nil && !initForce {
		return fmt.Errorf("config already exists at %s (use --force to overwrite)", configPath)
	}

	reader := bufio.NewReader(os.Stdin)
	cfg.State.RootDir = promptString(reader, "State root", initStateRoot, cfg.State.RootDir, initNonInteractive)
	cfg.State.KVStore.Type = promptString(reader, "KVStore type (badger|memory|fs)", initKVStoreType, cfg.State.KVStore.Type, initNonInteractive)
	cfg.RPC.Listen = promptString(reader, "Daemon listen", initRPCListen, cfg.RPC.Listen, initNonInteractive)
	cfg.CAS.BaseURL = promptString(reader, "CAS base URL", initCASBaseURL, cfg.CAS.BaseURL, initNonInteractive)

	if err := os.MkdirAll(cfg.State.RootDir, 0o755); err != nil {
		return fmt.Errorf("create state root: %w", err)
	}
	if err := config.WriteToFile(configPath, cfg); err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "wrote config to %s\n", configPath)
	fmt.Fprintf(os.Stdout, "state root: %s\n", cfg.State.RootDir)
	fmt.Fprintf(os.Stdout, "daemon API: %s\n", cfg.APIBaseURL())
	fmt.Fprintf(os.Stdout, "CAS: %s\n", cfg.CASBaseURL())
	return nil
}

func promptString(reader *bufio.Reader, label string, explicit string, fallback string, nonInteractive bool) string {
	if explicit != "" {
		return explicit
	}
	if nonInteractive {
		return fallback
	}

	fmt.Fprintf(os.Stdout, "%s [%s]: ", label, fallback)
	line, err := reader.ReadString('\n')
	if err != nil {
		return fallback
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return fallback
	}
	return line
}
