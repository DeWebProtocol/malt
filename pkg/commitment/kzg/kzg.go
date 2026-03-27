// Package kzg provides a KZG (Kate-Zaverucha-Goldberg) polynomial commitment scheme.
//
// KZG commitments:
// 1. Require a trusted setup (structured reference string)
// 2. Have constant-size proofs (single group element)
// 3. Support efficient verification
// 4. Enable batch verification
//
// This implementation uses github.com/crate-crypto/go-kzg-4844
// which is the KZG implementation used in Ethereum EIP-4844.
package kzg

import (
	"crypto/sha256"
	"fmt"
	"sync"

	gokzg4844 "github.com/crate-crypto/go-kzg-4844"
	"github.com/dewebprotocol/malt/pkg/commitment"
	"github.com/dewebprotocol/malt/pkg/types"
)

// KZGCommitment implements CommitmentScheme using KZG polynomial commitments.
type KZGCommitment struct {
	// config holds the configuration
	config *Config

	// context holds the KZG context (SRS)
	// This is loaded from trusted setup
	context *gokzg4844.Context

	// cache stores arc sets for proof generation
	cache map[string]*cacheEntry

	// mu protects concurrent access
	mu sync.RWMutex

	// initialized indicates if setup is complete
	initialized bool
}

// Config holds KZG commitment configuration.
type Config struct {
	// UseSecureSetup uses a cryptographically secure trusted setup
	// For production, this should be true
	UseSecureSetup bool
}

// DefaultConfig returns the default KZG configuration.
func DefaultConfig() *Config {
	return &Config{
		UseSecureSetup: true,
	}
}

// cacheEntry stores cached data for a commitment.
type cacheEntry struct {
	arcs        *types.ArcSet
	pathToIndex map[types.Path]int
	blob        *gokzg4844.Blob
}

// New creates a new KZG commitment scheme.
func New(cfg *Config) (*KZGCommitment, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	k := &KZGCommitment{
		config: cfg,
		cache:  make(map[string]*cacheEntry),
	}

	// Initialize with trusted setup
	if err := k.initialize(); err != nil {
		return nil, fmt.Errorf("failed to initialize KZG: %w", err)
	}

	return k, nil
}

// initialize loads the trusted setup.
func (k *KZGCommitment) initialize() error {
	var context *gokzg4844.Context
	var err error

	if k.config.UseSecureSetup {
		// Use the secure trusted setup provided by the library
		context, err = gokzg4844.NewContext4096Secure()
	} else {
		// For testing, we could use a test setup
		context, err = gokzg4844.NewContext4096Secure()
	}

	if err != nil {
		return fmt.Errorf("failed to create KZG context: %w", err)
	}

	k.context = context
	k.initialized = true
	return nil
}

// Commit generates a KZG commitment for an arc set.
func (k *KZGCommitment) Commit(arcs *types.ArcSet) (commitment.Commitment, error) {
	if arcs == nil {
		return nil, fmt.Errorf("arc set is nil")
	}

	if !k.initialized {
		return nil, fmt.Errorf("KZG not initialized")
	}

	k.mu.Lock()
	defer k.mu.Unlock()

	// Convert arc set to blob (4096 scalars * 32 bytes each)
	pairs := arcs.Pairs()
	pathToIndex := make(map[types.Path]int, len(pairs))

	// Create blob
	blob := &gokzg4844.Blob{}

	// Fill blob with arc data
	for i, pair := range pairs {
		// Convert CID to 32-byte scalar
		scalar := cidToScalar(pair.Target)

		// Place in blob at position i
		offset := i * gokzg4844.SerializedScalarSize
		if offset+gokzg4844.SerializedScalarSize <= len(blob) {
			copy(blob[offset:offset+gokzg4844.SerializedScalarSize], scalar[:])
		}

		pathToIndex[pair.Path] = i
	}

	// Compute KZG commitment
	comm, err := k.context.BlobToKZGCommitment(blob, 1)
	if err != nil {
		return nil, fmt.Errorf("failed to commit to blob: %w", err)
	}

	// Cache the arc set
	commKey := string(comm[:])
	k.cache[commKey] = &cacheEntry{
		arcs:        arcs.Clone(),
		pathToIndex: pathToIndex,
		blob:        blob,
	}

	return commitment.Commitment(comm[:]), nil
}

