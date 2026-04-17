package radix

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/dewebprotocol/malt/core/kvstore"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

const (
	currentNodeVersion byte = 1

	nodeKindInternal byte = 1
	nodeKindLeaf     byte = 2
)

const (
	nodeStorePrefix = "radix:node:"
	hotIndexPrefix  = "radix:hidx:"
)

type radixNode struct {
	Version  byte
	Kind     byte
	NodePath []byte
	Children map[byte]cid.Cid
	Entries  []leafEntry
}

type leafEntry struct {
	FullPath  string
	KeyDigest [32]byte
	Target    cid.Cid
}

type buildNode struct {
	Kind     byte
	NodePath []byte
	Children map[byte]*buildNode
	Entries  []leafEntry
}

func digestPath(path string) [32]byte {
	return sha256.Sum256([]byte(path))
}

func cloneNode(n *radixNode) *radixNode {
	out := &radixNode{
		Version:  n.Version,
		Kind:     n.Kind,
		NodePath: append([]byte(nil), n.NodePath...),
	}
	if n.Kind == nodeKindInternal {
		out.Children = make(map[byte]cid.Cid, len(n.Children))
		for k, v := range n.Children {
			out.Children[k] = v
		}
		return out
	}

	out.Entries = make([]leafEntry, len(n.Entries))
	copy(out.Entries, n.Entries)
	return out
}

func newInternalNode(prefix []byte) *radixNode {
	return &radixNode{
		Version:  currentNodeVersion,
		Kind:     nodeKindInternal,
		NodePath: append([]byte(nil), prefix...),
		Children: make(map[byte]cid.Cid),
	}
}

func newLeafNode(entry leafEntry) *radixNode {
	return &radixNode{
		Version:  currentNodeVersion,
		Kind:     nodeKindLeaf,
		NodePath: append([]byte(nil), entry.KeyDigest[:]...),
		Entries:  []leafEntry{entry},
	}
}

func newBuildInternal(prefix []byte) *buildNode {
	return &buildNode{
		Kind:     nodeKindInternal,
		NodePath: append([]byte(nil), prefix...),
		Children: make(map[byte]*buildNode),
	}
}

func newBuildLeaf(entry leafEntry) *buildNode {
	return &buildNode{
		Kind:     nodeKindLeaf,
		NodePath: append([]byte(nil), entry.KeyDigest[:]...),
		Entries:  []leafEntry{entry},
	}
}

func sortLeafEntries(entries []leafEntry) {
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].FullPath < entries[j].FullPath
	})
}

func (n *radixNode) FormatVersion() byte {
	if n == nil || n.Version == 0 {
		return currentNodeVersion
	}
	return n.Version
}

func (n *radixNode) Serialize() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte(n.FormatVersion())
	buf.WriteByte(n.Kind)

	if err := binary.Write(&buf, binary.BigEndian, uint16(len(n.NodePath))); err != nil {
		return nil, err
	}
	if _, err := buf.Write(n.NodePath); err != nil {
		return nil, err
	}

	switch n.Kind {
	case nodeKindInternal:
		slots := make([]int, 0, len(n.Children))
		for slot := range n.Children {
			slots = append(slots, int(slot))
		}
		sort.Ints(slots)
		if err := binary.Write(&buf, binary.BigEndian, uint16(len(slots))); err != nil {
			return nil, err
		}
		for _, slot := range slots {
			buf.WriteByte(byte(slot))
			child := n.Children[byte(slot)]
			childBytes := child.Bytes()
			if err := binary.Write(&buf, binary.BigEndian, uint16(len(childBytes))); err != nil {
				return nil, err
			}
			if _, err := buf.Write(childBytes); err != nil {
				return nil, err
			}
		}
	case nodeKindLeaf:
		entries := make([]leafEntry, len(n.Entries))
		copy(entries, n.Entries)
		sortLeafEntries(entries)
		if err := binary.Write(&buf, binary.BigEndian, uint16(len(entries))); err != nil {
			return nil, err
		}
		for _, entry := range entries {
			pathBytes := []byte(entry.FullPath)
			if err := binary.Write(&buf, binary.BigEndian, uint16(len(pathBytes))); err != nil {
				return nil, err
			}
			if _, err := buf.Write(pathBytes); err != nil {
				return nil, err
			}
			if _, err := buf.Write(entry.KeyDigest[:]); err != nil {
				return nil, err
			}
			targetBytes := entry.Target.Bytes()
			if err := binary.Write(&buf, binary.BigEndian, uint16(len(targetBytes))); err != nil {
				return nil, err
			}
			if _, err := buf.Write(targetBytes); err != nil {
				return nil, err
			}
		}
	default:
		return nil, fmt.Errorf("unknown node kind: %d", n.Kind)
	}

	return buf.Bytes(), nil
}

