package importer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	mdag "github.com/ipfs/boxo/ipld/merkledag"
	unixfs "github.com/ipfs/boxo/ipld/unixfs"
	ipld "github.com/ipfs/go-ipld-format"
)

// Batch stages directory changes for a basic UnixFS Editor. File objects are
// written by PutFile; Commit writes only the final affected directory versions,
// bottom-up, and advances the Editor's Root after all writes succeed. A failed
// batch may leave immutable file or directory objects in Store. It is not a
// storage transaction, and neither Batch nor Editor supports concurrent use.
type Batch struct {
	editor   *Editor
	baseRoot string
	root     *batchDirectory
	failed   error
	closed   bool
}

type batchDirectory struct {
	node     *mdag.ProtoNode
	children map[string]*batchDirectory
	dirty    bool
}

// BeginBatch starts an isolated directory overlay at the current Root. Discard
// the Batch to abandon it. HAMT and adaptive directory layouts are unsupported.
func (e *Editor) BeginBatch() (*Batch, error) {
	if e == nil || e.flat || e.opts.DirLayout != DirLayoutBasic {
		return nil, fmt.Errorf("directory batching requires a basic UnixFS editor")
	}
	root, err := e.batchDirectory(e.root)
	if err != nil {
		return nil, err
	}
	return &Batch{editor: e, baseRoot: e.Root(), root: root}, nil
}

func (e *Editor) batchDirectory(node ipld.Node) (*batchDirectory, error) {
	dirty := node == nil
	if node == nil {
		dir, err := e.newDirectory()
		if err != nil {
			return nil, err
		}
		node, err = dir.GetNode()
		if err != nil {
			return nil, err
		}
	}
	proto, ok := node.(*mdag.ProtoNode)
	if !ok {
		return nil, fmt.Errorf("path component %s is not a basic UnixFS directory", node.Cid())
	}
	data, err := unixfs.FSNodeFromBytes(proto.Data())
	if err != nil || data.Type() != unixfs.TDirectory {
		return nil, fmt.Errorf("path component %s is not a basic UnixFS directory", node.Cid())
	}
	return &batchDirectory{node: proto.Copy().(*mdag.ProtoNode), children: make(map[string]*batchDirectory), dirty: dirty}, nil
}

func (b *Batch) ready(ctx context.Context) error {
	if b == nil || b.closed {
		return fmt.Errorf("directory batch is closed")
	}
	if b.failed != nil {
		return fmt.Errorf("directory batch failed: %w", b.failed)
	}
	return ctx.Err()
}

// PutFile writes a file DAG and stages its directory binding. Replacing an
// existing entry follows Editor.PutFile semantics; intermediate directory
// versions are never sent to Store.
func (b *Batch) PutFile(ctx context.Context, filePath string, data []byte, mode fs.FileMode) (err error) {
	if err := b.ready(ctx); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			b.failed = err
		}
	}()
	clean, parts, err := cleanPathParts(filePath)
	if err != nil {
		return err
	}
	importer := pathImporter{dag: b.editor.dag, opts: b.editor.opts, build: b.editor.build}
	file, err := importer.importFileReader(ctx, clean, bytes.NewReader(data), virtualFileMode(mode), time.Time{})
	if err != nil {
		return err
	}
	return b.put(ctx, b.root, parts, file)
}

func (b *Batch) child(ctx context.Context, dir *batchDirectory, name string, create bool) (*batchDirectory, error) {
	if child, ok := dir.children[name]; ok {
		return child, nil
	}
	var node ipld.Node
	link, err := dir.node.GetNodeLink(name)
	if errors.Is(err, mdag.ErrLinkNotFound) {
		if !create {
			return nil, fmt.Errorf("%s: %w", name, os.ErrNotExist)
		}
	} else if err != nil {
		return nil, err
	} else {
		node, err = b.editor.dag.Get(ctx, link.Cid)
		if err != nil {
			return nil, err
		}
	}
	child, err := b.editor.batchDirectory(node)
	if err != nil {
		return nil, err
	}
	dir.children[name] = child
	return child, nil
}

func (b *Batch) put(ctx context.Context, dir *batchDirectory, parts []string, file ipld.Node) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name := parts[0]
	if len(parts) == 1 {
		if err := removeBatchLink(dir.node, name); err != nil {
			return err
		}
		if err := dir.node.AddNodeLink(name, file); err != nil {
			return err
		}
		delete(dir.children, name)
	} else {
		child, err := b.child(ctx, dir, name, true)
		if err != nil {
			return err
		}
		if err := b.put(ctx, child, parts[1:], file); err != nil {
			return err
		}
	}
	dir.dirty = true
	return nil
}

// RemoveFile removes a staged or existing binding and prunes empty ancestors.
// A directory that disappears within the batch is not written as an empty
// intermediate object. The root directory itself is always retained.
func (b *Batch) RemoveFile(ctx context.Context, filePath string) (err error) {
	if err := b.ready(ctx); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			b.failed = err
		}
	}()
	_, parts, err := cleanPathParts(filePath)
	if err != nil {
		return err
	}
	return b.remove(ctx, b.root, parts)
}

func (b *Batch) remove(ctx context.Context, dir *batchDirectory, parts []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name := parts[0]
	if len(parts) == 1 {
		_, linkErr := dir.node.GetNodeLink(name)
		if linkErr != nil && dir.children[name] == nil {
			return fmt.Errorf("%s: %w", name, os.ErrNotExist)
		}
		if err := removeBatchLink(dir.node, name); err != nil {
			return err
		}
		delete(dir.children, name)
	} else {
		child, err := b.child(ctx, dir, name, false)
		if err != nil {
			return err
		}
		if err := b.remove(ctx, child, parts[1:]); err != nil {
			return err
		}
		if len(child.node.Links()) == 0 && len(child.children) == 0 {
			if err := removeBatchLink(dir.node, name); err != nil {
				return err
			}
			delete(dir.children, name)
		}
	}
	dir.dirty = true
	return nil
}

func removeBatchLink(node *mdag.ProtoNode, name string) error {
	err := node.RemoveNodeLink(name)
	if errors.Is(err, mdag.ErrLinkNotFound) {
		return nil
	}
	return err
}

// Commit closes the batch and publishes its Root after writing each surviving
// affected directory once. A changed Editor Root rejects a stale batch.
func (b *Batch) Commit(ctx context.Context) error {
	if err := b.ready(ctx); err != nil {
		return err
	}
	defer func() { b.closed, b.root = true, nil }()
	if b.editor.Root() != b.baseRoot {
		return fmt.Errorf("editor root changed while directory batch was open")
	}
	root, err := b.flush(ctx, b.root)
	if err != nil {
		return err
	}
	b.editor.root = root
	return nil
}

func (b *Batch) flush(ctx context.Context, dir *batchDirectory) (*mdag.ProtoNode, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !dir.dirty {
		return dir.node, nil
	}
	for _, name := range sortedKeys(dir.children) {
		child, err := b.flush(ctx, dir.children[name])
		if err != nil {
			return nil, err
		}
		if err := removeBatchLink(dir.node, name); err != nil {
			return nil, err
		}
		if err := dir.node.AddNodeLink(name, child); err != nil {
			return nil, err
		}
	}
	if err := b.editor.dag.Add(ctx, dir.node); err != nil {
		return nil, err
	}
	return dir.node, nil
}
