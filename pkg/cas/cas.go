// Package cas defines the Content-Addressed Storage interface for MALT.
// CAS provides the underlying immutable object storage that MALT builds upon.
package cas

import (
	"fmt"

	"github.com/dewebprotocol/malt/pkg/types"
)

// CAS defines the Content-Addressed Storage interface.
// Implementations store immutable objects identified by their content hash.
//
// Key properties:
// 1. Content-addressed: Objects are identified by their cryptographic hash
// 2. Immutable: Once stored, objects cannot be modified
// 3. Deduplicated: Identical content has the same CID
type CAS interface {
	// Put stores data and returns its CID.
	// The CID is derived from the content using multihash.
	Put(data []byte) (types.CID, error)

	// Get retrieves data by CID.
	// Returns ErrNotFound if the CID doesn't exist.
	Get(cid types.CID) ([]byte, error)

	// Has checks if data exists for the given CID.
	Has(cid types.CID) (bool, error)

	// Delete removes data for the given CID.
	// Note: Some implementations may not support deletion.
	Delete(cid types.CID) error

	// Stat returns information about stored data.
	Stat(cid types.CID) (Stat, error)
}

// Stat contains information about stored data.
type Stat struct {
	// Size is the size in bytes
	Size int64

	// CID is the content identifier
	CID types.CID

	// Replication is the number of replicas (for distributed storage)
	Replication int
}

// BatchCAS extends CAS with batch operations.
type BatchCAS interface {
	CAS

	// PutMany stores multiple objects and returns their CIDs.
	PutMany(data [][]byte) ([]types.CID, error)

	// GetMany retrieves multiple objects by CID.
	GetMany(cids []types.CID) ([][]byte, error)
}

// Config holds CAS configuration.
type Config struct {
	// Type specifies the CAS implementation type
	Type CASType

	// HashFunction specifies the hash function for CID generation
	HashFunction string

	// ReplicationFactor is the number of replicas (for distributed storage)
	ReplicationFactor int

	// IPFS configuration (when Type is CASTypeIPFS)
	IPFS IPFSConfig
}

// CASType identifies the CAS implementation type.
type CASType string

const (
	CASTypeMemory CASType = "memory" // In-memory (for testing)
	CASTypeIPFS   CASType = "ipfs"   // IPFS
	CASTypeFile   CASType = "file"   // File-based
)

// IPFSConfig holds IPFS-specific configuration.
type IPFSConfig struct {
	// Addr is the IPFS API address (e.g., "/ip4/127.0.0.1/tcp/5001")
	Addr string

	// Timeout is the request timeout in seconds
	Timeout int
}

// DefaultConfig returns the default CAS configuration.
func DefaultConfig() *Config {
	return &Config{
		Type:          CASTypeMemory,
		HashFunction:  "sha2-256",
		ReplicationFactor: 1,
	}
}

// Errors
var (
	// ErrNotFound is returned when data is not found.
	ErrNotFound = fmt.Errorf("data not found")

	// ErrInvalidCID is returned when a CID is invalid.
	ErrInvalidCID = fmt.Errorf("invalid CID")

	// ErrNotSupported is returned when an operation is not supported.
	ErrNotSupported = fmt.Errorf("operation not supported")
)

// IsNotFound checks if an error is ErrNotFound.
func IsNotFound(err error) bool {
	return err == ErrNotFound
}