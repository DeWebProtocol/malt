// Package maltflat replays Git mutations into a flat MALT path-to-blob map.
package maltflat

import (
	"context"
	"fmt"
	"strings"

	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/auth/commitment"
	"github.com/dewebprotocol/malt/auth/commitment/ipa"
	"github.com/dewebprotocol/malt/auth/commitment/kzg"
	semanticmapping "github.com/dewebprotocol/malt/auth/semantic/mapping"
	"github.com/dewebprotocol/malt/cmd/eval/helper/replay"
	evalstore "github.com/dewebprotocol/malt/cmd/eval/helper/store"
	"github.com/dewebprotocol/malt/layout/unixfs"
	"github.com/dewebprotocol/malt/runtime/arctable"
	"github.com/dewebprotocol/malt/runtime/arctable/overwrite"
	"github.com/dewebprotocol/malt/runtime/arctable/versioned"
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

// Adapter materializes each commit mutation set as a MALT-flat map root.
type Adapter struct {
	system    *evalstore.System
	maps      *mappingradix.Map
	namespace string
	root      cid.Cid
	entries   map[arcset.Path]cid.Cid
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
	return &Adapter{
		system:    system,
		maps:      maps,
		namespace: opts.Namespace,
		entries:   make(map[arcset.Path]cid.Cid),
	}, nil
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
		root, err = a.maps.Commit(ctx, a.namespace, semanticmapping.NewViewFromPaths(nil))
		if err != nil {
			return replay.ApplyResult{}, err
		}
	}
	nextEntries := cloneEntries(a.entries)
	updates := make([]semanticmapping.BatchUpdate, 0, len(commit.Mutations))
	materializedPaths := 0
	for _, mutation := range commit.Mutations {
		var err error
		switch mutation.Kind {
		case replay.MutationAdd, replay.MutationModify:
			if mutationHasBlob(mutation) {
				err = a.queuePutMutationFile(ctx, commit.Snapshot, nextEntries, &updates, mutation)
				materializedPaths++
			}
		case replay.MutationDelete:
			err = a.queueRemoveMutationPath(nextEntries, &updates, mutation.Path)
		case replay.MutationRename:
			err = a.queueRemoveMutationPath(nextEntries, &updates, mutation.OldPath)
			if err == nil && mutationHasBlob(mutation) {
				err = a.queuePutMutationFile(ctx, commit.Snapshot, nextEntries, &updates, mutation)
				materializedPaths++
			}
		default:
			if mutationHasBlob(mutation) {
				err = a.queuePutMutationFile(ctx, commit.Snapshot, nextEntries, &updates, mutation)
				materializedPaths++
			}
		}
		if err != nil {
			return replay.ApplyResult{}, fmt.Errorf("apply %s %s: %w", mutation.Kind, mutation.Path, err)
		}
	}
	if len(updates) > 0 {
		var err error
		root, err = a.maps.BatchUpdate(ctx, a.namespace, root, updates)
		if err != nil {
			return replay.ApplyResult{}, err
		}
	}
	a.root = root
	a.entries = nextEntries
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

func (a *Adapter) queuePutMutationFile(ctx context.Context, snapshot replay.SnapshotReader, entries map[arcset.Path]cid.Cid, updates *[]semanticmapping.BatchUpdate, mutation replay.FileMutation) error {
	if strings.TrimSpace(mutation.Hash) == "" {
		return fmt.Errorf("blob hash is empty")
	}
	key, err := arcset.NewPath(mutation.Path)
	if err != nil {
		return err
	}
	data, err := snapshot.ReadBlob(ctx, mutation.Hash)
	if err != nil {
		return fmt.Errorf("read blob: %w", err)
	}
	target, err := a.system.CAS.Put(ctx, data)
	if err != nil {
		return err
	}
	oldValue := entries[key]
	if sameCID(oldValue, target) {
		return nil
	}
	*updates = append(*updates, semanticmapping.BatchUpdate{
		Key:      key,
		OldValue: oldValue,
		NewValue: target,
	})
	entries[key] = target
	return nil
}

func mutationHasBlob(mutation replay.FileMutation) bool {
	return strings.TrimSpace(mutation.Hash) != ""
}

func (a *Adapter) queueRemoveMutationPath(entries map[arcset.Path]cid.Cid, updates *[]semanticmapping.BatchUpdate, path string) error {
	key, err := arcset.NewPath(path)
	if err != nil {
		return err
	}
	oldValue := entries[key]
	if !oldValue.Defined() {
		return nil
	}
	*updates = append(*updates, semanticmapping.BatchUpdate{
		Key:      key,
		OldValue: oldValue,
		NewValue: cid.Undef,
	})
	delete(entries, key)
	return nil
}

var _ replay.SystemAdapter = (*Adapter)(nil)

func cloneEntries(entries map[arcset.Path]cid.Cid) map[arcset.Path]cid.Cid {
	out := make(map[arcset.Path]cid.Cid, len(entries))
	for key, value := range entries {
		out[key] = value
	}
	return out
}

func sameCID(a, b cid.Cid) bool {
	if !a.Defined() && !b.Defined() {
		return true
	}
	return a.Equals(b)
}

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
