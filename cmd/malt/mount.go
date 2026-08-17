package main

import (
	"fmt"

	filesystemmount "github.com/dewebprotocol/malt-client/filesystem/mount"
	clientdaemon "github.com/dewebprotocol/malt-client/internal/daemon"
	"github.com/dewebprotocol/malt-client/localapi"
	"github.com/spf13/cobra"
)

var (
	mountBranch          string
	mountTrustAlias      string
	mountEncryptionEpoch uint32
)

var mountCmd = &cobra.Command{
	Use:   "mount",
	Short: "Manage authenticated remote-backed filesystem mounts",
}

var mountAddCmd = &cobra.Command{
	Use:           "add <id> <dataset-id> <mountpoint>",
	Short:         "Mount an accepted remote Bucket view read-only",
	Args:          cobra.ExactArgs(3),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		spec := mountSpec(args[0], args[1], args[2], mountBranch, mountTrustAlias, mountEncryptionEpoch)
		client, closeTransport, err := configuredMountClient()
		if err != nil {
			return err
		}
		defer closeTransport()
		status, err := client.Mount(cmd.Context(), spec)
		if err != nil {
			return fmt.Errorf("mount through MALT daemon: %w", err)
		}
		printJSON(status)
		return nil
	},
}

var mountListCmd = &cobra.Command{
	Use:           "list",
	Short:         "List desired and active filesystem mounts",
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, closeTransport, err := configuredMountClient()
		if err != nil {
			return err
		}
		defer closeTransport()
		mounts, err := client.ListMounts(cmd.Context())
		if err != nil {
			return fmt.Errorf("list mounts through MALT daemon: %w", err)
		}
		printJSON(map[string]any{"mounts": mounts})
		return nil
	},
}

var unmountCmd = &cobra.Command{
	Use:           "unmount <id>",
	Short:         "Unmount and remove a durable filesystem binding",
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, closeTransport, err := configuredMountClient()
		if err != nil {
			return err
		}
		defer closeTransport()
		if err := client.Unmount(cmd.Context(), args[0]); err != nil {
			return fmt.Errorf("unmount through MALT daemon: %w", err)
		}
		return nil
	},
}

func mountSpec(id, datasetID, mountpoint, branch, trustAlias string, encryptionEpoch uint32) filesystemmount.Spec {
	if trustAlias == "" {
		trustAlias = id
	}
	return filesystemmount.Spec{
		ID: id, DatasetID: datasetID, Branch: branch, Mountpoint: mountpoint,
		TrustAlias: trustAlias, CachePolicy: filesystemmount.CacheVerified,
		WritePolicy: filesystemmount.WriteReadOnly, EncryptionEpoch: encryptionEpoch,
		ConflictPolicy: filesystemmount.ConflictFailReadOnly,
	}
}

func configuredMountClient() (*localapi.Client, func(), error) {
	cfg, err := loadRuntimeConfig()
	if err != nil {
		return nil, nil, err
	}
	if err := clientdaemon.ValidateSocketPath(cfg.Daemon.SocketPath); err != nil {
		return nil, nil, err
	}
	httpClient, transport := daemonHTTPClient(cfg.Daemon.SocketPath)
	httpClient.Timeout = 0
	client, err := localapi.New(localapi.Options{HTTPClient: httpClient})
	if err != nil {
		transport.CloseIdleConnections()
		return nil, nil, err
	}
	return client, transport.CloseIdleConnections, nil
}

func init() {
	mountAddCmd.Flags().StringVar(&mountBranch, "branch", "main", "remote dataset branch")
	mountAddCmd.Flags().StringVar(&mountTrustAlias, "trust-alias", "", "local accepted-root alias (defaults to mount id)")
	mountAddCmd.Flags().Uint32Var(&mountEncryptionEpoch, "encryption-epoch", 0, "local encryption epoch (nonzero requires decryption support)")
	mountCmd.AddCommand(mountAddCmd, mountListCmd)
	rootCmd.AddCommand(mountCmd, unmountCmd)
}
