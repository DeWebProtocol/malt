// Package types defines the core types for MALT.
// These types are used across all packages to avoid circular dependencies.
package types

import (
	"encoding/json"
	"fmt"

	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// CID represents a content identifier for objects in MALT.
// It wraps the go-cid implementation for compatibility with IPFS.
type CID struct {
	cid.Cid
}

// NewCID creates a new CID from raw bytes using the default multihash (sha2-256).
func NewCID(data []byte) (CID, error) {
	// Create multihash from data
	mhash, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		return CID{}, fmt.Errorf("failed to create multihash: %w", err)
	}

	// Create CIDv1 with raw codec
	c := cid.NewCidV1(cid.Raw, mhash)
	return CID{Cid: c}, nil
}

// NewCIDFromCID creates a MALT CID from a go-cid CID.
func NewCIDFromCID(c cid.Cid) CID {
	return CID{Cid: c}
}

// ParseCID parses a CID string.
func ParseCID(s string) (CID, error) {
	c, err := cid.Decode(s)
	if err != nil {
		return CID{}, fmt.Errorf("failed to parse CID: %w", err)
	}
	return CID{Cid: c}, nil
}

// Bytes returns the byte representation of the CID.
func (c CID) Bytes() []byte {
	return c.Cid.Bytes()
}

// String returns the string representation of the CID.
func (c CID) String() string {
	return c.Cid.String()
}

// Equals checks if two CIDs are equal.
func (c CID) Equals(other CID) bool {
	return c.Cid.Equals(other.Cid)
}

// IsEmpty checks if the CID is empty.
func (c CID) IsEmpty() bool {
	return !c.Cid.Defined()
}

// IsDefined checks if the CID is defined.
func (c CID) IsDefined() bool {
	return c.Cid.Defined()
}

// MarshalJSON implements json.Marshaler.
func (c CID) MarshalJSON() ([]byte, error) {
	return json.Marshal(c.String())
}

// UnmarshalJSON implements json.Unmarshaler.
func (c *CID) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	parsed, err := ParseCID(s)
	if err != nil {
		return err
	}
	*c = parsed
	return nil
}

// CIDBuilder provides utilities for building CIDs with different codecs.
type CIDBuilder struct {
	codec    uint64
	mhType   uint64
	mhLength int
}

// DefaultCIDBuilder returns a CIDBuilder with default settings (raw codec, sha2-256).
func DefaultCIDBuilder() *CIDBuilder {
	return &CIDBuilder{
		codec:    cid.Raw,
		mhType:   mh.SHA2_256,
		mhLength: -1,
	}
}

// WithCodec sets the codec for the CID builder.
func (b *CIDBuilder) WithCodec(codec uint64) *CIDBuilder {
	b.codec = codec
	return b
}

// WithMultihash sets the multihash type and length for the CID builder.
func (b *CIDBuilder) WithMultihash(mhType uint64, length int) *CIDBuilder {
	b.mhType = mhType
	b.mhLength = length
	return b
}

// Build creates a CID from the given data.
func (b *CIDBuilder) Build(data []byte) (CID, error) {
	mhash, err := mh.Sum(data, b.mhType, b.mhLength)
	if err != nil {
		return CID{}, fmt.Errorf("failed to create multihash: %w", err)
	}
	c := cid.NewCidV1(b.codec, mhash)
	return CID{Cid: c}, nil
}

// Path represents a path label for arcs.
// It identifies a specific structural relationship from a source object.
type Path string

// String returns the string representation of the path.
func (p Path) String() string {
	return string(p)
}

// Bytes returns the byte representation of the path.
func (p Path) Bytes() []byte {
	return []byte(p)
}

// IsValid checks if the path is valid (non-empty).
func (p Path) IsValid() bool {
	return len(p) > 0
}

// ArcPair represents a (path, target) pair without the source.
// This is used in ArcSet to represent all outgoing arcs from an object.
type ArcPair struct {
	Path   Path `json:"path"`
	Target CID  `json:"target"`
}

// NewArcPair creates a new ArcPair.
func NewArcPair(path Path, target CID) ArcPair {
	return ArcPair{Path: path, Target: target}
}

// String returns a string representation of the arc pair.
func (ap ArcPair) String() string {
	return fmt.Sprintf("(%s -> %s)", ap.Path, ap.Target)
}