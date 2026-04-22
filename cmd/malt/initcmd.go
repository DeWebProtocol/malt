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
	initForce              bool
	initNonInteractive     bool
	initStateRoot          string
	initRPCListen          string
	initCASMode            string
	initCASBaseURL         string
	initEmbeddedMockListen string
	initEmbeddedMock       bool
	initDefaultBucketID    string
)

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().BoolVar(&initForce, "force", false, "overwrite an existing config file")
	initCmd.Flags().BoolVar(&initNonInteractive, "non-interactive", false, "do not prompt; use defaults and explicit flags")
	initCmd.Flags().StringVar(&initStateRoot, "state-root", "", "override the local state root")
	initCmd.Flags().StringVar(&initRPCListen, "listen", "", "daemon listen address")
	initCmd.Flags().StringVar(&initCASMode, "cas-mode", "", "CAS mode: external or embedded-mock")
	initCmd.Flags().StringVar(&initCASBaseURL, "cas-base-url", "", "external CAS base URL")
	initCmd.Flags().BoolVar(&initEmbeddedMock, "embedded-mock", true, "enable the embedded mock CAS when cas-mode is embedded-mock")
	initCmd.Flags().StringVar(&initEmbeddedMockListen, "embedded-mock-listen", "", "embedded mock CAS listen address")
	initCmd.Flags().StringVar(&initDefaultBucketID, "default-bucket", "", "default bucket id for CLI commands (optional)")
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
	cfg.RPC.Listen = promptString(reader, "Daemon listen", initRPCListen, cfg.RPC.Listen, initNonInteractive)
	cfg.CAS.Mode = promptString(reader, "CAS mode (external|embedded-mock)", initCASMode, cfg.CAS.Mode, initNonInteractive)
	cfg.Client.DefaultBucketID = promptString(reader, "Default bucket id (optional)", initDefaultBucketID, cfg.Client.DefaultBucketID, initNonInteractive)

	if cfg.CAS.Mode == "external" {
		cfg.CAS.EmbeddedMock.Enabled = false
		cfg.CAS.BaseURL = promptString(reader, "External CAS base URL", initCASBaseURL, "http://127.0.0.1:5001", initNonInteractive)
	} else {
		cfg.CAS.Mode = "embedded-mock"
		cfg.CAS.EmbeddedMock.Enabled = initEmbeddedMock
		cfg.CAS.EmbeddedMock.Listen = promptString(reader, "Embedded mock CAS listen", initEmbeddedMockListen, cfg.CAS.EmbeddedMock.Listen, initNonInteractive)
		cfg.CAS.BaseURL = ""
	}

	if err := os.MkdirAll(cfg.State.RootDir, 0o755); err != nil {
		return fmt.Errorf("create state root: %w", err)
	}
	if err := config.WriteToFile(configPath, cfg); err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "wrote config to %s\n", configPath)
	fmt.Fprintf(os.Stdout, "state root: %s\n", cfg.State.RootDir)
	fmt.Fprintf(os.Stdout, "daemon API: %s\n", cfg.APIBaseURL())
	if cfg.CAS.Mode == "embedded-mock" {
		fmt.Fprintf(os.Stdout, "embedded mock CAS: http://%s/api/v0\n", cfg.CAS.EmbeddedMock.Listen)
	} else {
		fmt.Fprintf(os.Stdout, "external CAS: %s\n", cfg.CAS.BaseURL)
	}
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
