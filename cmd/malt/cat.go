package main

import (
	"io"
	"os"

	"github.com/dewebprotocol/malt/core/bucketpath"
	"github.com/spf13/cobra"
)

var catBucketID string

func init() {
	rootCmd.AddCommand(catCmd)
	catCmd.Flags().StringVarP(&catBucketID, "bucket", "b", "", "Bucket ID (defaults to client.default_bucket_id)")
}

var catCmd = &cobra.Command{
	Use:   "cat <malt-path>",
	Short: "Stream file content from a bucket path",
	Args:  cobra.ExactArgs(1),
	RunE:  runCat,
}

func runCat(cmd *cobra.Command, args []string) error {
	cfg, err := loadRuntimeConfig()
	if err != nil {
		return err
	}
	bucketID, err := resolveAddBucketID(cfg.Client.DefaultBucketID, catBucketID)
	if err != nil {
		return err
	}
	maltPath := bucketpath.CanonicalizeQueryPath(args[0])

	client := mustDaemonClient()
	body, _, _, err := client.OpenBucketContent(cmd.Context(), bucketID, maltPath, "")
	if err != nil {
		return daemonCommandError(err)
	}
	defer body.Close()

	_, err = io.Copy(os.Stdout, body)
	return err
}
