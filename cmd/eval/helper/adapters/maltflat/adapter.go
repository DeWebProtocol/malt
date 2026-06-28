// Package maltflat replays Git snapshots into the MALT-flat UnixFS layout.
package maltflat

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dewebprotocol/malt/auth/commitment"
	"github.com/dewebprotocol/malt/auth/commitment/ipa"
	"github.com/dewebprotocol/malt/auth/commitment/kzg"
	"github.com/dewebprotocol/malt/cmd/eval/helper/replay"
	evalstore "github.com/dewebprotocol/malt/cmd/eval/helper/store"
	"github.com/dewebprotocol/malt/layout/unixfs"
	"github.com/dewebprotocol/malt/runtime/arctable"
	"github.com/dewebprotocol/malt/runtime/arctable/overwrite"
	"github.com/dewebprotocol/malt/runtime/arctable/versioned"
	"github.com/dewebprotocol/malt/runtime/semantic/list/tree"
	mappingradix "github.com/dewebprotocol/malt/runtime/semantic/mapping/radix"
	"github.com/dewebprotocol/malt/storage/kv"
	cid "github.com/ipfs/go-cid"
)

const defaultNamespace = "eval-maltflat"

// ArcTableMode selects the ArcTable persistence strategy used by MALT-flat.
type ArcTableMode string

const (
	ArcTableModeOverwrite ArcTableMode = "overwrite"
	ArcTableModeVersioned ArcTableMode = "versioned"
)

// CommitmentBackend selects the primitive commitment scheme used by map/list semantics.
type CommitmentBackend string

const (
	CommitmentBackendIPA CommitmentBackend = "ipa"
	CommitmentBackendKZG CommitmentBackend = "kzg"
)

const (
	MaterializationStrategyLiveSnapshotRebuild = "live_snapshot_rebuild"
	MaterializationStrategyIncrementalDelta    = "incremental_delta"
)

// Options configures the MALT-flat adapter.
type Options struct {
	Namespace         string
	ChunkSize         int
	ArcTableMode      ArcTableMode
	CommitmentBackend CommitmentBackend
}

// Adapter materializes each commit snapshot as a MALT-flat UnixFS root.
type Adapter struct {
	system *evalstore.System
	layout *unixfs.Layout
	root   cid.Cid
}

// DefaultOptions returns the paper-aligned MALT-flat evaluation configuration.
func DefaultOptions() Options {
	return Options{
		Namespace:         defaultNamespace,
		ChunkSize:         unixfs.DefaultChunkSize,
		ArcTableMode:      ArcTableModeVersioned,
		CommitmentBackend: CommitmentBackendKZG,
	}
}

// New creates a MALT-flat adapter.
func New(system *evalstore.System, opts Options) (*Adapter, error) {
	if system == nil {
		return nil, fmt.Errorf("system store is nil")
	}
	opts = applyDefaults(opts)
	arcs, err := newArcTable(system.StateKV, opts.ArcTableMode)
	if err != nil {
		return nil, fmt.Errorf("create arctable: %w", err)
	}
	scheme, err := newCommitmentBackend(opts.CommitmentBackend)
	if err != nil {
		return nil, fmt.Errorf("create commitment scheme: %w", err)
	}
	maps, err := mappingradix.NewMap(scheme, arcs)
	if err != nil {
		return nil, fmt.Errorf("create map semantics: %w", err)
	}
	lists, err := tree.NewList(scheme, arcs)
	if err != nil {
		return nil, fmt.Errorf("create list semantics: %w", err)
	}
	layout, err := unixfs.New(unixfs.Options{
		Namespace: opts.Namespace,
		ChunkSize: opts.ChunkSize,
		Map:       maps,
		List:      lists,
		Blocks:    system.CAS,
	})
	if err != nil {
		return nil, err
	}
	return &Adapter{system: system, layout: layout}, nil
}

func (a *Adapter) Name() string {
	if a.system != nil && a.system.Name != "" {
		return a.system.Name
	}
	return "maltflat"
}

