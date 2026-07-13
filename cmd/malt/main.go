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
	Short: "MALT client tools and reference executor CLI",
	Long: `MALT is an authenticated structure layer over immutable content-addressed storage.

Primary commands:
  init        Create ~/.malt/malt.json and choose local state paths
  start       Start the local MALT reference executor in the background
  status      Show the local MALT reference executor status
  stop        Stop the managed local MALT reference executor
  restart     Restart the managed local MALT reference executor
  add         Upload local files/directories to CAS from a base root and print a result root
  resolve     Resolve a path via the reference executor
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
	if os.Getenv(managedDaemonProcessEnv) == "1" {
		if err := runManagedDaemonProcess(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
