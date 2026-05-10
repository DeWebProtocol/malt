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
  resolve     Resolve a path via the daemon
  verify      Verify a ProofList`,
	Version: Version,
	CompletionOptions: cobra.CompletionOptions{
		DisableDefaultCmd: true,
	},
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
