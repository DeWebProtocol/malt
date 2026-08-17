package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dewebprotocol/malt-client/bucketsync"
	client "github.com/dewebprotocol/malt-client/transport"
	"github.com/dewebprotocol/malt-client/unixfs"
	cid "github.com/ipfs/go-cid"
	"github.com/spf13/cobra"
)

var bucketCmd = &cobra.Command{
	Use:   "bucket",
	Short: "Manage and synchronize managed Gateway Buckets",
	Long: `Manage the configured Gateway tenant and Bucket.

Pull records the observed remote head as synchronization metadata only; it does
not trust that root. Push first persists the candidate and its original base in
the local workspace, then fetches the latest head and submits the candidate to
the Gateway for fast-forward, automatic merge, or conflict-branch preservation.`,
}

var bucketListCmd = &cobra.Command{
	Use:   "list",
	Short: "List Buckets visible to the configured API key",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		gateway, err := gatewayClient()
		if err != nil {
			return err
		}
		values, err := gateway.ListBuckets(cmd.Context())
		if err != nil {
			return err
		}
		printJSON(map[string]any{"buckets": values})
		return nil
	},
}

var bucketCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a Bucket owned by the configured principal",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		gateway, err := gatewayClient()
		if err != nil {
			return err
		}
		value, err := gateway.CreateBucket(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		printJSON(value)
		return nil
	},
}

var bucketPullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Observe the latest head without overwriting pending local work",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		syncer, err := bucketSyncService()
		if err != nil {
			return err
		}
		workspace, err := syncer.Pull(cmd.Context())
		if err != nil {
			return err
		}
		printJSON(workspace)
		return nil
	},
}

var bucketStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the local Bucket base, observed remote head, and stashes",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		syncer, err := bucketSyncService()
		if err != nil {
			return err
		}
		workspace, err := syncer.Status()
		if err != nil {
			return err
		}
		printJSON(workspace)
		return nil
	},
}

var bucketPushMessage string
var bucketPushChangeSet string

var bucketStageBaseCommit string
var bucketStageBaseRoot string
var bucketStageBaseRevision uint64

var bucketStageCmd = &cobra.Command{
	Use:   "stage <candidate-root>",
	Short: "Bind an externally materialized candidate to its original base",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		candidate, err := cid.Parse(args[0])
		if err != nil {
			return fmt.Errorf("invalid candidate root: %w", err)
		}
		base := bucketsync.Head{
			CommitID: strings.TrimSpace(bucketStageBaseCommit), Root: strings.TrimSpace(bucketStageBaseRoot), Revision: bucketStageBaseRevision,
		}
		if (base.CommitID == "") != (base.Root == "") || (base.CommitID == "") != (base.Revision == 0) {
			return fmt.Errorf("base commit, root, and non-zero revision must be supplied together; omit all three for an empty Bucket")
		}
		syncer, err := bucketSyncService()
		if err != nil {
			return err
		}
		stash, err := syncer.Stage(candidate, base, cid.Undef, "")
		if err != nil {
			return err
		}
		printJSON(stash)
		return nil
	},
}

var bucketPushCmd = &cobra.Command{
	Use:   "push <candidate-root>",
	Short: "Push a previously staged candidate root",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		candidate, err := cid.Parse(args[0])
		if err != nil {
			return fmt.Errorf("invalid candidate root: %w", err)
		}
		changeSet := cid.Undef
		if strings.TrimSpace(bucketPushChangeSet) != "" {
			changeSet, err = cid.Parse(bucketPushChangeSet)
			if err != nil {
				return fmt.Errorf("invalid change-set CID: %w", err)
			}
		}
		syncer, err := bucketSyncService()
		if err != nil {
			return err
		}
		outcome, err := syncer.Push(cmd.Context(), candidate, changeSet, bucketPushMessage)
		if err != nil {
			if errors.Is(err, bucketsync.ErrNotStaged) {
				return fmt.Errorf("%w; run malt add first, or use malt bucket stage for an externally materialized root", err)
			}
			return err
		}
		printJSON(outcome)
		return nil
	},
}