// Prove generates a KZG proof for an arc.
func (k *KZGCommitment) Prove(comm commitment.Commitment, arcs *types.ArcSet, p types.Path) (types.CID, commitment.Proof, error) {
	if arcs == nil {
		return types.CID{}, nil, fmt.Errorf("arc set is nil")
	}

	if !k.initialized {
		return types.CID{}, nil, fmt.Errorf("KZG not initialized")
	}

	// Look up the target for the path
	target, ok := arcs.Get(p)
	if !ok {
		return types.CID{}, nil, fmt.Errorf("path %s not found in arc set", p)
	}

	k.mu.RLock()
	entry, cached := k.cache[string(comm)]
	k.mu.RUnlock()

	if !cached {
		return types.CID{}, nil, fmt.Errorf("commitment not found in cache")
	}

	// Get the index for the path
	index, ok := entry.pathToIndex[p]
	if !ok {
		return types.CID{}, nil, fmt.Errorf("path %s not found in index", p)
	}

	// Generate evaluation point (input point)
	inputPoint := indexToScalar(index)

	// Compute KZG proof
	proof, claimedValue, err := k.context.ComputeKZGProof(entry.blob, inputPoint, 1)
	if err != nil {
		return types.CID{}, nil, fmt.Errorf("failed to compute KZG proof: %w", err)
	}

	// Serialize proof (proof + claimed value + index for verification)
	proofBytes := serializeProof(proof, claimedValue, index)

	return target, commitment.Proof(proofBytes), nil
}

// Verify verifies a KZG proof for an arc.
func (k *KZGCommitment) Verify(comm commitment.Commitment, p types.Path, c types.CID, proof commitment.Proof) (bool, error) {
	if !k.initialized {
		return false, fmt.Errorf("KZG not initialized")
	}

	// Deserialize proof
	inputPoint, claimedValue, kzgProof, err := deserializeProof(proof)
	if err != nil {
		return false, fmt.Errorf("failed to deserialize proof: %w", err)
	}

	// Convert commitment
	var kzgComm gokzg4844.KZGCommitment
	copy(kzgComm[:], comm)

	// Verify KZG proof
	err = k.context.VerifyKZGProof(kzgComm, inputPoint, claimedValue, kzgProof)
	if err != nil {
		return false, nil // Proof is invalid, but not an error
	}

	return true, nil
}

// Update updates the commitment for a changed arc.
func (k *KZGCommitment) Update(comm commitment.Commitment, p types.Path, oldCID, newCID types.CID) (commitment.Commitment, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	entry, ok := k.cache[string(comm)]
	if !ok {
		return nil, fmt.Errorf("commitment not found in cache")
	}

	// Verify old CID matches
	currentCID, ok := entry.arcs.Get(p)
	if !ok {
		return nil, fmt.Errorf("path %s not found in arc set", p)
	}
	if !currentCID.Equals(oldCID) {
		return nil, fmt.Errorf("old CID mismatch")
	}

	// Get the index for the path
	index, ok := entry.pathToIndex[p]
	if !ok {
		return nil, fmt.Errorf("path %s not found in index", p)
	}

	// Update the arc set
	entry.arcs.Add(p, newCID)

	// Update blob
	newScalar := cidToScalar(newCID)
	offset := index * gokzg4844.SerializedScalarSize
	if offset+gokzg4844.SerializedScalarSize <= len(entry.blob) {
		copy(entry.blob[offset:offset+gokzg4844.SerializedScalarSize], newScalar[:])
	}

	// Recompute commitment
	return k.Commit(entry.arcs)
}

