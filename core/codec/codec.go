// Package codec defines MALT-specific multicodec constants and CID utilities.
// MALT uses the Private Use Area (0x300000-0x3FFFFF) for commitment scheme codecs.
package codec

import (
	"fmt"

	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// MALT multicodec constants (Private Use Area: 0x300000-0x3FFFFF)
// These codecs identify different commitment schemes in CID.
const (
	// CodecMaltKZG is the codec for KZG polynomial commitments (48 bytes).
	CodecMaltKZG = 0x300001

	// CodecMaltVerkle is the codec for Verkle commitments (31 bytes stem).
	CodecMaltVerkle = 0x300002

	// CodecMaltIPA is the codec for Inner Product Argument commitments (32 bytes).
	CodecMaltIPA = 0x300003
)

// KZG commitment size constants
const (
	// KZGCommitmentSize is the size of a KZG commitment in bytes (48 bytes).
	KZGCommitmentSize = 48
)

// Verkle commitment size constants
const (
	// VerkleStemSize is the size of a Verkle stem in bytes (31 bytes).
	VerkleStemSize = 31
)

// IPA commitment size constants
const (
	// IPACommitmentSize is the size of an IPA commitment in bytes (32 bytes).
	IPACommitmentSize = 32
)

// NewKZGCid creates a CID from KZG commitment bytes.
// Uses identity multihash to store the commitment directly.
func NewKZGCid(commitment []byte) (cid.Cid, error) {
	if len(commitment) != KZGCommitmentSize {
		return cid.Cid{}, fmt.Errorf("invalid KZG commitment size: %d, expected %d", len(commitment), KZGCommitmentSize)
	}
	return newMaltCid(CodecMaltKZG, commitment)
}

// NewVerkleCid creates a CID from Verkle commitment bytes.
// Uses identity multihash to store the commitment directly.
func NewVerkleCid(commitment []byte) (cid.Cid, error) {
	if len(commitment) != VerkleStemSize {
		return cid.Cid{}, fmt.Errorf("invalid Verkle stem size: %d, expected %d", len(commitment), VerkleStemSize)
	}
	return newMaltCid(CodecMaltVerkle, commitment)
}

// NewIPACid creates a CID from IPA commitment bytes.
// Uses identity multihash to store the commitment directly.
func NewIPACid(commitment []byte) (cid.Cid, error) {
	if len(commitment) != IPACommitmentSize {
		return cid.Cid{}, fmt.Errorf("invalid IPA commitment size: %d, expected %d", len(commitment), IPACommitmentSize)
	}
	return newMaltCid(CodecMaltIPA, commitment)
}

// newMaltCid creates a CIDv1 with the given codec and commitment bytes.
// Uses identity multihash (0x00) to store the commitment directly.
func newMaltCid(codec uint64, commitment []byte) (cid.Cid, error) {
	// Create identity multihash (hash code 0x00, stores data directly)
	// Identity multihash format: <0x00><size><data>
	mhash, err := mh.Encode(commitment, mh.IDENTITY)
	if err != nil {
		return cid.Cid{}, fmt.Errorf("failed to create identity multihash: %w", err)
	}

	// Create CIDv1 with MALT codec
	return cid.NewCidV1(codec, mhash), nil
}

// IsMaltCid checks if a CID is a MALT commitment CID.
func IsMaltCid(c cid.Cid) bool {
	codec := c.Prefix().Codec
	return codec == CodecMaltKZG || codec == CodecMaltVerkle || codec == CodecMaltIPA
}

// GetMaltCodec returns the MALT codec type for a CID.
// Returns 0 if the CID is not a MALT commitment.
func GetMaltCodec(c cid.Cid) uint64 {
	codec := c.Prefix().Codec
	if IsMaltCid(c) {
		return codec
	}
	return 0
}

// ExtractCommitment extracts the raw commitment bytes from a MALT CID.
// Returns an error if the CID is not a MALT commitment or doesn't use identity hash.
func ExtractCommitment(c cid.Cid) ([]byte, error) {
	if !IsMaltCid(c) {
		return nil, fmt.Errorf("not a MALT commitment CID: codec=%x", c.Prefix().Codec)
	}

	// Decode multihash
	decoded, err := mh.Decode(c.Hash())
	if err != nil {
		return nil, fmt.Errorf("failed to decode multihash: %w", err)
	}

	// Check it's identity hash
	if decoded.Code != mh.IDENTITY {
		return nil, fmt.Errorf("expected identity hash, got code=%x", decoded.Code)
	}

	return decoded.Digest, nil
}

// CodecName returns a human-readable name for a MALT codec.
func CodecName(codec uint64) string {
	switch codec {
	case CodecMaltKZG:
		return "malt-kzg"
	case CodecMaltVerkle:
		return "malt-verkle"
	case CodecMaltIPA:
		return "malt-ipa"
	default:
		return fmt.Sprintf("unknown-%x", codec)
	}
}