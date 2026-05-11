// Package maltflat replays Git snapshots into the MALT-flat UnixFS layout.
package maltflat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/dewebprotocol/malt/cmd/eval/helper/replay"
	evalstore "github.com/dewebprotocol/malt/cmd/eval/helper/store"
	"github.com/dewebprotocol/malt/core/arctable/overwrite"
	"github.com/dewebprotocol/malt/core/commitment/ipa"
	"github.com/dewebprotocol/malt/core/layout/malt/unixfs"
	"github.com/dewebprotocol/malt/core/structure/list/tree"
	mappingradix "github.com/dewebprotocol/malt/core/structure/mapping/radix"
	cid "github.com/ipfs/go-cid"
)

const defaultNamespace = "eval-maltflat"

// Options configures the MALT-flat adapter.
type Options struct {
	Namespace string
	ChunkSize int
}

// Adapter materializes each commit snapshot as a MALT-flat UnixFS root.
type Adapter struct {
	system *evalstore.System
	layout *unixfs.Layout
}

// New creates a MALT-flat adapter.
func New(system *evalstore.System, opts Options) (*Adapter, error) {
	if system == nil {
		return nil, fmt.Errorf("system store is nil")
	}
	namespace := opts.Namespace
	if namespace == "" {
		namespace = defaultNamespace
	}
	arcs, err := overwrite.NewArcTable(overwrite.WithKVStore(system.StateKV))
	if err != nil {
		return nil, fmt.Errorf("create arctable: %w", err)
	}
	scheme, err := ipa.NewScheme()
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
		Namespace: namespace,
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

// Apply materializes the checked-out live snapshot for this commit. Rebuilding
// from the live trace keeps delete/rename semantics correct even before the
// UnixFS layout exposes a delete primitive.
func (a *Adapter) Apply(ctx context.Context, commit replay.CommitMutation) (replay.ApplyResult, error) {
	if commit.SnapshotRoot == "" {
		return replay.ApplyResult{}, fmt.Errorf("snapshot root is empty")
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
		data, err := os.ReadFile(filepath.Join(commit.SnapshotRoot, filepath.FromSlash(file.Path)))
		if err != nil {
			return replay.ApplyResult{}, fmt.Errorf("read %s: %w", file.Path, err)
		}
		root, err = a.layout.AddFile(ctx, root, file.Path, data)
		if err != nil {
			return replay.ApplyResult{}, fmt.Errorf("materialize %s: %w", file.Path, err)
		}
	}
	if root.Defined() {
		a.system.Meter.RecordLogicalBytes(evalstore.CategoryRootHead, len(root.Bytes()))
	}
	return replay.ApplyResult{
		Root:              root.String(),
		AppliedMutations:  len(commit.Mutations),
		MaterializedPaths: len(files),
		Accounting:        a.system.Meter.Snapshot(),
	}, nil
}

var _ replay.SystemAdapter = (*Adapter)(nil)
