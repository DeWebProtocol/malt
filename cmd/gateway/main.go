package main

import (
	"fmt"
	"os"

	"github.com/dewebprotocol/malt/config"
	daemonapp "github.com/dewebprotocol/malt/daemon"
	"github.com/spf13/cobra"
)

var (
	Version = "dev"
	cfgFile string
	listen  string
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "malt-gateway",
		Short: "Debug alias for the MALT daemon server",
		Long: `malt-gateway is a thin debug alias for the MALT daemon server.

It starts the same /api/v1 daemon API and optional embedded mock CAS used by
the main "malt daemon" command. This binary remains only for evaluation and
debugging; the primary product entrypoint is "malt daemon".`,
		Version: Version,
		RunE:    runGateway,
	}

	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file")
	rootCmd.PersistentFlags().StringVarP(&listen, "listen", "l", "", "override daemon listen address")

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runGateway(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig(cfgFile)
	if err != nil {
		return err
	}

	return daemonapp.Run(cfg, daemonapp.RunOptions{
		ListenOverride: listen,
		APILabel:       "malt-gateway serving daemon API",
		Stdout:         os.Stdout,
		Stderr:         os.Stderr,
	})
}

func loadConfig(explicitPath string) (*config.Config, error) {
	if explicitPath == "" {
		return config.Load()
	}
	return config.LoadFromFile(explicitPath)
}
