// Package storage defines the storage interface for MALT's Explicit Arc Table (EAT).
// The EAT is an index that maps (commitment, path) pairs to target CIDs,
// enabling fast lookup of structural relationships.
package storage

import (
	"encoding/json"
	"fmt"

	"github.com/dewebprotocol/malt/pkg/commitment"
	"github.com/dewebprotocol/malt/pkg/types"
)

// Storage defines the interface for the Explicit Arc Table (EAT) storage backend.
// The EAT stores entries of the form: (C_v, p) -> c
//
// Key properties:
// 1. Fast lookup: O(1) or O(log n) retrieval
// 2. Atomic updates: Batch operations should be atomic
// 3. Durability: Persisted data survives restarts (for persistent backends)
// 4. Versioning: Support for versioned resolution via commitment lineage
type Storage interface {
	// Get retrieves the target CID for a commitment and path.
	// Returns ErrNotFound if no entry exists.
	Get(comm commitment.Commitment, p types.Path) (types.CID, error)

	// Put stores an arc entry.
	// If an entry already exists for (comm, p), it is overwritten.
	Put(comm commitment.Commitment, p types.Path, c types.CID) error

	// Delete removes an arc entry.
	// Returns ErrNotFound if no entry exists.
	Delete(comm commitment.Commitment, p types.Path) error

	// Has checks if an entry exists for (comm, p).
	Has(comm commitment.Commitment, p types.Path) (bool, error)

	// Batch executes multiple operations atomically.
	Batch(ops []Operation) error

	// Iterate iterates over all entries for a commitment.
	// The iterator is valid until the returned Iter is closed.
	Iterate(comm commitment.Commitment) (Iter, error)

	// Close closes the storage and releases resources.
	Close() error
}

// Iter is an iterator over storage entries.
type Iter interface {
	// Next moves to the next entry.
	// Returns false when there are no more entries.
	Next() bool

	// Entry returns the current entry.
	// Must be called after Next() returns true.
	Entry() EATEntry

	// Err returns any error encountered during iteration.
	Err() error

	// Close releases iterator resources.
	Close()
}

// Operation represents a single storage operation.
type Operation struct {
	Type  OpType
	Entry EATEntry
}

// OpType defines the type of storage operation.
type OpType int

const (
	OpPut    OpType = iota // Put operation
	OpDelete               // Delete operation
)

// EATEntry represents an entry in the Explicit Arc Table.
type EATEntry struct {
	// Commitment is the structure commitment
	Commitment commitment.Commitment `json:"commitment"`

	// Path is the path label
	Path types.Path `json:"path"`

	// Target is the target CID
	Target types.CID `json:"target"`

	// Version is optional version number for lineage tracking
	Version uint64 `json:"version,omitempty"`

	// ParentCommitment is the parent commitment in the lineage (for versioned resolution)
	ParentCommitment commitment.Commitment `json:"parent_commitment,omitempty"`
}

// NewEATEntry creates a new EAT entry.
func NewEATEntry(comm commitment.Commitment, p types.Path, target types.CID) EATEntry {
	return EATEntry{
		Commitment: comm,
		Path:       p,
		Target:     target,
	}
}

// Key returns the storage key for this entry.
func (e EATEntry) Key() Key {
	return MakeKey(e.Commitment, e.Path)
}

// String returns a string representation of the entry.
func (e EATEntry) String() string {
	return fmt.Sprintf("EATEntry{comm=%s, path=%s, target=%s, version=%d}",
		e.Commitment, e.Path, e.Target, e.Version)
}

// MarshalBinary serializes the entry to bytes.
func (e EATEntry) MarshalBinary() ([]byte, error) {
	return json.Marshal(e)
}

// UnmarshalBinary deserializes the entry from bytes.
func (e *EATEntry) UnmarshalBinary(data []byte) error {
	return json.Unmarshal(data, e)
}

// Key represents a storage key.
type Key []byte

// MakeKey creates a storage key from commitment and path.
func MakeKey(comm commitment.Commitment, p types.Path) Key {
	// Key format: commitment_length + commitment + path
	key := make([]byte, 4+len(comm)+len(p))
	// Write commitment length (big endian)
	key[0] = byte(len(comm) >> 24)
	key[1] = byte(len(comm) >> 16)
	key[2] = byte(len(comm) >> 8)
	key[3] = byte(len(comm))
	// Write commitment
	copy(key[4:4+len(comm)], comm)
	// Write path
	copy(key[4+len(comm):], p)
	return key
}

// ParseKey parses a storage key into commitment and path.
func ParseKey(key Key) (commitment.Commitment, types.Path, error) {
	if len(key) < 4 {
		return nil, "", fmt.Errorf("key too short")
	}
	commLen := int(key[0])<<24 | int(key[1])<<16 | int(key[2])<<8 | int(key[3])
	if len(key) < 4+commLen {
		return nil, "", fmt.Errorf("key too short for commitment")
	}
	comm := commitment.Commitment(key[4 : 4+commLen])
	path := types.Path(key[4+commLen:])
	return comm, path, nil
}

// String returns a string representation of the key.
func (k Key) String() string {
	comm, path, err := ParseKey(k)
	if err != nil {
		return fmt.Sprintf("<invalid key: %v>", err)
	}
	return fmt.Sprintf("(%s, %s)", comm, path)
}

// Errors
var (
	// ErrNotFound is returned when an entry is not found.
	ErrNotFound = fmt.Errorf("entry not found")

	// ErrClosed is returned when operating on a closed storage.
	ErrClosed = fmt.Errorf("storage is closed")

	// ErrInvalidKey is returned when a key is invalid.
	ErrInvalidKey = fmt.Errorf("invalid key")
)

// IsNotFound checks if an error is ErrNotFound.
func IsNotFound(err error) bool {
	return err == ErrNotFound
}

// Stats contains storage statistics.
type Stats struct {
	// TotalEntries is the total number of entries
	TotalEntries int64

	// TotalCommitments is the number of unique commitments
	TotalCommitments int64

	// SizeBytes is the approximate size in bytes
	SizeBytes int64
}

// LineageStore extends Storage with commitment lineage tracking.
// This enables versioned resolution as described in Section 4.4.
type LineageStore interface {
	Storage

	// SetParent records that childComm is derived from parentComm.
	SetParent(childComm, parentComm commitment.Commitment) error

	// GetParent returns the parent commitment of the given commitment.
	// Returns ErrNotFound if the commitment has no parent (is a root).
	GetParent(comm commitment.Commitment) (commitment.Commitment, error)

	// GetLineage returns the full lineage from the given commitment to the root.
	// The result is ordered from newest to oldest.
	GetLineage(comm commitment.Commitment) ([]commitment.Commitment, error)

	// GetLatest returns the latest commitment in a lineage.
	GetLatest(rootComm commitment.Commitment) (commitment.Commitment, error)
}