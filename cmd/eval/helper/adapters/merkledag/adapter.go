// Package merkledag replays Git snapshots into IPLD UnixFS Merkle DAGs.
package merkledag

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/dewebprotocol/malt/cmd/eval/helper/replay"
	evalstore "github.com/dewebprotocol/malt/cmd/eval/helper/store"
	"github.com/dewebprotocol/malt/cmd/internal/merkledagimport"
)

// Options configures a Merkle DAG baseline adapter.
type Options struct {
	Name        string
	FileLayout  string
	DirLayout   string
	ChunkSize   int
	HAMTFanout  int
	RawFileLeaf bool
}

// Adapter materializes each commit snapshot as an IPLD UnixFS DAG.
type Adapter struct {
	system *evalstore.System
	opts   Options
	editor *merkledagimport.Editor
}

// New creates a Merkle DAG adapter.
func New(system *evalstore.System, opts Options) *Adapter {
	if opts.Name == "" {
		opts.Name = "merkledag"
	}
	if opts.DirLayout == "" {
		opts.DirLayout = merkledagimport.DirLayoutBasic
	}
	return &Adapter{system: system, opts: opts}
}

func (a *Adapter) Name() string {
	if a.opts.Name != "" {
		return a.opts.Name
	}
	return "merkledag"
}

// Apply incrementally applies only the commit mutation set.
func (a *Adapter) Apply(ctx context.Context, commit replay.CommitMutation) (replay.ApplyResult, error) {
	if a.system == nil {
		return replay.ApplyResult{}, fmt.Errorf("system store is nil")
	}
	if commit.Snapshot == nil {
		return replay.ApplyResult{}, fmt.Errorf("snapshot reader is nil")
	}
	if a.editor == nil {
		editor, err := merkledagimport.NewEditor(a.system.CAS, merkledagimport.Options{
			Model:       merkledagimport.ModelUnixFS,
			FileLayout:  a.opts.FileLayout,
			DirLayout:   a.opts.DirLayout,
			ChunkSize:   a.opts.ChunkSize,
			HAMTFanout:  a.opts.HAMTFanout,
			RawFileLeaf: a.opts.RawFileLeaf,
		})
		if err != nil {
			return replay.ApplyResult{}, err
		}
		a.editor = editor
	}
	before := a.system.Meter.Snapshot()
	materializedPaths := 0
	for _, mutation := range commit.Mutations {
		var err error
		switch mutation.Kind {
		case replay.MutationAdd, replay.MutationModify:
			if mutationHasBlob(mutation) {
				err = a.putMutationFile(ctx, commit.Snapshot, mutation)
				materializedPaths++
			}
		case replay.MutationDelete:
			err = a.removeMutationPath(ctx, mutation.Path)
		case replay.MutationRename:
			err = a.removeMutationPath(ctx, mutation.OldPath)
			if err == nil && mutationHasBlob(mutation) {
				err = a.putMutationFile(ctx, commit.Snapshot, mutation)
				materializedPaths++
			}
		default:
			if mutationHasBlob(mutation) {
				err = a.putMutationFile(ctx, commit.Snapshot, mutation)
				materializedPaths++
			}
		}
		if err != nil {
			return replay.ApplyResult{}, fmt.Errorf("apply %s %s: %w", mutation.Kind, mutation.Path, err)
		}
	}
	root := a.editor.Root()
	a.system.Meter.RecordLogicalBytes(evalstore.CategoryRootHead, len(root))
	after := a.system.Meter.Snapshot()
	return replay.ApplyResult{
		Root:                    root,
		AppliedMutations:        len(commit.Mutations),
		MaterializedPaths:       materializedPaths,
		MaterializationStrategy: "incremental_delta",
		Accounting:              after,
		AccountingDelta:         evalstore.Delta(after, before),
	}, nil
}

func (a *Adapter) putMutationFile(ctx context.Context, snapshot replay.SnapshotReader, mutation replay.FileMutation) error {
	if strings.TrimSpace(mutation.Hash) == "" {
		return fmt.Errorf("blob hash is empty")
	}
	data, err := snapshot.ReadBlob(ctx, mutation.Hash)
	if err != nil {
		return fmt.Errorf("read blob: %w", err)
	}
	return a.editor.PutFile(ctx, mutation.Path, data, gitFileMode(mutation.Mode))
}

func mutationHasBlob(mutation replay.FileMutation) bool {
	return strings.TrimSpace(mutation.Hash) != ""
}

func (a *Adapter) removeMutationPath(ctx context.Context, path string) error {
	if err := a.editor.RemoveFile(ctx, path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return nil
}

func gitFileMode(mode string) fs.FileMode {
	if mode == "100755" {
		return 0o755
	}
	return 0o644
}

var _ replay.SystemAdapter = (*Adapter)(nil)
