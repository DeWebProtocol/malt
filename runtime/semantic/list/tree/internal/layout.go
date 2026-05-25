package layout

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"

	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/auth/commitment"
	"github.com/dewebprotocol/malt/runtime/arctable"
	"github.com/dewebprotocol/malt/wire/maltcid"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

const (
	// DefaultFanout is the v1 default commitment width for list nodes.
	// The current KZG backend supports 256 slots per commitment, so v1 fixes
	// this value to 256.
	DefaultFanout = 256

	// BranchingFactor is the number of content slots per committed list node.
	//
	// Slot 0 in every list node is reserved for authenticated node metadata, so
	// all nodes expose (DefaultFanout-1) content slots.
	//
	// For simplicity, v2 uses the same branching factor for root and non-root
	// nodes.
	BranchingFactor = DefaultFanout - 1

	// RootWidth is the fixed slot width for the committed root node.
	// Slot 0 is the authenticated node metadata marker.
	RootWidth = DefaultFanout

	// NodeWidth is the fixed slot width for all committed non-root nodes.
	NodeWidth = DefaultFanout

	// LegacyNodeWidth is the v1 non-root width. Legacy children do not reserve
	// slot 0 for metadata; all slots are content slots.
	LegacyNodeWidth = BranchingFactor

	lengthMarkerPrefix = "malt:list:length:v1:"
	fixedMetaPrefix    = "malt:list:fixed-meta:v1:"
	nodeMetaPrefix     = "malt:list:node-meta:v2:"
)

// FixedMetadata is the authenticated root metadata for fixed-width measured
// lists.
type FixedMetadata struct {
	ChildCount uint64
	TotalSize  uint64
	ChunkSize  uint64
}

// NodeMetadata is the authenticated metadata stored in slot 0 of every v2 list
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
// the v2 list layout.
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
	if !isRoot && len(slots) == LegacyNodeWidth {
		return slots
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
	if !isRoot && len(slots) == LegacyNodeWidth {
		return uint64(digit), nil
	}
	return uint64(digit) + 1, nil
}

// NodeSlotPath returns the canonical ArcTable key for a materialized list node slot.
func NodeSlotPath(root cid.Cid, slot uint64) arcset.Path {
	return arcset.CanonicalizePath(fmt.Sprintf("nodes/%s/slots/%d", root.String(), slot))
}

