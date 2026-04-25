package main

import "github.com/spf13/cobra"

func init() {
	rootCmd.AddCommand(bucketCmd)
}

var bucketCmd = &cobra.Command{
	Use:   "bucket",
	Short: "Manage buckets and client-side defaults",
}
