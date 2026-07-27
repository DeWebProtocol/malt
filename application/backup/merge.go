package backup

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type treeNode struct {
	Kind   string
	Mode   fs.FileMode
	Digest string
	Link   string
	Source string
}

// mergePlaintextTrees performs a conservative file-level three-way merge.
// Independent paths and one-sided changes merge automatically. Concurrent
// changes to the same path remain explicit conflicts; line-oriented content
// merging is deliberately left to the user's chosen merge tool.
func mergePlaintextTrees(ctx context.Context, base, local, remote, destination string) ([]string, error) {
	baseNodes, err := scanPlaintextTree(ctx, base)
	if err != nil {
		return nil, fmt.Errorf("scan merge base: %w", err)
	}
	localNodes, err := scanPlaintextTree(ctx, local)
	if err != nil {
		return nil, fmt.Errorf("scan local candidate: %w", err)
	}
	remoteNodes, err := scanPlaintextTree(ctx, remote)
	if err != nil {
		return nil, fmt.Errorf("scan remote candidate: %w", err)
	}
	paths := make(map[string]struct{}, len(baseNodes)+len(localNodes)+len(remoteNodes))
	for path := range baseNodes {
		paths[path] = struct{}{}
	}
	for path := range localNodes {
		paths[path] = struct{}{}
	}
	for path := range remoteNodes {
		paths[path] = struct{}{}
	}
	selected := make(map[string]*treeNode, len(paths))
	var conflicts []string
	for path := range paths {
		baseNode := baseNodes[path]
		localNode := localNodes[path]
		remoteNode := remoteNodes[path]
		switch {
		case sameTreeNode(localNode, remoteNode):
			selected[path] = localNode
		case sameTreeNode(localNode, baseNode):
			selected[path] = remoteNode
		case sameTreeNode(remoteNode, baseNode):
			selected[path] = localNode
		default:
			conflicts = append(conflicts, filepath.ToSlash(path))
		}
	}
	sort.Strings(conflicts)
	if len(conflicts) != 0 {
		return conflicts, nil
	}
	for path, node := range selected {
		if node == nil {
			continue
		}
		parent := filepath.Dir(path)
		for parent != "." {
			parentNode, ok := selected[parent]
			if !ok || parentNode == nil || parentNode.Kind != "dir" {
				return []string{filepath.ToSlash(path)}, nil
			}
			parent = filepath.Dir(parent)
		}
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return nil, err
	}
	ordered := make([]string, 0, len(selected))
	for path, node := range selected {
		if node != nil {
			ordered = append(ordered, path)
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		leftDepth := strings.Count(filepath.ToSlash(ordered[i]), "/")
		rightDepth := strings.Count(filepath.ToSlash(ordered[j]), "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return ordered[i] < ordered[j]
	})
	for _, relative := range ordered {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		node := selected[relative]
		target := filepath.Join(destination, relative)
		switch node.Kind {
		case "dir":
			if err := os.Mkdir(target, node.Mode.Perm()); err != nil && !os.IsExist(err) {
				return nil, err
			}
		case "file":
			if err := copyMergeFile(node.Source, target, node.Mode.Perm()); err != nil {
				return nil, err
			}
		case "symlink":
			if err := os.Symlink(node.Link, target); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unsupported merge node kind %q", node.Kind)
		}
	}
	return nil, nil
}

func scanPlaintextTree(ctx context.Context, root string) (map[string]*treeNode, error) {
	result := map[string]*treeNode{}
	if strings.TrimSpace(root) == "" {
		return result, nil
	}
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("merge snapshot root is not a safe directory")
	}
	err = filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == root {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		node := &treeNode{Mode: info.Mode(), Source: current}
		switch {
		case info.IsDir():
			node.Kind = "dir"
		case info.Mode().IsRegular():
			node.Kind = "file"
			file, err := os.Open(current)
			if err != nil {
				return err
			}
			hash := sha256.New()
			_, copyErr := io.Copy(hash, &contextReader{ctx: ctx, reader: file})
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			node.Digest = hex.EncodeToString(hash.Sum(nil))
		case info.Mode()&os.ModeSymlink != 0:
			node.Kind = "symlink"
			node.Link, err = os.Readlink(current)
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported filesystem object in merge: %s", current)
		}
		result[relative] = node
		return nil
	})
	return result, err
}

func sameTreeNode(left, right *treeNode) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Kind == right.Kind &&
		left.Mode.Perm() == right.Mode.Perm() &&
		left.Digest == right.Digest &&
		left.Link == right.Link
}

func samePlaintextTree(ctx context.Context, left, right string) (bool, error) {
	leftNodes, err := scanPlaintextTree(ctx, left)
	if err != nil {
		return false, err
	}
	rightNodes, err := scanPlaintextTree(ctx, right)
	if err != nil {
		return false, err
	}
	if len(leftNodes) != len(rightNodes) {
		return false, nil
	}
	for path, leftNode := range leftNodes {
		if !sameTreeNode(leftNode, rightNodes[path]) {
			return false, nil
		}
	}
	return true, nil
}

func copyPlaintextTree(ctx context.Context, source, destination string) error {
	conflicts, err := mergePlaintextTrees(ctx, source, source, source, destination)
	if err != nil {
		return err
	}
	if len(conflicts) != 0 {
		return fmt.Errorf("copying a stable plaintext tree unexpectedly conflicted at %s", strings.Join(conflicts, ", "))
	}
	return nil
}

func plaintextContentFingerprint(ctx context.Context, root string) (string, error) {
	nodes, err := scanPlaintextTree(ctx, root)
	if err != nil {
		return "", err
	}
	paths := make([]string, 0, len(nodes))
	for path := range nodes {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, path := range paths {
		node := nodes[path]
		writeMergeField(hash, []byte(filepath.ToSlash(path)))
		writeMergeField(hash, []byte(node.Kind))
		var mode [4]byte
		binary.BigEndian.PutUint32(mode[:], uint32(node.Mode.Perm()))
		_, _ = hash.Write(mode[:])
		writeMergeField(hash, []byte(node.Digest))
		writeMergeField(hash, []byte(node.Link))
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func writeMergeField(writer io.Writer, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write(value)
}

func copyMergeFile(source, target string, mode fs.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}
