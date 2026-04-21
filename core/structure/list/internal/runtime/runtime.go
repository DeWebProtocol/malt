package runtime

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"

	"github.com/dewebprotocol/malt/core/commitment"
	"github.com/dewebprotocol/malt/core/eat"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

const (
	// Fanout is the fixed branching factor for the v1 tree-shaped list runtime.
	Fanout = 255

	// RootWidth reserves slot 0 for the authenticated length marker.
	RootWidth = Fanout + 1

	lengthMarkerPrefix = "malt:list:length:v1:"
)

// ValidateCommitment checks whether the supplied index commitment can support
// the v1 list layout.
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
	return make([]cid.Cid, Fanout)
}

// ContentSlots returns the data-bearing portion of a slot vector.
func ContentSlots(slots []cid.Cid, isRoot bool) []cid.Cid {
	if isRoot {
		return slots[1:]
	}
	return slots
}

// NodeSlotPath returns the canonical EAT key for a materialized list node slot.
func NodeSlotPath(root cid.Cid, slot uint64) arcset.Path {
	return arcset.CanonicalizePath(fmt.Sprintf("nodes/%s/slots/%d", root.String(), slot))
}

// LoadSlots reconstructs a fixed-width slot vector for a committed node.
// Missing entries are treated as cid.Undef.
func LoadSlots(ctx context.Context, e eat.EAT, bucketID string, root cid.Cid, width int) ([]cid.Cid, error) {
	if e == nil {
		return nil, fmt.Errorf("eat is nil")
	}
	if !root.Defined() {
		return nil, fmt.Errorf("node root is undefined")
	}
	if width <= 0 {
		return nil, fmt.Errorf("width must be positive")
	}

	paths := make([]string, width)
	for i := range paths {
		paths[i] = NodeSlotPath(root, uint64(i)).String()
	}

	found, err := e.BatchGet(ctx, bucketID, cid.Undef, paths)
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

// StoreSlots materializes a committed node in EAT under its node-root namespace.
func StoreSlots(ctx context.Context, e eat.EAT, bucketID string, root cid.Cid, slots []cid.Cid) error {
	if e == nil {
		return fmt.Errorf("eat is nil")
	}
	if !root.Defined() {
		return fmt.Errorf("node root is undefined")
	}

	arcs := make(map[string]cid.Cid)
	for i, slot := range slots {
		if !slot.Defined() {
			continue
		}
		arcs[NodeSlotPath(root, uint64(i)).String()] = slot
	}
	if len(arcs) == 0 {
		return nil
	}
	return e.Update(ctx, bucketID, cid.Undef, cid.Undef, arcs)
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
	if !recomputed.Equals(root) {
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
	if !provedRoot.Equals(root) {
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

// RequiredHeight returns the minimal non-root height required for length values.
func RequiredHeight(length uint64) int {
	if length <= Fanout {
		return 0
	}

	capacity := uint64(Fanout)
	height := 0
	for capacity < length {
		height++
		if capacity > math.MaxUint64/uint64(Fanout) {
			return height
		}
		capacity *= uint64(Fanout)
	}
	return height
}

// SubtreeCapacity returns how many values fit under a node of the given height.
func SubtreeCapacity(height int) (uint64, error) {
	if height < 0 {
		return 0, fmt.Errorf("height must be non-negative")
	}

	capacity := uint64(Fanout)
	for i := 0; i < height; i++ {
		if capacity > math.MaxUint64/uint64(Fanout) {
			return 0, fmt.Errorf("list capacity overflow at height %d", height)
		}
		capacity *= uint64(Fanout)
	}
	return capacity, nil
}

// IndexDigits decomposes an index into base-Fanout digits for the target height.
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
