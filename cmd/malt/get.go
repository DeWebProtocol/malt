package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"

	daemonclient "github.com/dewebprotocol/malt/client"
	"github.com/dewebprotocol/malt/core/bucketpath"
	"github.com/dewebprotocol/malt/core/manifest"
	"github.com/dewebprotocol/malt/httpapi"
	cid "github.com/ipfs/go-cid"
	"github.com/spf13/cobra"
)

var getBucketID string

func init() {
	rootCmd.AddCommand(getCmd)
	getCmd.Flags().StringVarP(&getBucketID, "bucket", "b", "", "Bucket ID (defaults to client.default_bucket_id)")
}

var getCmd = &cobra.Command{
	Use:   "get <malt-path> [local-output]",
	Short: "Export a file or directory from a bucket path",
	Args:  cobra.RangeArgs(1, 2),
	RunE:  runGet,
}

func runGet(cmd *cobra.Command, args []string) error {
	cfg, err := loadRuntimeConfig()
	if err != nil {
		return err
	}
	bucketID, err := resolveAddBucketID(cfg.Client.DefaultBucketID, getBucketID)
	if err != nil {
		return err
	}

	maltPath := bucketpath.CanonicalizeQueryPath(args[0])
	localOutput := ""
	if len(args) > 1 {
		localOutput = args[1]
	}

	client := mustDaemonClient()
	stat, err := client.StatBucketPath(cmd.Context(), bucketID, maltPath)
	if err != nil {
		return daemonCommandError(err)
	}

	dest, err := resolveGetOutputPath(maltPath, stat.Kind, localOutput)
	if err != nil {
		return err
	}

	if stat.Kind == "file" {
		if err := writeBucketFile(cmd.Context(), client, bucketID, maltPath, dest); err != nil {
			return daemonCommandError(err)
		}
		return nil
	}
	if stat.Kind == "dir" {
		casClient, err := makeCASClient()
		if err != nil {
			return err
		}
		if err := exportBucketDirectory(cmd.Context(), client, casClient, bucketID, maltPath, dest, stat); err != nil {
			return daemonCommandError(err)
		}
		return nil
	}

	return fmt.Errorf("unsupported path kind %q", stat.Kind)
}

func resolveGetOutputPath(maltPath string, kind string, explicitOutput string) (string, error) {
	if explicitOutput != "" {
		return explicitOutput, nil
	}
	if maltPath == "" {
		return "", fmt.Errorf("bucket root requires explicit local-output")
	}
	base := path.Base(maltPath)
	if base == "" || base == "." || base == "/" {
		return "", fmt.Errorf("cannot infer output path for %q", maltPath)
	}
	return filepath.Join(".", base), nil
}

func exportBucketDirectory(ctx context.Context, client *daemonclient.Client, casClient addCASClient, bucketID string, bucketPath string, localDir string, rootStat *httpapi.BucketStatResponse) error {
	if rootStat == nil {
		stat, err := client.StatBucketPath(ctx, bucketID, bucketPath)
		if err != nil {
			return err
		}
		rootStat = stat
	}
	if rootStat.Kind != "dir" {
		return fmt.Errorf("path %q is not a directory", bucketPath)
	}
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		return fmt.Errorf("create directory %s: %w", localDir, err)
	}

	entries, err := directoryEntriesFromStatPayload(ctx, casClient, rootStat)
	if err != nil {
		return err
	}
	for _, child := range entries {
		childBucketPath := child
		if bucketPath != "" {
			childBucketPath = path.Join(bucketPath, child)
		}
		childLocalPath := filepath.Join(localDir, child)

		childStat, err := client.StatBucketPath(ctx, bucketID, childBucketPath)
		if err != nil {
			return err
		}
		switch childStat.Kind {
		case "file":
			if err := writeBucketFile(ctx, client, bucketID, childBucketPath, childLocalPath); err != nil {
				return err
			}
		case "dir":
			if err := exportBucketDirectory(ctx, client, casClient, bucketID, childBucketPath, childLocalPath, childStat); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported kind %q at %q", childStat.Kind, childBucketPath)
		}
	}
	return nil
}

func directoryEntriesFromStatPayload(ctx context.Context, casClient addCASClient, stat *httpapi.BucketStatResponse) ([]string, error) {
	if stat == nil || stat.Kind != "dir" {
		return nil, fmt.Errorf("directory stat is required")
	}
	if stat.Entries != nil {
		return stat.Entries, nil
	}
	if stat.Payload == "" {
		return []string{}, nil
	}
	payloadCID, err := cid.Decode(stat.Payload)
	if err != nil {
		return nil, fmt.Errorf("decode payload cid: %w", err)
	}
	data, err := casClient.Get(ctx, payloadCID)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest payload: %w", err)
	}
	m, err := manifest.ParseDirectoryJSON(data)
	if err != nil {
		return nil, fmt.Errorf("parse directory manifest: %w", err)
	}
	return m.Entries, nil
}

func writeBucketFile(ctx context.Context, client *daemonclient.Client, bucketID string, bucketPath string, localFile string) error {
	body, _, _, err := client.OpenBucketContent(ctx, bucketID, bucketPath, "")
	if err != nil {
		return err
	}
	defer body.Close()

	parent := filepath.Dir(localFile)
	if parent != "." && parent != "" {
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return fmt.Errorf("create parent directory %s: %w", parent, err)
		}
	}

	tmp := localFile + ".malt-tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create temp file %s: %w", tmp, err)
	}
	if _, err := io.Copy(f, body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write %s: %w", localFile, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close %s: %w", tmp, err)
	}

	_ = os.Remove(localFile)
	if err := os.Rename(tmp, localFile); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s -> %s: %w", tmp, localFile, err)
	}
	return nil
}
