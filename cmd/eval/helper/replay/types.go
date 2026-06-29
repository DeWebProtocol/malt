// Package replay defines write-amplification replay contracts and JSONL output.
package replay

import (
	"context"

	evalstore "github.com/dewebprotocol/malt/cmd/eval/helper/store"
)

// MutationKind identifies a source-domain file mutation.
type MutationKind string

const (
	MutationAdd    MutationKind = "add"
	MutationModify MutationKind = "modify"
	MutationDelete MutationKind = "delete"
	MutationRename MutationKind = "rename"
)

// FileMutation is one logical file mutation extracted from a Git commit.
type FileMutation struct {
	Kind           MutationKind `json:"kind"`
	Path           string       `json:"path"`
	OldPath        string       `json:"old_path,omitempty"`
	ContentChanged bool         `json:"content_changed,omitempty"`
	Mode           string       `json:"mode,omitempty"`
	Size           int64        `json:"size,omitempty"`
	Hash           string       `json:"hash,omitempty"`
}

// LiveFile describes a regular file present in the commit snapshot.
type LiveFile struct {
	Path string `json:"path"`
	Mode string `json:"mode,omitempty"`
	Size int64  `json:"size"`
	Hash string `json:"hash,omitempty"`
}

// LiveStats describes source-domain live scale after a commit.
type LiveStats struct {
	FileCount        int     `json:"file_count"`
	DirectoryCount   int     `json:"directory_count"`
	PathCount        int     `json:"path_count"`
	LivePayloadBytes int64   `json:"live_payload_bytes"`
	MaxPathDepth     int     `json:"max_path_depth"`
	AveragePathDepth float64 `json:"average_path_depth"`
}

// SkipStats records source-domain entries omitted by the evaluator.
type SkipStats struct {
	SymlinkCount int `json:"symlink_count,omitempty"`
	OtherCount   int `json:"other_count,omitempty"`
}

// SnapshotReader reads object bytes for files in one immutable source snapshot.
type SnapshotReader interface {
	ReadBlob(ctx context.Context, hash string) ([]byte, error)
}

// CommitMutation is the stable source-domain input shared by all systems.
type CommitMutation struct {
	Repo      string         `json:"repo"`
	Commit    string         `json:"commit"`
	Parent    string         `json:"parent,omitempty"`
	Index     int            `json:"index"`
	Snapshot  SnapshotReader `json:"-"`
	Mutations []FileMutation `json:"mutations"`
	LiveStats LiveStats      `json:"live_stats"`
	LiveFiles []LiveFile     `json:"live_files,omitempty"`
	Skipped   SkipStats      `json:"skipped,omitempty"`
}

// ApplyResult is returned by one evaluated system after applying a commit.
type ApplyResult struct {
	Root                    string             `json:"root,omitempty"`
	AppliedMutations        int                `json:"applied_mutations"`
	MaterializedPaths       int                `json:"materialized_paths"`
	MaterializationStrategy string             `json:"materialization_strategy,omitempty"`
	Accounting              evalstore.Snapshot `json:"accounting"`
	AccountingDelta         evalstore.Snapshot `json:"accounting_delta"`
}

// ResultRecord is one JSONL measurement point for one system at one commit.
type ResultRecord struct {
	Repo                       string             `json:"repo"`
	System                     string             `json:"system"`
	Commit                     string             `json:"commit"`
	Parent                     string             `json:"parent,omitempty"`
	Index                      int                `json:"index"`
	LiveStats                  LiveStats          `json:"live_stats"`
	MutationSet                []FileMutation     `json:"mutations"`
	Skipped                    SkipStats          `json:"skipped,omitempty"`
	Result                     ApplyResult        `json:"result"`
	Accounting                 evalstore.Snapshot `json:"accounting"`
	AccountingDelta            evalstore.Snapshot `json:"accounting_delta"`
	LogicalChangedPayloadBytes int64              `json:"logical_changed_payload_bytes"`
	PhysicalPersistedBytes     uint64             `json:"physical_persisted_bytes"`
	PhysicalPayloadBytes       uint64             `json:"physical_payload_bytes"`
	PhysicalMetadataBytes      uint64             `json:"physical_metadata_bytes"`
	WriteAmplification         *float64           `json:"write_amplification,omitempty"`
}

// SystemAdapter consumes source-domain commit mutations for one representation.
type SystemAdapter interface {
	Name() string
	Apply(context.Context, CommitMutation) (ApplyResult, error)
}