var bucketBranchesCmd = &cobra.Command{
	Use:   "branches",
	Short: "List main, explicit, and Gateway-created conflict branches",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		gateway, err := gatewayClient()
		if err != nil {
			return err
		}
		values, err := gateway.BucketBranches(cmd.Context())
		if err != nil {
			return err
		}
		printJSON(map[string]any{"branches": values})
		return nil
	},
}

var bucketBranchFrom string

var bucketBranchCreateCmd = &cobra.Command{
	Use:   "branch <name>",
	Short: "Create an advanced explicit branch",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		gateway, err := gatewayClient()
		if err != nil {
			return err
		}
		value, err := gateway.CreateBucketBranch(cmd.Context(), args[0], bucketBranchFrom)
		if err != nil {
			return err
		}
		printJSON(value)
		return nil
	},
}

func init() {
	bucketStageCmd.Flags().StringVar(&bucketStageBaseCommit, "base-commit", "", "Original base commit ID")
	bucketStageCmd.Flags().StringVar(&bucketStageBaseRoot, "base-root", "", "Original base root CID")
	bucketStageCmd.Flags().Uint64Var(&bucketStageBaseRevision, "base-revision", 0, "Original base head revision")
	bucketPushCmd.Flags().StringVarP(&bucketPushMessage, "message", "m", "", "Commit message")
	bucketPushCmd.Flags().StringVar(&bucketPushChangeSet, "change-set", "", "Optional Bucket-owned change-set CID")
	bucketBranchCreateCmd.Flags().StringVar(&bucketBranchFrom, "from", "", "Commit ID to branch from (defaults to main)")
	bucketCmd.AddCommand(bucketListCmd, bucketCreateCmd, bucketPullCmd, bucketStatusCmd, bucketStageCmd, bucketPushCmd, bucketBranchesCmd, bucketBranchCreateCmd)
	rootCmd.AddCommand(bucketCmd)
}

func bucketSyncService() (*bucketsync.Service, error) {
	cfg, err := loadRuntimeConfig()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.Gateway.Bucket) == "" {
		return nil, fmt.Errorf("gateway.bucket is not configured")
	}
	options, err := requiredGatewayOptions(cfg, cfg.Gateway.Bucket, "")
	if err != nil {
		return nil, err
	}
	gateway, err := client.New(options)
	if err != nil {
		return nil, err
	}
	return bucketsync.OpenRemote(cfg.Workspace.StatePath, gateway, cfg.Gateway.Bucket)
}

// prepareBucketCandidate captures the Bucket base before candidate
// materialization. A nil service means the runtime is using legacy routes.
func prepareBucketCandidate(
	ctx context.Context,
	remote *client.Client,
	baseRoot cid.Cid,
) (*bucketsync.Service, bucketsync.Head, unixfs.LayoutKind, error) {
	if remote == nil || strings.TrimSpace(remote.SelectedBucket()) == "" {
		return nil, bucketsync.Head{}, unixfs.LayoutHybridV1, nil
	}
	cfg, err := loadRuntimeConfig()
	if err != nil {
		return nil, bucketsync.Head{}, "", err
	}
	selected, err := remote.GetBucket(ctx)
	if err != nil {
		return nil, bucketsync.Head{}, "", err
	}
	layout, err := unixfs.ParseLayoutKind(string(selected.Layout))
	if err != nil {
		return nil, bucketsync.Head{}, "", fmt.Errorf("decode selected Bucket layout: %w", err)
	}
	syncer, err := bucketsync.OpenRemote(cfg.Workspace.StatePath, remote, remote.SelectedBucket())
	if err != nil {
		return nil, bucketsync.Head{}, "", err
	}
	base, err := syncer.CurrentBase(baseRoot)
	if err != nil {
		return nil, bucketsync.Head{}, "", err
	}
	return syncer, base, layout, nil
}
