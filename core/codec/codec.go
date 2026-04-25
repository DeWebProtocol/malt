// Package codec defines MALT-specific multicodec constants and CID utilities.
// MALT uses the Private Use Area (0x300000-0x3FFFFF) for typed structure roots.
//
// Wire allocation (locked; see Implementation plan Phase 0):
//
//	malt-map-kzg  = 0x300001
//	malt-list-kzg = 0x300002
package codec

import (
	"bytes"
	"fmt"

	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// Typed MALT multicodecs (Private Use Area: 0x300000-0x3FFFFF).
const (
	CodecMaltMapKZG  = 0x300001 // malt-map-kzg
	CodecMaltListKZG = 0x300002 // malt-list-kzg
)

// SemanticKind indicates the structural semantic encoded in the typed CID.
type SemanticKind string

const (
	SemanticKindUnknown SemanticKind = "unknown"
	SemanticKindMap     SemanticKind = "map"
	SemanticKindList    SemanticKind = "list"
)

// BackendKind indicates the primitive commitment backend used by the typed CID.
type BackendKind string

const (
	BackendKindUnknown BackendKind = "unknown"
	BackendKindKZG     BackendKind = "kzg"
)

// CodecMaltKZG is an alias for [CodecMaltMapKZG] (map roots use KZG in the current prototype).
// Deprecated: prefer CodecMaltMapKZG for new code.
const CodecMaltKZG = CodecMaltMapKZG

// Commitment size constants
const (
	KZGCommitmentSize = 48 // KZGCommitmentSize is the size of a KZG commitment in bytes (48 bytes).
)

// NewKZGCid creates a CID from KZG commitment bytes using the malt-map-kzg codec.
func NewKZGCid(commitment []byte) (cid.Cid, error) {
	if len(commitment) != KZGCommitmentSize {
		return cid.Cid{}, fmt.Errorf("invalid KZG commitment size: %d, expected %d", len(commitment), KZGCommitmentSize)
	}
	return newMaltCid(CodecMaltMapKZG, commitment)
}

// NewMapKZGCid is an alias for [NewKZGCid].
func NewMapKZGCid(commitment []byte) (cid.Cid, error) {
	return NewKZGCid(commitment)
}

// NewListKZGCid creates a CID from KZG commitment bytes using the malt-list-kzg codec.
func NewListKZGCid(commitment []byte) (cid.Cid, error) {
	if len(commitment) != KZGCommitmentSize {
		return cid.Cid{}, fmt.Errorf("invalid KZG commitment size: %d, expected %d", len(commitment), KZGCommitmentSize)
	}
	return newMaltCid(CodecMaltListKZG, commitment)
}

// NewTypedCID constructs a typed MALT CID for the given semantic/backend kinds.
func NewTypedCID(semantic SemanticKind, backend BackendKind, commitment []byte) (cid.Cid, error) {
	switch backend {
	case BackendKindKZG:
		if semantic == SemanticKindList {
			return NewListKZGCid(commitment)
		}
		if semantic == SemanticKindMap {
			return NewMapKZGCid(commitment)
		}
	}
	return cid.Undef, fmt.Errorf("unsupported typed cid kind: semantic=%s backend=%s", semantic, backend)
}

// newMaltCid creates a CIDv1 with the given codec and commitment bytes.
// Uses identity multihash (0x00) to store the commitment directly.
func newMaltCid(codec uint64, commitment []byte) (cid.Cid, error) {
	mhash, err := mh.Encode(commitment, mh.IDENTITY)
	if err != nil {
		return cid.Cid{}, fmt.Errorf("failed to create identity multihash: %w", err)
	}
	return cid.NewCidV1(codec, mhash), nil
}

// IsMaltCid checks if a CID is a typed MALT structure root (map or list, KZG).
func IsMaltCid(c cid.Cid) bool {
	switch c.Prefix().Codec {
	case CodecMaltMapKZG, CodecMaltListKZG:
		return true
	default:
		return false
	}
}

// SemanticKindOf returns the semantic kind for a typed MALT CID.
func SemanticKindOf(c cid.Cid) SemanticKind {
	switch c.Prefix().Codec {
	case CodecMaltMapKZG:
		return SemanticKindMap
	case CodecMaltListKZG:
		return SemanticKindList
	default:
		return SemanticKindUnknown
	}
}

// BackendKindOf returns the backend kind for a typed MALT CID.
func BackendKindOf(c cid.Cid) BackendKind {
	switch c.Prefix().Codec {
	case CodecMaltMapKZG, CodecMaltListKZG:
		return BackendKindKZG
	default:
		return BackendKindUnknown
	}
}

// GetMaltCodec returns the MALT codec value for a CID.
// Returns 0 if the CID is not a MALT structure root.
func GetMaltCodec(c cid.Cid) uint64 {
	if IsMaltCid(c) {
		return c.Prefix().Codec
	}
	return 0
}

// ExtractCommitment extracts the raw commitment bytes from a MALT structure CID.
func ExtractCommitment(c cid.Cid) ([]byte, error) {
	if !IsMaltCid(c) {
		return nil, fmt.Errorf("not a MALT commitment CID: codec=%x", c.Prefix().Codec)
	}
	decoded, err := mh.Decode(c.Hash())
	if err != nil {
		return nil, fmt.Errorf("failed to decode multihash: %w", err)
	}
	if decoded.Code != mh.IDENTITY {
		return nil, fmt.Errorf("expected identity hash, got code=%x", decoded.Code)
	}
	return decoded.Digest, nil
}

// EqualCommitment reports whether a and b carry the same commitment bytes.
// This is useful when comparing typed roots that differ only by semantic codec
// (e.g., map vs list) but refer to the same primitive commitment.
func EqualCommitment(a, b cid.Cid) (bool, error) {
	ab, err := ExtractCommitment(a)
	if err != nil {
		return false, err
	}
	bb, err := ExtractCommitment(b)
	if err != nil {
		return false, err
	}
	return bytes.Equal(ab, bb), nil
}

// CodecName returns the locked wire name for a typed MALT multicodec.
func CodecName(codec uint64) string {
	switch codec {
	case CodecMaltMapKZG:
		return "malt-map-kzg"
	case CodecMaltListKZG:
		return "malt-list-kzg"
	default:
		return fmt.Sprintf("unknown-%x", codec)
	}
}