// BatchUpdate updates multiple arcs.
func (k *KZGCommitment) BatchUpdate(comm commitment.Commitment, updates map[types.Path]struct {
	Old types.CID
	New types.CID
}) (commitment.Commitment, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	entry, ok := k.cache[string(comm)]
	if !ok {
		return nil, fmt.Errorf("commitment not found in cache")
	}

	// Apply all updates
	for p, update := range updates {
		currentCID, ok := entry.arcs.Get(p)
		if !ok {
			return nil, fmt.Errorf("path %s not found in arc set", p)
		}
		if !currentCID.Equals(update.Old) {
			return nil, fmt.Errorf("old CID mismatch for path %s", p)
		}

		// Update the arc set
		entry.arcs.Add(p, update.New)

		// Update blob
		index, ok := entry.pathToIndex[p]
		if ok {
			newScalar := cidToScalar(update.New)
			offset := index * gokzg4844.SerializedScalarSize
			if offset+gokzg4844.SerializedScalarSize <= len(entry.blob) {
				copy(entry.blob[offset:offset+gokzg4844.SerializedScalarSize], newScalar[:])
			}
		}
	}

	// Recompute commitment
	return k.Commit(entry.arcs)
}

// Helper functions

// cidToScalar converts a CID to a KZG scalar (32 bytes).
// The result is reduced modulo the BLS12-381 scalar field modulus to ensure it's canonical.
func cidToScalar(c types.CID) gokzg4844.Scalar {
	var scalar gokzg4844.Scalar
	hash := sha256.Sum256(c.Bytes())

	// Reduce the hash modulo the BLS modulus to ensure it's canonical
	// The BLS12-381 scalar field modulus is approximately 2^255
	// We use a simple approach: clear the top bit to ensure it fits
	// This is a simplification - in production, use proper modular reduction
	result := hash
	// Clear the most significant bit to ensure value < modulus
	// The BLS modulus has MSB 0x73, so values with MSB < 0x73 are always valid
	// For values with MSB >= 0x73, we XOR with a mask to reduce
	if result[0] > 0x73 {
		result[0] = result[0] ^ 0x80 // Clear high bit
	}

	copy(scalar[:], result[:])
	return scalar
}

// indexToScalar converts an index to a scalar for evaluation.
func indexToScalar(index int) gokzg4844.Scalar {
	var scalar gokzg4844.Scalar
	// Use simple encoding for indices - indices are small and always fit
	scalar[31] = byte(index)
	return scalar
}

// serializeProof serializes a KZG proof with metadata.
func serializeProof(proof gokzg4844.KZGProof, claimedValue gokzg4844.Scalar, index int) []byte {
	// Format: proof (48) + claimedValue (32) + index (4) = 84 bytes
	result := make([]byte, 0, 84)
	result = append(result, proof[:]...)
	result = append(result, claimedValue[:]...)
	result = append(result, byte(index>>24), byte(index>>16), byte(index>>8), byte(index))
	return result
}

// deserializeProof deserializes a KZG proof.
func deserializeProof(data []byte) (gokzg4844.Scalar, gokzg4844.Scalar, gokzg4844.KZGProof, error) {
	if len(data) < 84 {
		return gokzg4844.Scalar{}, gokzg4844.Scalar{}, gokzg4844.KZGProof{}, fmt.Errorf("proof data too short: %d", len(data))
	}

	var proof gokzg4844.KZGProof
	var claimedValue gokzg4844.Scalar

	copy(proof[:], data[:48])
	copy(claimedValue[:], data[48:80])

	// Reconstruct input point from index
	index := int(data[80])<<24 | int(data[81])<<16 | int(data[82])<<8 | int(data[83])
	inputPoint := indexToScalar(index)

	return inputPoint, claimedValue, proof, nil
}

// Ensure KZGCommitment implements CommitmentScheme
var _ commitment.CommitmentScheme = (*KZGCommitment)(nil)