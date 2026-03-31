// Package key defines the Key interface and its implementations.
// Key is the runtime dispatch term for resolution, representing
// either a StructureRoot (commitment to arc set) or a PayloadCID (content identifier).
package key

import (
	"encoding/binary"
	"fmt"

	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// KeyKind represents the type of a Key.
type KeyKind int

const (
	// KeyKindStructureRoot indicates a commitment to an arc set.
	KeyKindStructureRoot KeyKind = iota
	// KeyKindPayloadCID indicates a content identifier for payload data.
	KeyKindPayloadCID
)

// Key is the runtime dispatch term for resolution.
// It can represent either a StructureRoot or a PayloadCID.
type Key interface {
	// Bytes returns the raw bytes for storage/encoding.
	Bytes() []byte

	// String returns a human-readable representation.
	String() string

	// Equals checks if two keys are equal.
	Equals(other Key) bool

	// Kind returns the type of the key.
	Kind() KeyKind
}

// StructureRoot represents a commitment to an arc set.
// It is the entry point for MALT-native structure traversal.
type StructureRoot struct {
	bytes []byte
}

// NewStructureRoot creates a new StructureRoot from commitment bytes.
func NewStructureRoot(bytes []byte) *StructureRoot {
	return &StructureRoot{bytes: bytes}
}

// Bytes returns the commitment bytes.
func (s *StructureRoot) Bytes() []byte {
	return s.bytes
}

// String returns a hex representation.
func (s *StructureRoot) String() string {
	return fmt.Sprintf("root:%x", s.bytes)
}

// Equals checks equality.
func (s *StructureRoot) Equals(other Key) bool {
	if other == nil {
		return false
	}
	o, ok := other.(*StructureRoot)
	if !ok {
		return false
	}
	if len(s.bytes) != len(o.bytes) {
		return false
	}
	for i := range s.bytes {
		if s.bytes[i] != o.bytes[i] {
			return false
		}
	}
	return true
}

// Kind returns KeyKindStructureRoot.
func (s *StructureRoot) Kind() KeyKind {
	return KeyKindStructureRoot
}

// IsEmpty checks if the root is empty.
func (s *StructureRoot) IsEmpty() bool {
	return len(s.bytes) == 0
}

// PayloadCID represents a content identifier for payload data.
// It wraps go-cid for compatibility with IPFS/CAS.
type PayloadCID struct {
	cid cid.Cid
}

// NewPayloadCID creates a new PayloadCID from raw data.
func NewPayloadCID(data []byte) (*PayloadCID, error) {
	mhash, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		return nil, fmt.Errorf("failed to create multihash: %w", err)
	}
	c := cid.NewCidV1(cid.Raw, mhash)
	return &PayloadCID{cid: c}, nil
}

// NewPayloadCIDFromBytes creates a PayloadCID from CID bytes.
func NewPayloadCIDFromBytes(b []byte) (*PayloadCID, error) {
	c, err := cid.Cast(b)
	if err != nil {
		return nil, fmt.Errorf("failed to parse CID: %w", err)
	}
	return &PayloadCID{cid: c}, nil
}

// NewPayloadCIDFromString parses a CID string.
func NewPayloadCIDFromString(s string) (*PayloadCID, error) {
	c, err := cid.Decode(s)
	if err != nil {
		return nil, fmt.Errorf("failed to decode CID: %w", err)
	}
	return &PayloadCID{cid: c}, nil
}

// NewPayloadCIDFromCID creates a PayloadCID from a go-cid.
func NewPayloadCIDFromCID(c cid.Cid) *PayloadCID {
	return &PayloadCID{cid: c}
}

// Bytes returns the CID bytes.
func (p *PayloadCID) Bytes() []byte {
	return p.cid.Bytes()
}

// String returns the CID string.
func (p *PayloadCID) String() string {
	return p.cid.String()
}

// Equals checks equality.
func (p *PayloadCID) Equals(other Key) bool {
	if other == nil {
		return false
	}
	o, ok := other.(*PayloadCID)
	if !ok {
		return false
	}
	return p.cid.Equals(o.cid)
}

// Kind returns KeyKindPayloadCID.
func (p *PayloadCID) Kind() KeyKind {
	return KeyKindPayloadCID
}

// CID returns the underlying go-cid.Cid.
func (p *PayloadCID) CID() cid.Cid {
	return p.cid
}

// IsEmpty checks if the CID is empty.
func (p *PayloadCID) IsEmpty() bool {
	return !p.cid.Defined()
}

// EncodeKey encodes a Key to bytes with a kind prefix.
// Format: [kind:1][key_bytes]
func EncodeKey(k Key) []byte {
	bytes := k.Bytes()
	result := make([]byte, 1+len(bytes))
	result[0] = byte(k.Kind())
	copy(result[1:], bytes)
	return result
}

// DecodeKey decodes bytes to a Key.
func DecodeKey(data []byte) (Key, error) {
	if len(data) < 1 {
		return nil, fmt.Errorf("key data too short")
	}
	kind := KeyKind(data[0])
	bytes := data[1:]

	switch kind {
	case KeyKindStructureRoot:
		return NewStructureRoot(bytes), nil
	case KeyKindPayloadCID:
		return NewPayloadCIDFromBytes(bytes)
	default:
		return nil, fmt.Errorf("unknown key kind: %d", kind)
	}
}

// EncodeKeyWithIndex encodes a Key with an index for versioned storage.
// Format: [index:8][kind:1][key_bytes]
func EncodeKeyWithIndex(k Key, index uint64) []byte {
	bytes := k.Bytes()
	result := make([]byte, 8+1+len(bytes))
	binary.BigEndian.PutUint64(result[0:8], index)
	result[8] = byte(k.Kind())
	copy(result[9:], bytes)
	return result
}

// DecodeKeyWithIndex decodes a Key with index.
func DecodeKeyWithIndex(data []byte) (Key, uint64, error) {
	if len(data) < 9 {
		return nil, 0, fmt.Errorf("key data too short")
	}
	index := binary.BigEndian.Uint64(data[0:8])
	kind := KeyKind(data[8])
	bytes := data[9:]

	var k Key
	var err error
	switch kind {
	case KeyKindStructureRoot:
		k = NewStructureRoot(bytes)
	case KeyKindPayloadCID:
		k, err = NewPayloadCIDFromBytes(bytes)
		if err != nil {
			return nil, 0, err
		}
	default:
		return nil, 0, fmt.Errorf("unknown key kind: %d", kind)
	}

	return k, index, nil
}