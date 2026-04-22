package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var bucketBackend string

func init() {
	bucketCmd.AddCommand(bucketCreateCmd, bucketDeleteCmd, bucketListCmd, bucketFreezeCmd, bucketGetCmd)
	bucketCreateCmd.Flags().StringVar(&bucketBackend, "backend", "", "backend type (default: daemon config)")
}

var bucketCreateCmd = &cobra.Command{
	Use:   "create <id>",
	Short: "Create a new bucket",
	Args:  cobra.ExactArgs(1),
	RunE:  runBucketCreate,
}

func runBucketCreate(cmd *cobra.Command, args []string) error {
	client := mustDaemonClient()
	meta, err := client.CreateBucket(cmd.Context(), args[0], bucketBackend)
	if err != nil {
		return daemonCommandError(err)
	}
	printJSON(meta)
	return nil
}

var bucketGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Get bucket metadata",
	Args:  cobra.ExactArgs(1),
	RunE:  runBucketGet,
}

func runBucketGet(cmd *cobra.Command, args []string) error {
	client := mustDaemonClient()
	meta, err := client.GetBucket(cmd.Context(), args[0])
	if err != nil {
		return daemonCommandError(err)
	}
	printJSON(meta)
	return nil
}

var bucketDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a bucket",
	Args:  cobra.ExactArgs(1),
	RunE:  runBucketDelete,
}

func runBucketDelete(cmd *cobra.Command, args []string) error {
	client := mustDaemonClient()
	if err := client.DeleteBucket(cmd.Context(), args[0]); err != nil {
		return daemonCommandError(err)
	}
	fmt.Fprintf(os.Stdout, "bucket %q deleted\n", args[0])
	return nil
}

var bucketListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all buckets",
	RunE:  runBucketList,
}

func runBucketList(cmd *cobra.Command, args []string) error {
	client := mustDaemonClient()
	buckets, err := client.ListBuckets(cmd.Context())
	if err != nil {
		return daemonCommandError(err)
	}

	if len(buckets) == 0 {
		fmt.Println("no buckets found")
		return nil
	}
	for _, b := range buckets {
		fmt.Printf("%s  root=%s  state=%s  arcs=%d  backend=%s\n", b.ID, b.Root, b.State, b.ArcCount, b.Backend)
	}
	return nil
}

var bucketFreezeCmd = &cobra.Command{
	Use:   "freeze <id>",
	Short: "Freeze a bucket",
	Args:  cobra.ExactArgs(1),
	RunE:  runBucketFreeze,
}

func runBucketFreeze(cmd *cobra.Command, args []string) error {
	client := mustDaemonClient()
	if err := client.FreezeBucket(cmd.Context(), args[0]); err != nil {
		return daemonCommandError(err)
	}
	fmt.Fprintf(os.Stdout, "bucket %q frozen\n", args[0])
	return nil
}

