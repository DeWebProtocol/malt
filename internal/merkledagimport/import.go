// Package merkledagimport imports local UnixFS data into an IPFS-style Merkle DAG.
package merkledagimport

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	chunker "github.com/ipfs/boxo/chunker"
	merkledag "github.com/ipfs/boxo/ipld/merkledag"
	balanced "github.com/ipfs/boxo/ipld/unixfs/importer/balanced"
	helpers "github.com/ipfs/boxo/ipld/unixfs/importer/helpers"
	unixfsio "github.com/ipfs/boxo/ipld/unixfs/io"
	blocks "github.com/ipfs/go-block-format"
	cid "github.com/ipfs/go-cid"
	ipld "github.com/ipfs/go-ipld-format"
	mh "github.com/multiformats/go-multihash"
)

const (
	ModelUnixFS = "unixfs"

	LayoutBalanced = "balanced"
	LayoutHAMT     = "hamt"
)

const defaultChunkSize = 262144

// Options controls how local data is materialized as a Merkle DAG.
type Options struct {
	Model       string
	Layout      string
	ChunkSize   int
	HAMTFanout  int
	RawFileLeaf bool
}

// Result describes the imported Merkle DAG root and local input stats.
type Result struct {
	Root  string
	Files int
	Bytes int64
}

// Store is the minimal CAS surface needed by the DAGService adapter.
type Store interface {
	PutWithCodec(ctx context.Context, data []byte, codec uint64) (cid.Cid, error)
	Get(ctx context.Context, c cid.Cid) ([]byte, error)
}

// ImportPath imports localPath into the supplied CAS using Boxo's UnixFS DAG
// builders and returns the Merkle DAG root CID.
func ImportPath(ctx context.Context, store Store, localPath string, opts Options) (*Result, error) {
	opts = normalizeOptions(opts)
	if opts.Model != ModelUnixFS {
		return nil, fmt.Errorf("unsupported merkle-dag model %q", opts.Model)
	}
	if opts.Layout != LayoutBalanced && opts.Layout != LayoutHAMT {
		return nil, fmt.Errorf("unsupported merkle-dag unixfs layout %q", opts.Layout)
	}

	abs, err := filepath.Abs(localPath)
	if err != nil {
		return nil, fmt.Errorf("resolve path %q: %w", localPath, err)
	}
	importer := &pathImporter{
		dag:   &casDAGService{store: store},
		opts:  opts,
		seen:  make(map[string]struct{}),
		build: cid.Prefix{Version: 1, Codec: cid.DagProtobuf, MhType: mh.SHA2_256, MhLength: -1},
	}
	root, files, bytesUploaded, err := importer.importPath(ctx, abs)
	if err != nil {
		return nil, err
	}
	return &Result{
		Root:  root.Cid().String(),
		Files: files,
		Bytes: bytesUploaded,
	}, nil
}

func normalizeOptions(opts Options) Options {
	if opts.Model == "" {
		opts.Model = ModelUnixFS
	}
	if opts.Layout == "" {
		opts.Layout = LayoutHAMT
	}
	if opts.ChunkSize <= 0 {
		opts.ChunkSize = defaultChunkSize
	}
	if opts.HAMTFanout <= 0 {
		opts.HAMTFanout = unixfsio.DefaultShardWidth
	}
	return opts
}

type pathImporter struct {
	dag   ipld.DAGService
	opts  Options
	seen  map[string]struct{}
	build cid.Prefix
}

func (i *pathImporter) importPath(ctx context.Context, localPath string) (ipld.Node, int, int64, error) {
	info, err := os.Stat(localPath)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("stat %s: %w", localPath, err)
	}
	if info.IsDir() {
		return i.importDirectory(ctx, localPath)
	}
	if !info.Mode().IsRegular() {
		return nil, 0, 0, fmt.Errorf("only regular files and directories are supported: %s", localPath)
	}
	root, err := i.importFile(ctx, localPath, info)
	if err != nil {
		return nil, 0, 0, err
	}
	return root, 1, info.Size(), nil
}

