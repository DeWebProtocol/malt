package main

import (
	"fmt"
	"path"
	"strings"

	cid "github.com/ipfs/go-cid"
)

type addNode struct {
	Kind        string
	StorageKind string
	Key         cid.Cid
	Chunks      []cid.Cid
	Children    map[string]*addNode
	Changed     bool
}

func newDirNode() *addNode {
	return &addNode{
		Kind:     "dir",
		Children: make(map[string]*addNode),
	}
}

func ensureDirNode(root *addNode, p string) *addNode {
	root.Changed = true
	if p == "" {
		return root
	}
	segments := splitAddPath(p)
	cur := root
	for _, seg := range segments {
		child, ok := cur.Children[seg]
		if !ok {
			child = newDirNode()
			child.Changed = true
			cur.Children[seg] = child
		}
		if child.Kind != "dir" {
			child = newDirNode()
			child.Changed = true
			cur.Children[seg] = child
		}
		cur.Changed = true
		cur = child
	}
	return cur
}

func setFileNode(root *addNode, p string, key cid.Cid) error {
	segments := splitAddPath(p)
	if len(segments) == 0 {
		return fmt.Errorf("file path must not be empty")
	}
	parentPath := path.Dir(p)
	if parentPath == "." {
		parentPath = ""
	}
	parent := ensureDirNode(root, parentPath)
	name := segments[len(segments)-1]

	if existing, ok := parent.Children[name]; ok {
		if existing.Kind == "file" && existing.Key.Equals(key) {
			return nil
		}
	}
	parent.Children[name] = &addNode{
		Kind:        "file",
		Key:         key,
		StorageKind: storageKindFromCID(key),
		Changed:     true,
	}
	parent.Changed = true
	return nil
}

func ensureFileNode(root *addNode, p string) *addNode {
	segments := splitAddPath(p)
	if len(segments) == 0 {
		return nil
	}
	parentPath := path.Dir(p)
	if parentPath == "." {
		parentPath = ""
	}
	parent := ensureDirNode(root, parentPath)
	name := segments[len(segments)-1]
	node := &addNode{
		Kind:        "file",
		StorageKind: "raw",
		Changed:     true,
	}
	parent.Children[name] = node
	parent.Changed = true
	return node
}

func setMapDirNode(root *addNode, p string, key cid.Cid) error {
	segments := splitAddPath(p)
	if len(segments) == 0 {
		return fmt.Errorf("map directory path must not be empty")
	}
	parentPath := path.Dir(p)
	if parentPath == "." {
		parentPath = ""
	}
	parent := ensureDirNode(root, parentPath)
	name := segments[len(segments)-1]
	parent.Children[name] = &addNode{
		Kind:        "mapdir",
		StorageKind: "map",
		Key:         key,
		Changed:     true,
	}
	parent.Changed = true
	return nil
}

func mergeAddNodes(existing *addNode, staged *addNode) *addNode {
	if staged == nil {
		return existing
	}
	if existing == nil {
		return staged
	}
	if staged.Kind != "dir" {
		if existing.Kind == staged.Kind && existing.Key.Equals(staged.Key) {
			return existing
		}
		return staged
	}
	if existing.Kind != "dir" {
		return staged
	}
	for name, child := range staged.Children {
		mergedChild := mergeAddNodes(existing.Children[name], child)
		if existing.Children[name] != mergedChild {
			existing.Changed = true
		}
		if mergedChild != nil && mergedChild.Changed {
			existing.Changed = true
		}
		existing.Children[name] = mergedChild
	}
	return existing
}

func splitAddPath(p string) []string {
	clean := canonicalAddPath(p)
	if clean == "" {
		return nil
	}
	parts := strings.Split(clean, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		out = append(out, part)
	}
	return out
}

func canonicalAddPath(raw string) string {
	p := strings.TrimSpace(raw)
	p = strings.ReplaceAll(p, "\\", "/")
	p = path.Clean("/" + p)
	p = strings.TrimPrefix(p, "/")
	if p == "." {
		return ""
	}
	return p
}

func storageKindFromCID(c cid.Cid) string {
	if !c.Defined() {
		return ""
	}
	codec := c.Prefix().Codec
	switch codec {
	case 0x55:
		return "raw"
	case 0x300002, 0x300004:
		return "list"
	case 0x300001, 0x300003:
		return "map"
	default:
		return ""
	}
}
