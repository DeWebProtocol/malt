package runtime

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"

	"github.com/dewebprotocol/malt/core/eat"
	"github.com/dewebprotocol/malt/core/sce/commitment"
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

type indexedScheme interface {
	MaxValues() int
	CommitValues(values []cid.Cid) (cid.Cid, error)
	ProveIndex(root cid.Cid, values []cid.Cid, index uint64) (cid.Cid, []byte, error)
	VerifyIndex(root cid.Cid, index uint64, value cid.Cid, proof []byte) (bool, error)
}

// ValidateScheme checks whether the supplied commitment scheme can support the
// v1 list layout. For schemes exposing native fixed-slot helpers, we enforce
// the required root width up front; generic path-oriented schemes are accepted
// and use the slot-path compatibility path.
func ValidateScheme(scheme commitment.Scheme) error {
	if scheme == nil {
		return fmt.Errorf("commitment scheme is nil")
	}
	if indexed, ok := scheme.(indexedScheme); ok && indexed.MaxValues() < RootWidth {
		return fmt.Errorf("commitment scheme capacity %d is smaller than required root width %d", indexed.MaxValues(), RootWidth)
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

// SlotCommitmentPath returns the local path used by the generic Scheme
// compatibility path for one committed slot within a list node.
func SlotCommitmentPath(slot uint64) arcset.Path {
	return arcset.CanonicalizePath("slot/" + strconv.FormatUint(slot, 10))
}

// SlotArcSet materializes the defined slots of a node as a local arc set. This
// is used by the generic path-oriented compatibility path.
func SlotArcSet(slots []cid.Cid) arcset.ArcSet {
	arcs := make(map[arcset.Path]cid.Cid)
	for i, slot := range slots {
		if !slot.Defined() {
			continue
		}
		arcs[SlotCommitmentPath(uint64(i))] = slot
	}
	return arcset.NewSetFromPaths(arcs)
}

// CommitSlots commits a node slot vector using either the scheme's native
// fixed-slot helpers or the generic path-oriented compatibility path.
func CommitSlots(scheme commitment.Scheme, slots []cid.Cid) (cid.Cid, error) {
	if indexed, ok := scheme.(indexedScheme); ok {
		return indexed.CommitValues(slots)
	}
	return scheme.Commit(SlotArcSet(slots))
}

// ProveSlot proves one slot under a committed node.
func ProveSlot(scheme commitment.Scheme, root cid.Cid, slots []cid.Cid, slot uint64) (cid.Cid, []byte, error) {
	if indexed, ok := scheme.(indexedScheme); ok {
		return indexed.ProveIndex(root, slots, slot)
	}
	return scheme.Prove(root, SlotArcSet(slots), SlotCommitmentPath(slot).String())
}

// VerifySlot verifies one committed slot proof.
func VerifySlot(scheme commitment.Scheme, root cid.Cid, slot uint64, value cid.Cid, proof []byte) (bool, error) {
	if indexed, ok := scheme.(indexedScheme); ok {
		return indexed.VerifyIndex(root, slot, value, proof)
	}
	return scheme.Verify(root, SlotCommitmentPath(slot).String(), value, proof)
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
