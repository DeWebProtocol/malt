// Package merkledag replays Git snapshots into IPLD UnixFS Merkle DAGs.
package merkledag

import (
	"context"
	"fmt"
	"io/fs"
	"sort"

	"github.com/dewebprotocol/malt/cmd/eval/helper/replay"
	evalstore "github.com/dewebprotocol/malt/cmd/eval/helper/store"
	"github.com/dewebprotocol/malt/internal/merkledagimport"
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

// Apply imports only the files listed in the trace's live snapshot.
func (a *Adapter) Apply(ctx context.Context, commit replay.CommitMutation) (replay.ApplyResult, error) {
	if a.system == nil {
		return replay.ApplyResult{}, fmt.Errorf("system store is nil")
	}
	if commit.Snapshot == nil {
		return replay.ApplyResult{}, fmt.Errorf("snapshot reader is nil")
	}
	files := append([]replay.LiveFile(nil), commit.LiveFiles...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	importFiles := make([]merkledagimport.File, 0, len(files))
	for _, file := range files {
		data, err := commit.Snapshot.ReadBlob(ctx, file.Hash)
		if err != nil {
			return replay.ApplyResult{}, fmt.Errorf("read blob for %s: %w", file.Path, err)
		}
		importFiles = append(importFiles, merkledagimport.File{
			Path: file.Path,
			Data: data,
			Mode: gitFileMode(file.Mode),
		})
	}
	result, err := merkledagimport.ImportFiles(ctx, a.system.CAS, importFiles, merkledagimport.Options{
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
	a.system.Meter.RecordLogicalBytes(evalstore.CategoryRootHead, len(result.Root))
	return replay.ApplyResult{
		Root:              result.Root,
		AppliedMutations:  len(commit.Mutations),
		MaterializedPaths: len(commit.LiveFiles),
		Accounting:        a.system.Meter.Snapshot(),
	}, nil
}

func gitFileMode(mode string) fs.FileMode {
	if mode == "100755" {
		return 0o755
	}
	return 0o644
}

var _ replay.SystemAdapter = (*Adapter)(nil)