// Apply incrementally applies the commit mutation set to the previous root.
func (a *Adapter) Apply(ctx context.Context, commit replay.CommitMutation) (replay.ApplyResult, error) {
	if commit.Snapshot == nil {
		return replay.ApplyResult{}, fmt.Errorf("snapshot reader is nil")
	}
	before := a.system.Meter.Snapshot()
	root := a.root
	if !root.Defined() {
		var err error
		root, err = a.layout.EmptyDirectory(ctx)
		if err != nil {
			return replay.ApplyResult{}, err
		}
	}
	materializedPaths := 0
	for _, mutation := range commit.Mutations {
		var err error
		switch mutation.Kind {
		case replay.MutationAdd, replay.MutationModify:
			if mutationHasBlob(mutation) {
				root, err = a.addMutationFile(ctx, root, commit.Snapshot, mutation)
				materializedPaths++
			}
		case replay.MutationDelete:
			root, err = a.removeMutationPath(ctx, root, mutation.Path)
		case replay.MutationRename:
			root, err = a.removeMutationPath(ctx, root, mutation.OldPath)
			if err == nil && mutationHasBlob(mutation) {
				root, err = a.addMutationFile(ctx, root, commit.Snapshot, mutation)
				materializedPaths++
			}
		default:
			if mutationHasBlob(mutation) {
				root, err = a.addMutationFile(ctx, root, commit.Snapshot, mutation)
				materializedPaths++
			}
		}
		if err != nil {
			return replay.ApplyResult{}, fmt.Errorf("apply %s %s: %w", mutation.Kind, mutation.Path, err)
		}
	}
	a.root = root
	a.system.Meter.RecordLogicalBytes(evalstore.CategoryRootHead, len(root.String()))
	after := a.system.Meter.Snapshot()
	return replay.ApplyResult{
		Root:                    root.String(),
		AppliedMutations:        len(commit.Mutations),
		MaterializedPaths:       materializedPaths,
		MaterializationStrategy: MaterializationStrategyIncrementalDelta,
		Accounting:              after,
		AccountingDelta:         evalstore.Delta(after, before),
	}, nil
}

func (a *Adapter) addMutationFile(ctx context.Context, root cid.Cid, snapshot replay.SnapshotReader, mutation replay.FileMutation) (cid.Cid, error) {
	if strings.TrimSpace(mutation.Hash) == "" {
		return cid.Undef, fmt.Errorf("blob hash is empty")
	}
	data, err := snapshot.ReadBlob(ctx, mutation.Hash)
	if err != nil {
		return cid.Undef, fmt.Errorf("read blob: %w", err)
	}
	return a.layout.AddFile(ctx, root, mutation.Path, data)
}

func mutationHasBlob(mutation replay.FileMutation) bool {
	return strings.TrimSpace(mutation.Hash) != ""
}

func (a *Adapter) removeMutationPath(ctx context.Context, root cid.Cid, path string) (cid.Cid, error) {
	nextRoot, err := a.layout.RemovePath(ctx, root, path)
	if err != nil {
		if errors.Is(err, unixfs.ErrNotFound) {
			return root, nil
		}
		return cid.Undef, err
	}
	return nextRoot, nil
}

var _ replay.SystemAdapter = (*Adapter)(nil)

func applyDefaults(opts Options) Options {
	defaults := DefaultOptions()
	if opts.Namespace == "" {
		opts.Namespace = defaults.Namespace
	}
	if opts.ChunkSize == 0 {
		opts.ChunkSize = defaults.ChunkSize
	}
	if opts.ArcTableMode == "" {
		opts.ArcTableMode = defaults.ArcTableMode
	}
	if opts.CommitmentBackend == "" {
		opts.CommitmentBackend = defaults.CommitmentBackend
	}
	return opts
}

func newArcTable(kv kvstore.KVStore, mode ArcTableMode) (arctable.ArcTable, error) {
	switch ArcTableMode(strings.ToLower(string(mode))) {
	case ArcTableModeOverwrite, "simple":
		return overwrite.NewArcTable(overwrite.WithKVStore(kv))
	case ArcTableModeVersioned:
		return versioned.NewArcTable(versioned.WithKVStore(kv))
	default:
		return nil, fmt.Errorf("unknown ArcTable mode %q", mode)
	}
}

func newCommitmentBackend(backend CommitmentBackend) (commitment.IndexCommitment, error) {
	switch CommitmentBackend(strings.ToLower(string(backend))) {
	case CommitmentBackendIPA:
		return ipa.NewScheme()
	case CommitmentBackendKZG:
		return kzg.NewScheme()
	default:
		return nil, fmt.Errorf("unknown commitment backend %q", backend)
	}
}
