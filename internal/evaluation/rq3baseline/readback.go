package rq3baseline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	merkledagimport "github.com/dewebprotocol/malt-client/merkledag/importer"
	merkledag "github.com/ipfs/boxo/ipld/merkledag"
	unixfs "github.com/ipfs/boxo/ipld/unixfs"
	unixfsio "github.com/ipfs/boxo/ipld/unixfs/io"
	cid "github.com/ipfs/go-cid"
	ipld "github.com/ipfs/go-ipld-format"
)

// VerifyAll checks every final source file and the complete directory key set.
// Per-commit checks must not hide a lost untouched file or an extra binding.
func (s *StreamSession) VerifyAll(ctx context.Context) error {
	if s == nil || s.failed || s.editor == nil {
		return fmt.Errorf("invalid stream")
	}
	dag := merkledagimport.NewDAGService(s.store)
	key, err := cid.Parse(s.editor.Root())
	if err != nil {
		return err
	}
	node, err := dag.Get(ctx, key)
	if err != nil {
		return err
	}
	var paths []string
	var walk func(ipld.Node, string) error
	walk = func(node ipld.Node, prefix string) error {
		dir, err := unixfsio.NewDirectoryFromNode(dag, node)
		if err != nil {
			return err
		}
		links, err := dir.Links(ctx)
		if err != nil {
			return err
		}
		for _, link := range links {
			name := link.Name
			if prefix != "" {
				name = prefix + "/" + name
			}
			child, err := dag.Get(ctx, link.Cid)
			if err != nil {
				return err
			}
			isDirectory := false
			if proto, ok := child.(*merkledag.ProtoNode); ok {
				info, err := unixfs.FSNodeFromBytes(proto.Data())
				if err != nil {
					return err
				}
				isDirectory = info.Type() == unixfs.TDirectory || info.Type() == unixfs.THAMTShard
			}
			if isDirectory && !s.flat {
				if err := walk(child, name); err != nil {
					return err
				}
				continue
			}
			if _, exists := s.state[name]; !exists {
				return fmt.Errorf("unexpected final binding %q", name)
			}
			paths = append(paths, name)
		}
		return nil
	}
	if err := walk(node, ""); err != nil {
		return err
	}
	sort.Strings(paths)
	if len(paths) != len(s.state) {
		return fmt.Errorf("final directory key set differs from frozen source")
	}
	for i := 1; i < len(paths); i++ {
		if paths[i] == paths[i-1] {
			return fmt.Errorf("duplicate final binding")
		}
	}
	return s.VerifyPaths(ctx, paths)
}

// RetainedCAS independently enumerates the actual CAS files before cleanup.
// It measures serialized object bodies, excluding filesystem allocation and
// inode metadata; no physical-device write amplification is implied.
func (s *StreamSession) RetainedCAS() (objects, bytes int64, err error) {
	if s == nil || s.failed || s.store == nil {
		return 0, 0, fmt.Errorf("invalid stream")
	}
	err = filepath.WalkDir(s.store.root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return statErr
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular CAS entry %q", path)
		}
		objects++
		bytes += info.Size()
		return nil
	})
	return
}

// VerifyPaths checks the active CAS root against the independently validated
// source state. It includes absence and verifies every traversed CID through
// the accounting store. It is not a MALT proof or a durable-write receipt.
func (s *StreamSession) VerifyPaths(ctx context.Context, paths []string) error {
	if s == nil || s.failed || s.editor == nil {
		return fmt.Errorf("readback requires an active, valid stream")
	}
	return s.verifyPaths(ctx, paths)
}

func (s *StreamSession) verifyPaths(ctx context.Context, paths []string) error {
	dag := merkledagimport.NewDAGService(s.store)
	root, err := cid.Parse(s.editor.Root())
	if err != nil {
		return err
	}
	for _, path := range paths {
		if err := validateCanonicalPath("readback path", path); err != nil {
			return err
		}
		node, err := dag.Get(ctx, root)
		if err != nil {
			return err
		}
		parts := strings.Split(path, "/")
		if s.flat {
			parts = []string{path}
		}
		for _, part := range parts {
			dir, directoryErr := unixfsio.NewDirectoryFromNode(dag, node)
			if directoryErr != nil {
				return fmt.Errorf("readback %q: %w", path, directoryErr)
			}
			node, err = dir.Find(ctx, part)
			if err != nil {
				break
			}
		}
		want, exists := s.state[path]
		if !exists {
			if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("readback deleted path %q returned %v", path, err)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("readback present path %q: %w", path, err)
		}
		if s.preserveMode {
			proto, ok := node.(*merkledag.ProtoNode)
			if !ok {
				return fmt.Errorf("file %q lacks its declared permission metadata", path)
			}
			info, err := unixfs.FSNodeFromBytes(proto.Data())
			if err != nil {
				return err
			}
			if uint32(info.Mode().Perm()) != want.mode {
				return fmt.Errorf("readback permissions for %q differ from frozen source", path)
			}
		}
		reader, err := unixfsio.NewDagReader(ctx, node, dag)
		if err != nil {
			return fmt.Errorf("readback file %q: %w", path, err)
		}
		hash := sha256.New()
		n, readErr := io.Copy(hash, io.LimitReader(reader, int64(want.size)+1))
		closeErr := reader.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return err
		}
		if n != int64(want.size) || hex.EncodeToString(hash.Sum(nil)) != want.hash {
			return fmt.Errorf("readback %q does not match the frozen post-image", path)
		}
	}
	return nil
}