// LoadSlots reconstructs a fixed-width slot vector for a committed node.
// Missing entries are treated as cid.Undef.
func LoadSlots(ctx context.Context, e arctable.ArcTable, namespace string, root cid.Cid, width int) ([]cid.Cid, error) {
	if e == nil {
		return nil, fmt.Errorf("arctable is nil")
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

// StoreSlots materializes a committed node in ArcTable under its node-root namespace.
func StoreSlots(ctx context.Context, e arctable.ArcTable, namespace string, root cid.Cid, slots []cid.Cid) error {
	if e == nil {
		return fmt.Errorf("arctable is nil")
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

// EncodeLengthMarker encodes list length as a self-describing identity CID so
// it can be parsed after restart without any external side state.
func EncodeLengthMarker(length uint64) (cid.Cid, error) {
	payload := make([]byte, len(lengthMarkerPrefix)+8)
	copy(payload, []byte(lengthMarkerPrefix))
	binary.BigEndian.PutUint64(payload[len(lengthMarkerPrefix):], length)

	sum, err := mh.Sum(payload, mh.IDENTITY, len(payload))
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(cid.Raw, sum), nil
}

// DecodeLengthMarker parses the authenticated list length from an identity CID.
func DecodeLengthMarker(marker cid.Cid) (uint64, error) {
	if !marker.Defined() {
		return 0, fmt.Errorf("length marker is undefined")
	}

	decoded, err := mh.Decode(marker.Hash())
	if err != nil {
		return 0, err
	}
	if decoded.Code != mh.IDENTITY {
		return 0, fmt.Errorf("length marker is not identity-encoded")
	}
	if len(decoded.Digest) != len(lengthMarkerPrefix)+8 {
		return 0, fmt.Errorf("length marker payload has unexpected size %d", len(decoded.Digest))
	}
	if string(decoded.Digest[:len(lengthMarkerPrefix)]) != lengthMarkerPrefix {
		return 0, fmt.Errorf("length marker prefix mismatch")
	}
	return binary.BigEndian.Uint64(decoded.Digest[len(lengthMarkerPrefix):]), nil
}

// EncodeFixedMetadata encodes fixed-width measured list metadata as a
// self-describing identity CID.
func EncodeFixedMetadata(meta FixedMetadata) (cid.Cid, error) {
	payload := make([]byte, len(fixedMetaPrefix)+24)
	copy(payload, []byte(fixedMetaPrefix))
	binary.BigEndian.PutUint64(payload[len(fixedMetaPrefix):], meta.ChildCount)
	binary.BigEndian.PutUint64(payload[len(fixedMetaPrefix)+8:], meta.TotalSize)
	binary.BigEndian.PutUint64(payload[len(fixedMetaPrefix)+16:], meta.ChunkSize)

	sum, err := mh.Sum(payload, mh.IDENTITY, len(payload))
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(cid.Raw, sum), nil
}

// DecodeFixedMetadata parses fixed-width measured list metadata from an
// identity CID.
func DecodeFixedMetadata(marker cid.Cid) (FixedMetadata, error) {
	if !marker.Defined() {
		return FixedMetadata{}, fmt.Errorf("fixed metadata marker is undefined")
	}

	decoded, err := mh.Decode(marker.Hash())
	if err != nil {
		return FixedMetadata{}, err
	}
	if decoded.Code != mh.IDENTITY {
		return FixedMetadata{}, fmt.Errorf("fixed metadata marker is not identity-encoded")
	}
	if len(decoded.Digest) != len(fixedMetaPrefix)+24 {
		nodeMeta, nodeErr := DecodeNodeMetadata(marker)
		if nodeErr == nil && nodeMeta.ChunkSize > 0 {
			return FixedMetadata{
				ChildCount: nodeMeta.ChildCount,
				TotalSize:  nodeMeta.TotalSize,
				ChunkSize:  nodeMeta.ChunkSize,
			}, nil
		}
		return FixedMetadata{}, fmt.Errorf("fixed metadata marker payload has unexpected size %d", len(decoded.Digest))
	}
	if string(decoded.Digest[:len(fixedMetaPrefix)]) != fixedMetaPrefix {
		nodeMeta, nodeErr := DecodeNodeMetadata(marker)
		if nodeErr == nil && nodeMeta.ChunkSize > 0 {
			return FixedMetadata{
				ChildCount: nodeMeta.ChildCount,
				TotalSize:  nodeMeta.TotalSize,
				ChunkSize:  nodeMeta.ChunkSize,
			}, nil
		}
		return FixedMetadata{}, fmt.Errorf("fixed metadata marker prefix mismatch")
	}
	return FixedMetadata{
		ChildCount: binary.BigEndian.Uint64(decoded.Digest[len(fixedMetaPrefix):]),
		TotalSize:  binary.BigEndian.Uint64(decoded.Digest[len(fixedMetaPrefix)+8:]),
		ChunkSize:  binary.BigEndian.Uint64(decoded.Digest[len(fixedMetaPrefix)+16:]),
	}, nil
}

// EncodeNodeMetadata encodes authenticated v2 list node metadata as a
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

// DecodeNodeMetadata parses authenticated v2 list node metadata.
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

// DecodeRootLength parses the child count authenticated in a root metadata
// marker. Plain lists encode only length; fixed measured lists encode length as
// ChildCount inside their metadata marker.
func DecodeRootLength(marker cid.Cid) (uint64, error) {
	length, err := DecodeLengthMarker(marker)
	if err == nil {
		return length, nil
	}
	meta, metaErr := DecodeFixedMetadata(marker)
	if metaErr == nil {
		return meta.ChildCount, nil
	}
	nodeMeta, nodeMetaErr := DecodeNodeMetadata(marker)
	if nodeMetaErr == nil {
		return nodeMeta.ChildCount, nil
	}
	return 0, err
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
