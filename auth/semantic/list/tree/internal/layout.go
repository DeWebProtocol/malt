package layout

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"

	"github.com/dewebprotocol/malt/auth/arcset"
	materializer "github.com/dewebprotocol/malt/auth/arcset/materializer"
	"github.com/dewebprotocol/malt/auth/commitment"
	"github.com/dewebprotocol/malt/wire/maltcid"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

const (
	// DefaultFanout is the default commitment width for list nodes.
	// The current KZG backend supports 256 slots per commitment, so this
	// value is fixed to 256.
	DefaultFanout = 256

	// BranchingFactor is the number of content slots per committed list node.
	//
	// Slot 0 in every list node is reserved for authenticated node metadata, so
	// all nodes expose (DefaultFanout-1) content slots.
	//
	// Root and non-root nodes share the same branching factor.
	BranchingFactor = DefaultFanout - 1

	// RootWidth is the fixed slot width for the committed root node.
	// Slot 0 is the authenticated node metadata marker.
	RootWidth = DefaultFanout

	// NodeWidth is the fixed slot width for all committed non-root nodes.
	NodeWidth = DefaultFanout

	nodeMetaPrefix = "malt:list:node-meta:"
)

// NodeMetadata is the authenticated metadata stored in slot 0 of every list
// tree node. ChunkSize == 0 identifies a plain list node; ChunkSize > 0
// identifies a fixed-width measured node whose TotalSize is the byte span
// covered by that subtree.
type NodeMetadata struct {
	Height     uint64
	ChildCount uint64
	TotalSize  uint64
	ChunkSize  uint64
}

// ValidateCommitment checks whether the supplied index commitment can support
// the list layout.
func ValidateCommitment(scheme commitment.IndexCommitment) error {
	if scheme == nil {
		return fmt.Errorf("index commitment is nil")
	}
	if scheme.MaxValues() < RootWidth {
		return fmt.Errorf("index commitment capacity %d is smaller than required root width %d", scheme.MaxValues(), RootWidth)
	}
	return nil
}

// EmptyRootSlots allocates a zero-initialized root slot vector.
func EmptyRootSlots() []cid.Cid {
	return make([]cid.Cid, RootWidth)
}

// EmptyNodeSlots allocates a zero-initialized non-root slot vector.
func EmptyNodeSlots() []cid.Cid {
	return make([]cid.Cid, NodeWidth)
}

// ContentSlots returns the data-bearing portion of a slot vector.
func ContentSlots(slots []cid.Cid, isRoot bool) []cid.Cid {
	if len(slots) == 0 {
		return nil
	}
	return slots[1:]
}

// ContentSlotIndex maps a logical child digit to its physical committed slot.
func ContentSlotIndex(slots []cid.Cid, isRoot bool, digit int) (uint64, error) {
	if digit < 0 {
		return 0, fmt.Errorf("content digit %d is negative", digit)
	}
	content := ContentSlots(slots, isRoot)
	if digit >= len(content) {
		return 0, fmt.Errorf("content digit %d exceeds content width %d", digit, len(content))
	}
	return uint64(digit) + 1, nil
}

// NodeSlotPath returns the canonical ArcSet materializer key for a materialized list node slot.
func NodeSlotPath(root cid.Cid, slot uint64) arcset.Path {
	return arcset.CanonicalizePath(fmt.Sprintf("nodes/%s/slots/%d", root.String(), slot))
}

// LoadSlots reconstructs a fixed-width slot vector for a committed node.
// Missing entries are treated as cid.Undef.
func LoadSlots(ctx context.Context, e materializer.Lookup, namespace string, root cid.Cid, width int) ([]cid.Cid, error) {
	if e == nil {
		return nil, fmt.Errorf("materializer is nil")
	}
	if !root.Defined() {
		return nil, fmt.Errorf("node root is undefined")
	}
	if width <= 0 {
		return nil, fmt.Errorf("width must be positive")
	}

	paths := make([]arcset.Path, width)
	for i := range paths {
		paths[i] = NodeSlotPath(root, uint64(i))
	}

	found, err := e.BatchGet(ctx, namespace, cid.Undef, paths)
	if err != nil {
		return nil, err
	}

	slots := make([]cid.Cid, width)
	for i, path := range paths {
		if target, ok := found[path]; ok {
			slots[i] = target
		}
	}
	return slots, nil
}

