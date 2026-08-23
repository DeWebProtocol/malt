package main

import (
	"errors"
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
		mountAddCmd.Flag("trust-alias") == nil || mountAddCmd.Flag("encryption-epoch") == nil ||
		mountAddCmd.Flag("write-policy") == nil || mountAddCmd.Flag("layout") == nil {
		t.Fatal("mount add command contract is incomplete")
	}
	for _, command := range []*cobra.Command{mountAddCmd, mountListCmd, unmountCmd} {
		if !command.SilenceUsage || !command.SilenceErrors {
			t.Fatalf("%s must suppress usage and duplicate runtime errors", command.CommandPath())
		}
	}
}

func TestMountSpecWithWriteBackPolicy(t *testing.T) {
	spec, err := mountSpecWithPolicy(
		"docs", "bucket-one", "/mnt/docs", "main", "accepted-docs", 0,
		"write_back", "hybrid-v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	if spec.WritePolicy != filesystemmount.WriteBack || spec.LayoutPolicy != filesystemmount.LayoutHybridV1 ||
		spec.ConflictPolicy != filesystemmount.ConflictPreserveLocal {
		t.Fatalf("write-back mount policies=%#v", spec)
	}
	if _, err := mountSpecWithPolicy("docs", "bucket", "/mnt/docs", "main", "docs", 0, "write_back", ""); !errors.Is(err, filesystemmount.ErrInvalidSpec) {
		t.Fatalf("missing write-back layout error=%v", err)
	}
	if _, err := mountSpecWithPolicy("docs", "bucket", "/mnt/docs", "main", "docs", 0, "read_only", "flat-v1"); err == nil {
		t.Fatal("read-only mount accepted a write layout")
	}
}