func deserializeNode(data []byte) (*radixNode, error) {
	r := bytes.NewReader(data)

	version, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	if version != currentNodeVersion {
		return nil, fmt.Errorf("unsupported radix node version: %d", version)
	}

	kind, err := r.ReadByte()
	if err != nil {
		return nil, err
	}

	var pathLen uint16
	if err := binary.Read(r, binary.BigEndian, &pathLen); err != nil {
		return nil, err
	}
	nodePath := make([]byte, pathLen)
	if _, err := r.Read(nodePath); err != nil {
		return nil, err
	}

	node := &radixNode{
		Version:  version,
		Kind:     kind,
		NodePath: nodePath,
	}

	switch kind {
	case nodeKindInternal:
		var count uint16
		if err := binary.Read(r, binary.BigEndian, &count); err != nil {
			return nil, err
		}
		node.Children = make(map[byte]cid.Cid, int(count))
		for range int(count) {
			slot, err := r.ReadByte()
			if err != nil {
				return nil, err
			}
			var childLen uint16
			if err := binary.Read(r, binary.BigEndian, &childLen); err != nil {
				return nil, err
			}
			childBytes := make([]byte, childLen)
			if _, err := r.Read(childBytes); err != nil {
				return nil, err
			}
			childCID, err := cid.Cast(childBytes)
			if err != nil {
				return nil, err
			}
			node.Children[slot] = childCID
		}
	case nodeKindLeaf:
		var count uint16
		if err := binary.Read(r, binary.BigEndian, &count); err != nil {
			return nil, err
		}
		node.Entries = make([]leafEntry, 0, count)
		for range int(count) {
			var pathLen uint16
			if err := binary.Read(r, binary.BigEndian, &pathLen); err != nil {
				return nil, err
			}
			pathBytes := make([]byte, pathLen)
			if _, err := r.Read(pathBytes); err != nil {
				return nil, err
			}
			var digest [32]byte
			if _, err := r.Read(digest[:]); err != nil {
				return nil, err
			}
			var targetLen uint16
			if err := binary.Read(r, binary.BigEndian, &targetLen); err != nil {
				return nil, err
			}
			targetBytes := make([]byte, targetLen)
			if _, err := r.Read(targetBytes); err != nil {
				return nil, err
			}
			targetCID, err := cid.Cast(targetBytes)
			if err != nil {
				return nil, err
			}
			node.Entries = append(node.Entries, leafEntry{
				FullPath:  string(pathBytes),
				KeyDigest: digest,
				Target:    targetCID,
			})
		}
	default:
		return nil, fmt.Errorf("unknown node kind: %d", kind)
	}

	return node, nil
}

func nodeCIDForBytes(data []byte) (cid.Cid, error) {
	mhash, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(cid.Raw, mhash), nil
}

func nodeDigest(nodeCID cid.Cid) ([]byte, error) {
	decoded, err := mh.Decode(nodeCID.Hash())
	if err != nil {
		return nil, err
	}
	return decoded.Digest, nil
}

func nodeStoreKey(nodeCID cid.Cid) []byte {
	out := make([]byte, 0, len(nodeStorePrefix)+len(nodeCID.Bytes()))
	out = append(out, []byte(nodeStorePrefix)...)
	out = append(out, nodeCID.Bytes()...)
	return out
}

func hotIndexKey(root cid.Cid, prefix []byte) []byte {
	out := make([]byte, 0, len(hotIndexPrefix)+len(root.Bytes())+2+len(prefix))
	out = append(out, []byte(hotIndexPrefix)...)
	out = append(out, root.Bytes()...)
	var lenBuf [2]byte
	binary.BigEndian.PutUint16(lenBuf[:], uint16(len(prefix)))
	out = append(out, lenBuf[:]...)
	out = append(out, prefix...)
	return out
}

func putNode(ctx context.Context, store kvstore.KVStore, node *radixNode) (cid.Cid, []byte, error) {
	nodeCID, serialized, err := materializeNode(node)
	if err != nil {
		return cid.Undef, nil, err
	}
	if err := store.Put(ctx, nodeStoreKey(nodeCID), serialized); err != nil {
		return cid.Undef, nil, err
	}
	return nodeCID, serialized, nil
}