// StoreSlots materializes a committed node in ArcSet materializer under its node-root namespace.
func StoreSlots(ctx context.Context, e materializer.Updater, namespace string, root cid.Cid, slots []cid.Cid) error {
	if e == nil {
		return fmt.Errorf("materializer is nil")
	}
	if !root.Defined() {
		return fmt.Errorf("node root is undefined")
	}

	arcs := make(map[arcset.Path]cid.Cid)
	for i, slot := range slots {
		if !slot.Defined() {
			continue
		}
		arcs[NodeSlotPath(root, uint64(i))] = slot
	}
	if len(arcs) == 0 {
		return nil
	}
	snapshot, err := arcset.NewArcSetFromPaths(arcs)
	if err != nil {
		return err
	}
	return e.Update(ctx, namespace, cid.Undef, cid.Undef, snapshot)
}

// CellsFromSlots converts a CID slot vector into commitment cells.
func CellsFromSlots(slots []cid.Cid) []commitment.Cell {
	cells := make([]commitment.Cell, len(slots))
	for i, slot := range slots {
		cells[i] = commitment.CellFromCID(slot)
	}
	return cells
}

// CommitSlots commits a node slot vector via the index commitment backend.
func CommitSlots(scheme commitment.IndexCommitment, slots []cid.Cid) (cid.Cid, error) {
	return scheme.Commit(CellsFromSlots(slots))
}

// ValidateSlots checks that the materialized slot vector recomputes to root.
func ValidateSlots(scheme commitment.IndexCommitment, root cid.Cid, slots []cid.Cid) error {
	recomputed, err := CommitSlots(scheme, slots)
	if err != nil {
		return err
	}
	ok, err := maltcid.EqualCommitment(recomputed, root)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("materialized node state does not match root %s", root.String())
	}
	return nil
}

// ProveSlot proves one slot under a committed node.
func ProveSlot(scheme commitment.IndexCommitment, root cid.Cid, slots []cid.Cid, slot uint64) (commitment.Cell, []byte, error) {
	provedRoot, value, proof, err := scheme.Prove(CellsFromSlots(slots), slot)
	if err != nil {
		return nil, nil, err
	}
	ok, err := maltcid.EqualCommitment(provedRoot, root)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, fmt.Errorf("recomputed node root does not match requested root")
	}
	return value, proof, nil
}

// VerifySlot verifies one committed slot proof.
func VerifySlot(scheme commitment.IndexCommitment, root cid.Cid, slot uint64, value commitment.Cell, proof []byte) (bool, error) {
	return scheme.VerifyIndex(root, slot, value, proof)
}

// EncodeNodeMetadata encodes authenticated list node metadata as a
// self-describing identity CID.
func EncodeNodeMetadata(meta NodeMetadata) (cid.Cid, error) {
	if meta.ChunkSize == 0 && meta.TotalSize != 0 {
		return cid.Undef, fmt.Errorf("plain node metadata cannot carry total size %d", meta.TotalSize)
	}
	if meta.ChunkSize > 0 && meta.ChildCount == 0 && meta.TotalSize != 0 {
		return cid.Undef, fmt.Errorf("measured empty node cannot carry total size %d", meta.TotalSize)
	}
	payload := make([]byte, len(nodeMetaPrefix)+32)
	copy(payload, []byte(nodeMetaPrefix))
	binary.BigEndian.PutUint64(payload[len(nodeMetaPrefix):], meta.Height)
	binary.BigEndian.PutUint64(payload[len(nodeMetaPrefix)+8:], meta.ChildCount)
	binary.BigEndian.PutUint64(payload[len(nodeMetaPrefix)+16:], meta.TotalSize)
	binary.BigEndian.PutUint64(payload[len(nodeMetaPrefix)+24:], meta.ChunkSize)

	sum, err := mh.Sum(payload, mh.IDENTITY, len(payload))
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(cid.Raw, sum), nil
}