func (i *pathImporter) importFile(ctx context.Context, localPath string, info fs.FileInfo) (ipld.Node, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", localPath, err)
	}
	defer f.Close()

	dbp := helpers.DagBuilderParams{
		Dagserv:     i.dag,
		Maxlinks:    helpers.DefaultLinksPerBlock,
		RawLeaves:   i.opts.RawFileLeaf,
		CidBuilder:  i.build,
		FileMode:    info.Mode(),
		FileModTime: info.ModTime(),
	}
	db, err := dbp.New(chunker.NewSizeSplitter(f, int64(i.opts.ChunkSize)))
	if err != nil {
		return nil, fmt.Errorf("create unixfs dag builder for %s: %w", localPath, err)
	}
	root, err := balanced.Layout(db)
	if err != nil {
		return nil, fmt.Errorf("build unixfs file dag for %s: %w", localPath, err)
	}
	return root, nil
}

func (i *pathImporter) importDirectory(ctx context.Context, localPath string) (ipld.Node, int, int64, error) {
	realPath, err := filepath.EvalSymlinks(localPath)
	if err != nil {
		realPath, err = filepath.Abs(localPath)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("resolve directory %s: %w", localPath, err)
		}
	}
	if _, ok := i.seen[realPath]; ok {
		return nil, 0, 0, fmt.Errorf("symlink cycle detected at %s", localPath)
	}
	i.seen[realPath] = struct{}{}
	defer delete(i.seen, realPath)

	dir, err := i.newDirectory()
	if err != nil {
		return nil, 0, 0, err
	}
	entries, err := os.ReadDir(localPath)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("read directory %s: %w", localPath, err)
	}

	var files int
	var bytesUploaded int64
	for _, entry := range entries {
		childPath := filepath.Join(localPath, entry.Name())
		child, childFiles, childBytes, err := i.importPath(ctx, childPath)
		if err != nil {
			return nil, 0, 0, err
		}
		if err := dir.AddChild(ctx, entry.Name(), child); err != nil {
			return nil, 0, 0, fmt.Errorf("add %s to unixfs directory %s: %w", entry.Name(), localPath, err)
		}
		files += childFiles
		bytesUploaded += childBytes
	}

	root, err := dir.GetNode()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("materialize unixfs directory %s: %w", localPath, err)
	}
	if err := i.dag.Add(ctx, root); err != nil {
		return nil, 0, 0, fmt.Errorf("store unixfs directory %s: %w", localPath, err)
	}
	return root, files, bytesUploaded, nil
}

func (i *pathImporter) newDirectory() (unixfsio.Directory, error) {
	opts := []unixfsio.DirectoryOption{
		unixfsio.WithCidBuilder(i.build),
		unixfsio.WithMaxHAMTFanout(i.opts.HAMTFanout),
	}
	if i.opts.Layout == LayoutHAMT {
		return unixfsio.NewHAMTDirectory(i.dag, 0, opts...)
	}
	return unixfsio.NewDirectory(i.dag, opts...)
}

type casDAGService struct {
	store Store
}

func (s *casDAGService) Add(ctx context.Context, node ipld.Node) error {
	if node == nil {
		return nil
	}
	got, err := s.store.PutWithCodec(ctx, node.RawData(), node.Cid().Type())
	if err != nil {
		return err
	}
	if !got.Equals(node.Cid()) {
		return fmt.Errorf("stored block CID %s does not match DAG node CID %s", got, node.Cid())
	}
	return nil
}

func (s *casDAGService) AddMany(ctx context.Context, nodes []ipld.Node) error {
	for _, node := range nodes {
		if err := s.Add(ctx, node); err != nil {
			return err
		}
	}
	return nil
}

func (s *casDAGService) Get(ctx context.Context, c cid.Cid) (ipld.Node, error) {
	data, err := s.store.Get(ctx, c)
	if err != nil {
		return nil, ipld.ErrNotFound{Cid: c}
	}
	block, err := blocks.NewBlockWithCid(data, c)
	if err != nil {
		return nil, err
	}
	switch c.Type() {
	case cid.Raw:
		return merkledag.DecodeRawBlock(block)
	case cid.DagProtobuf:
		return merkledag.DecodeProtobufBlock(block)
	default:
		return nil, fmt.Errorf("unsupported merkledag codec %d for %s", c.Type(), c)
	}
}

func (s *casDAGService) GetMany(ctx context.Context, cids []cid.Cid) <-chan *ipld.NodeOption {
	out := make(chan *ipld.NodeOption)
	go func() {
		defer close(out)
		for _, c := range cids {
			node, err := s.Get(ctx, c)
			select {
			case out <- &ipld.NodeOption{Node: node, Err: err}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

func (s *casDAGService) Remove(context.Context, cid.Cid) error {
	return nil
}

func (s *casDAGService) RemoveMany(context.Context, []cid.Cid) error {
	return nil
}
