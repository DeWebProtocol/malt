package main

import (
	"io"
	"os"

	"github.com/dewebprotocol/malt/core/querypath"
	"github.com/spf13/cobra"
)

var catRoot string

func init() {
	rootCmd.AddCommand(catCmd)
	catCmd.Flags().StringVar(&catRoot, "root", "", "Root CID to read from")
}

var catCmd = &cobra.Command{
	Use:   "cat <malt-path>",
	Short: "Stream file content from a root",
	Args:  cobra.ExactArgs(1),
	RunE:  runCat,
}

func runCat(cmd *cobra.Command, args []string) error {
	maltPath := querypath.CanonicalizeQueryPath(args[0])

	client := mustDaemonClient()
	body, _, _, err := client.Content(cmd.Context(), catRoot, maltPath, "")
	if err != nil {
		return daemonCommandError(err)
	}
	defer body.Close()

	_, err = io.Copy(os.Stdout, body)
	return err
}
