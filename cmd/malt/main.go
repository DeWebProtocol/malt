// Package main provides the primary MALT CLI.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	Version = "dev"
	cfgFile string
)

var rootCmd = &cobra.Command{
	Use:   "malt",
	Short: "MALT runtime CLI",
	Long: `MALT is an authenticated structure layer over immutable content-addressed storage.

Primary commands:
  init        Create ~/.malt/malt.json and choose local state paths
  daemon      Run the local MALT daemon
  add         Upload local files/directories to CAS and merge into the current root
  cat         Stream file content from the current root
  get         Export a file or directory from the current root
  resolve     Resolve a path via the daemon
  update      Mutate structure via the daemon
  prove       Resolve and print transcript evidence via the daemon
  verify      Verify a transcript via the daemon
  lineage     Query lineage via the daemon
  cas         Interact directly with the configured CAS endpoint`,
	Version: Version,
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file (default: ~/.malt/malt.json)")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
