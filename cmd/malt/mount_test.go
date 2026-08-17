package main

import (
	"testing"

	filesystemmount "github.com/dewebprotocol/malt-client/filesystem/mount"
	"github.com/spf13/cobra"
)

func TestMountCommandBuildsReadOnlyAcceptedViewSpec(t *testing.T) {
	spec := mountSpec("docs", "bucket-one", "/mnt/docs", "main", "", 0)
	if spec.ID != "docs" || spec.DatasetID != "bucket-one" || spec.Branch != "main" || spec.Mountpoint != "/mnt/docs" || spec.TrustAlias != "docs" {
		t.Fatalf("mount Spec=%#v", spec)
	}
	if spec.CachePolicy != filesystemmount.CacheVerified || spec.WritePolicy != filesystemmount.WriteReadOnly ||
		spec.ConflictPolicy != filesystemmount.ConflictFailReadOnly {
		t.Fatalf("mount policies=%#v", spec)
	}
	override := mountSpec("docs", "bucket-one", "/mnt/docs", "feature", "accepted-docs", 2)
	if override.TrustAlias != "accepted-docs" || override.Branch != "feature" || override.EncryptionEpoch != 2 {
		t.Fatalf("override mount Spec=%#v", override)
	}
}

func TestMountCommandsExposeDaemonControlContract(t *testing.T) {
	if mountAddCmd.Use != "add <id> <dataset-id> <mountpoint>" || mountAddCmd.Flag("branch") == nil ||
		mountAddCmd.Flag("trust-alias") == nil || mountAddCmd.Flag("encryption-epoch") == nil {
		t.Fatal("mount add command contract is incomplete")
	}
	for _, command := range []*cobra.Command{mountAddCmd, mountListCmd, unmountCmd} {
		if !command.SilenceUsage || !command.SilenceErrors {
			t.Fatalf("%s must suppress usage and duplicate runtime errors", command.CommandPath())
		}
	}
}
