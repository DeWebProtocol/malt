package main

import (
	"os"

	daemonapp "github.com/dewebprotocol/malt/daemon"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(daemonCmd)
	daemonCmd.Flags().String("listen", "", "override daemon listen address")
}

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run the local MALT daemon",
	RunE:  runDaemon,
}

func runDaemon(cmd *cobra.Command, args []string) error {
	cfg, err := loadRuntimeConfig()
	if err != nil {
		return err
	}

	override, _ := cmd.Flags().GetString("listen")
	return daemonapp.Run(cfg, daemonapp.RunOptions{
		ListenOverride: override,
		APILabel:       "malt daemon",
		Stdout:         os.Stdout,
		Stderr:         os.Stderr,
	})
}
