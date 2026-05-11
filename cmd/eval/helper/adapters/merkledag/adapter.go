// Package merkledag replays Git snapshots into IPLD UnixFS Merkle DAGs.
package merkledag

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

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
	if commit.SnapshotRoot == "" {
		return replay.ApplyResult{}, fmt.Errorf("snapshot root is empty")
	}
	result, err := merkledagimport.ImportPath(ctx, a.system.CAS, commit.SnapshotRoot, merkledagimport.Options{
		Model:       merkledagimport.ModelUnixFS,
		FileLayout:  a.opts.FileLayout,
		DirLayout:   a.opts.DirLayout,
		ChunkSize:   a.opts.ChunkSize,
		HAMTFanout:  a.opts.HAMTFanout,
		RawFileLeaf: a.opts.RawFileLeaf,
		Ignore:      newLivePathFilter(commit.SnapshotRoot, commit.LiveFiles),
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

type livePathFilter struct {
	root  string
	files map[string]struct{}
	dirs  map[string]struct{}
}

func newLivePathFilter(root string, files []replay.LiveFile) *livePathFilter {
	filter := &livePathFilter{
		root:  root,
		files: make(map[string]struct{}, len(files)),
		dirs:  make(map[string]struct{}),
	}
	for _, file := range files {
		clean := strings.Trim(filepath.ToSlash(file.Path), "/")
		if clean == "" {
			continue
		}
		filter.files[clean] = struct{}{}
		dir := filepath.ToSlash(filepath.Dir(clean))
		for dir != "." && dir != "" {
			filter.dirs[dir] = struct{}{}
			next := filepath.ToSlash(filepath.Dir(dir))
			if next == dir {
				break
			}
			dir = next
		}
	}
	return filter
}

func (f *livePathFilter) LoadDirectoryRules(string) error {
	return nil
}

func (f *livePathFilter) Ignored(localPath string, isDir bool) (bool, error) {
	rel, err := filepath.Rel(f.root, localPath)
	if err != nil {
		return false, err
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return false, nil
	}
	if rel == ".git" || strings.HasPrefix(rel, ".git/") {
		return true, nil
	}
	if isDir {
		_, ok := f.dirs[rel]
		return !ok, nil
	}
	_, ok := f.files[rel]
	return !ok, nil
}

var _ replay.SystemAdapter = (*Adapter)(nil)
