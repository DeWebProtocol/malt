package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	version = "dev"
	cfgFile string
)

var rootCmd = &cobra.Command{
	Use:     "malt",
	Short:   "User-controlled MALT local data runtime",
	Version: version,
	Long: `malt is the user-controlled local data runtime.

It owns accepted roots, local keys and synchronization state, maps UnixFS paths
into canonical MALT segments, verifies resolve/read results locally, and binds
returned payload bytes to authenticated CIDs. Gateway, Peer, CAS, cache, and
remote verification endpoints are never trust authorities.`,
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "local runtime config path")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
