package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var (
	initForce          bool
	initNonInteractive bool
	initListen         string
	initKVStoreType    string
	initDataDir        string
)

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().BoolVar(&initForce, "force", false, "overwrite an existing settings file")
	initCmd.Flags().BoolVar(&initNonInteractive, "non-interactive", false, "do not prompt; use defaults and explicit flags")
	initCmd.Flags().StringVar(&initListen, "listen", "", "listen address")
	initCmd.Flags().StringVar(&initKVStoreType, "kvstore-type", "", "KV store type: badger, memory, or fs")
	initCmd.Flags().StringVar(&initDataDir, "data-dir", "", "KV store data directory")
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Create ~/.malt/cas/settings.json",
	RunE:  runInit,
}

func runInit(cmd *cobra.Command, args []string) error {
	cfg := DefaultConfig()

	configPath, err := DefaultConfigPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(configPath); err == nil && !initForce {
		return fmt.Errorf("settings already exist at %s (use --force to overwrite)", configPath)
	}

	reader := bufio.NewReader(os.Stdin)
	cfg.Listen = promptString(reader, "Listen address", initListen, cfg.Listen, initNonInteractive)
	cfg.KVStore.Type = promptString(reader, "KV store type (badger|memory|fs)", initKVStoreType, cfg.KVStore.Type, initNonInteractive)
	if cfg.KVStore.Type != "memory" {
		cfg.KVStore.DataDir = promptString(reader, "Data directory", initDataDir, cfg.KVStore.DataDir, initNonInteractive)
	}

	dir, _ := DefaultConfigDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := WriteToFile(configPath, cfg); err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "wrote settings to %s\n", configPath)
	fmt.Fprintf(os.Stdout, "listen: %s\n", cfg.Listen)
	fmt.Fprintf(os.Stdout, "kvstore: type=%s data_dir=%s\n", cfg.KVStore.Type, cfg.KVStorePath())
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