func materializeNode(node *radixNode) (cid.Cid, []byte, error) {
	serialized, err := node.Serialize()
	if err != nil {
		return cid.Undef, nil, err
	}
	nodeCID, err := nodeCIDForBytes(serialized)
	if err != nil {
		return cid.Undef, nil, err
	}
	return nodeCID, serialized, nil
}

func loadNode(ctx context.Context, store kvstore.KVStore, nodeCID cid.Cid) (*radixNode, []byte, error) {
	data, err := store.Get(ctx, nodeStoreKey(nodeCID))
	if err != nil {
		return nil, nil, err
	}
	node, err := deserializeNode(data)
	if err != nil {
		return nil, nil, err
	}
	return node, data, nil
}

func putHotIndex(ctx context.Context, store kvstore.KVStore, root cid.Cid, prefix []byte, nodeCID cid.Cid) error {
	return store.Put(ctx, hotIndexKey(root, prefix), nodeCID.Bytes())
}

func getHotIndex(ctx context.Context, store kvstore.KVStore, root cid.Cid, prefix []byte) (cid.Cid, error) {
	data, err := store.Get(ctx, hotIndexKey(root, prefix))
	if err != nil {
		return cid.Undef, err
	}
	return cid.Cast(data)
}

func buildTree(entries []leafEntry) *buildNode {
	root := newBuildInternal(nil)
	for _, entry := range entries {
		insertBuildNode(root, 0, entry)
	}
	return root
}

func insertBuildNode(parent *buildNode, depth int, entry leafEntry) {
	if depth >= len(entry.KeyDigest) {
		return
	}
	slot := entry.KeyDigest[depth]
	child, ok := parent.Children[slot]
	if !ok {
		parent.Children[slot] = newBuildLeaf(entry)
		return
	}

	switch child.Kind {
	case nodeKindInternal:
		insertBuildNode(child, depth+1, entry)
	case nodeKindLeaf:
		if len(child.Entries) > 0 && child.Entries[0].KeyDigest == entry.KeyDigest {
			child.Entries = append(child.Entries, entry)
			sortLeafEntries(child.Entries)
			return
		}
		newLeaf := newBuildLeaf(entry)
		parent.Children[slot] = buildBranch(depth+1, child, child.Entries[0].KeyDigest, newLeaf, entry.KeyDigest)
	}
}

func buildBranch(depth int, oldLeaf *buildNode, oldDigest [32]byte, newLeaf *buildNode, newDigest [32]byte) *buildNode {
	node := newBuildInternal(newDigest[:depth])
	oldSlot := oldDigest[depth]
	newSlot := newDigest[depth]
	if oldSlot == newSlot {
		node.Children[oldSlot] = buildBranch(depth+1, oldLeaf, oldDigest, newLeaf, newDigest)
		return node
	}
	node.Children[oldSlot] = oldLeaf
	node.Children[newSlot] = newLeaf
	return node
}

func materializeBuildNode(node *buildNode, index map[string]cid.Cid, staged map[string][]byte) (cid.Cid, error) {
	switch node.Kind {
	case nodeKindInternal:
		children := make(map[byte]cid.Cid, len(node.Children))
		for slot, child := range node.Children {
			childCID, err := materializeBuildNode(child, index, staged)
			if err != nil {
				return cid.Undef, err
			}
			children[slot] = childCID
		}
		rn := &radixNode{
			Kind:     nodeKindInternal,
			NodePath: append([]byte(nil), node.NodePath...),
			Children: children,
		}
		nodeCID, serialized, err := materializeNode(rn)
		if err != nil {
			return cid.Undef, err
		}
		staged[string(nodeStoreKey(nodeCID))] = serialized
		index[string(rn.NodePath)] = nodeCID
		return nodeCID, nil
	case nodeKindLeaf:
		rn := &radixNode{
			Kind:     nodeKindLeaf,
			NodePath: append([]byte(nil), node.NodePath...),
			Entries:  append([]leafEntry(nil), node.Entries...),
		}
		sortLeafEntries(rn.Entries)
		nodeCID, serialized, err := materializeNode(rn)
		if err != nil {
			return cid.Undef, err
		}
		staged[string(nodeStoreKey(nodeCID))] = serialized
		index[string(rn.NodePath)] = nodeCID
		return nodeCID, nil
	default:
		return cid.Undef, fmt.Errorf("unknown build node kind: %d", node.Kind)
	}
}
