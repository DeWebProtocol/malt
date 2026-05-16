// Package maltflat replays Git snapshots into the MALT-flat UnixFS layout.
package maltflat

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/dewebprotocol/malt/cmd/eval/helper/replay"
	evalstore "github.com/dewebprotocol/malt/cmd/eval/helper/store"
	"github.com/dewebprotocol/malt/core/arctable"
	"github.com/dewebprotocol/malt/core/arctable/overwrite"
	"github.com/dewebprotocol/malt/core/arctable/versioned"
	"github.com/dewebprotocol/malt/core/commitment"
	"github.com/dewebprotocol/malt/core/commitment/ipa"
	"github.com/dewebprotocol/malt/core/commitment/kzg"
	"github.com/dewebprotocol/malt/core/kvstore"
	"github.com/dewebprotocol/malt/core/layout/malt/unixfs"
	"github.com/dewebprotocol/malt/core/structure/list/tree"
	mappingradix "github.com/dewebprotocol/malt/core/structure/mapping/radix"
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

const MaterializationStrategyLiveSnapshotRebuild = "live_snapshot_rebuild"

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

// Apply materializes the object-backed live snapshot for this commit.
// Rebuilding from the live trace keeps delete/rename semantics correct even
// before the UnixFS layout exposes a delete primitive.
func (a *Adapter) Apply(ctx context.Context, commit replay.CommitMutation) (replay.ApplyResult, error) {
	if commit.Snapshot == nil {
		return replay.ApplyResult{}, fmt.Errorf("snapshot reader is nil")
	}
	files := append([]replay.LiveFile(nil), commit.LiveFiles...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	root := cid.Undef
	var err error
	if len(files) == 0 {
		root, err = a.layout.EmptyDirectory(ctx)
		if err != nil {
			return replay.ApplyResult{}, err
		}
	}
	for _, file := range files {
		data, err := commit.Snapshot.ReadBlob(ctx, file.Hash)
		if err != nil {
			return replay.ApplyResult{}, fmt.Errorf("read blob for %s: %w", file.Path, err)
		}
		root, err = a.layout.AddFile(ctx, root, file.Path, data)
		if err != nil {
			return replay.ApplyResult{}, fmt.Errorf("materialize %s: %w", file.Path, err)
		}
	}
	if root.Defined() {
		a.system.Meter.RecordLogicalBytes(evalstore.CategoryRootHead, len(root.String()))
	}
	return replay.ApplyResult{
		Root:                    root.String(),
		AppliedMutations:        len(commit.Mutations),
		MaterializedPaths:       len(files),
		MaterializationStrategy: MaterializationStrategyLiveSnapshotRebuild,
		Accounting:              a.system.Meter.Snapshot(),
	}, nil
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