// DecodeNodeMetadata parses authenticated list node metadata.
func DecodeNodeMetadata(marker cid.Cid) (NodeMetadata, error) {
	if !marker.Defined() {
		return NodeMetadata{}, fmt.Errorf("node metadata marker is undefined")
	}

	decoded, err := mh.Decode(marker.Hash())
	if err != nil {
		return NodeMetadata{}, err
	}
	if decoded.Code != mh.IDENTITY {
		return NodeMetadata{}, fmt.Errorf("node metadata marker is not identity-encoded")
	}
	if len(decoded.Digest) != len(nodeMetaPrefix)+32 {
		return NodeMetadata{}, fmt.Errorf("node metadata marker payload has unexpected size %d", len(decoded.Digest))
	}
	if string(decoded.Digest[:len(nodeMetaPrefix)]) != nodeMetaPrefix {
		return NodeMetadata{}, fmt.Errorf("node metadata marker prefix mismatch")
	}
	meta := NodeMetadata{
		Height:     binary.BigEndian.Uint64(decoded.Digest[len(nodeMetaPrefix):]),
		ChildCount: binary.BigEndian.Uint64(decoded.Digest[len(nodeMetaPrefix)+8:]),
		TotalSize:  binary.BigEndian.Uint64(decoded.Digest[len(nodeMetaPrefix)+16:]),
		ChunkSize:  binary.BigEndian.Uint64(decoded.Digest[len(nodeMetaPrefix)+24:]),
	}
	if meta.ChunkSize == 0 && meta.TotalSize != 0 {
		return NodeMetadata{}, fmt.Errorf("plain node metadata carries total size %d", meta.TotalSize)
	}
	if meta.ChunkSize > 0 && meta.ChildCount == 0 && meta.TotalSize != 0 {
		return NodeMetadata{}, fmt.Errorf("measured empty node carries total size %d", meta.TotalSize)
	}
	return meta, nil
}

// RequiredHeight returns the minimal non-root height required for length values.
func RequiredHeight(length uint64) int {
	if length <= uint64(BranchingFactor) {
		return 0
	}

	capacity := uint64(BranchingFactor)
	height := 0
	for capacity < length {
		height++
		if capacity > math.MaxUint64/uint64(BranchingFactor) {
			return height
		}
		capacity *= uint64(BranchingFactor)
	}
	return height
}

// SubtreeCapacity returns how many values fit under a node of the given height.
func SubtreeCapacity(height int) (uint64, error) {
	if height < 0 {
		return 0, fmt.Errorf("height must be non-negative")
	}

	capacity := uint64(BranchingFactor)
	for i := 0; i < height; i++ {
		if capacity > math.MaxUint64/uint64(BranchingFactor) {
			return 0, fmt.Errorf("list capacity overflow at height %d", height)
		}
		capacity *= uint64(BranchingFactor)
	}
	return capacity, nil
}

// IndexDigits decomposes an index into base-BranchingFactor digits for the target height.
func IndexDigits(index uint64, height int) ([]int, error) {
	if height < 0 {
		return nil, fmt.Errorf("height must be non-negative")
	}

	digits := make([]int, height+1)
	remaining := index
	for level := 0; level <= height; level++ {
		exp := height - level
		if exp == 0 {
			digits[level] = int(remaining)
			continue
		}
		chunk, err := SubtreeCapacity(exp - 1)
		if err != nil {
			return nil, err
		}
		digits[level] = int(remaining / chunk)
		remaining %= chunk
	}
	return digits, nil
}
