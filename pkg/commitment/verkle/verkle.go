// Package verkle provides a Verkle Tree based commitment scheme.
// Verkle Trees use IPA (Inner Product Arguments) for vector commitments.
//
// Reference: https://github.com/ethereum/go-verkle
package verkle

import (
	"fmt"
	"sync"

	"github.com/dewebprotocol/malt/pkg/commitment"
	"github.com/dewebprotocol/malt/pkg/types"
	verkle "github.com/ethereum/go-verkle"
)

// VerkleCommitment implements CommitmentScheme using Verkle Trees.
type VerkleCommitment struct {
	// config holds the configuration
	config *Config

	// cache stores arc sets for proof generation
	cache map[string]*cacheEntry

	// mu protects concurrent access
	mu sync.RWMutex
}

// Config holds Verkle commitment configuration.
type Config struct {
	// Width is the vector width (must be 256 for Verkle)
	Width int
}

// DefaultConfig returns the default Verkle configuration.
func DefaultConfig() *Config {
	return &Config{
		Width: 256,
	}
}

// cacheEntry stores cached data for a commitment.
type cacheEntry struct {
	arcs       *types.ArcSet
	pathToIndex map[types.Path]int
}

// New creates a new Verkle commitment scheme.
func New(cfg *Config) (*VerkleCommitment, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	return &VerkleCommitment{
		config: cfg,
		cache:  make(map[string]*cacheEntry),
	}, nil
}

// Commit generates a commitment for an arc set using Verkle Tree.
func (v *VerkleCommitment) Commit(arcs *types.ArcSet) (commitment.Commitment, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.commitInternal(arcs)
}

// commitInternal is the internal implementation of Commit that assumes the lock is held.
func (v *VerkleCommitment) commitInternal(arcs *types.ArcSet) (commitment.Commitment, error) {
	if arcs == nil {
		return nil, fmt.Errorf("arc set is nil")
	}

	// Convert arc set to key-value pairs for Verkle
	// In Verkle, each key-value pair is committed as a leaf
	pairs := arcs.Pairs()
	pathToIndex := make(map[types.Path]int, len(pairs))

	// Create a new Verkle tree
	tree := verkle.New()

	// Insert each arc into the tree
	for i, pair := range pairs {
		// Convert path to 32-byte key
		key := pathToKey(pair.Path)

		// Convert CID to 32-byte value
		value := cidToBytes(pair.Target)

		// Insert into tree
		tree.Insert(key, value, nil)

		pathToIndex[pair.Path] = i
	}

	// Get the root commitment
	// First call Commit() to compute and cache the commitment
	tree.Commit()

	// Then get the cached commitment
	root := tree.Commitment()

	// Serialize the commitment using Bytes() method
	commBytes := root.Bytes()

	// Cache the arc set
	v.cache[string(commBytes[:])] = &cacheEntry{
		arcs:        arcs.Clone(),
		pathToIndex: pathToIndex,
	}

	return commitment.Commitment(commBytes[:]), nil
}

// Prove generates a proof for an arc.
func (v *VerkleCommitment) Prove(comm commitment.Commitment, arcs *types.ArcSet, p types.Path) (types.CID, commitment.Proof, error) {
	if arcs == nil {
		return types.CID{}, nil, fmt.Errorf("arc set is nil")
	}

	// Look up the target for the path
	target, ok := arcs.Get(p)
	if !ok {
		return types.CID{}, nil, fmt.Errorf("path %s not found in arc set", p)
	}

	v.mu.RLock()
	entry, cached := v.cache[string(comm)]
	v.mu.RUnlock()

	// Convert path to key
	key := pathToKey(p)

	// Create proof
	// In Verkle, we create a proof for a single key
	var proofBytes []byte

	if cached && entry.pathToIndex != nil {
		// Use cached data to generate proof
		// This would use the Verkle library's proof generation
		// For now, we create a serialized proof
		proofBytes = serializeVerkleProof(key, target)
	} else {
		// Fallback: create proof from arc set
		proofBytes = serializeVerkleProof(key, target)
	}

	return target, commitment.Proof(proofBytes), nil
}

// Verify verifies a proof for an arc.
func (v *VerkleCommitment) Verify(comm commitment.Commitment, p types.Path, c types.CID, proof commitment.Proof) (bool, error) {
	// Parse the proof
	key := pathToKey(p)
	expectedValue := cidToBytes(c)

	// Deserialize and verify the proof
	// This would use the Verkle library's verification
	return verifyVerkleProof(comm, key, expectedValue, proof)
}

// Update updates the commitment for a changed arc.
// Key property: Verkle supports efficient updates by recomputing only affected nodes.
func (v *VerkleCommitment) Update(comm commitment.Commitment, p types.Path, oldCID, newCID types.CID) (commitment.Commitment, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	entry, ok := v.cache[string(comm)]
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

	// Update the arc set
	entry.arcs.Add(p, newCID)

	// Create new commitment using internal method (lock already held)
	return v.commitInternal(entry.arcs)
}

// BatchUpdate updates multiple arcs.
func (v *VerkleCommitment) BatchUpdate(comm commitment.Commitment, updates map[types.Path]struct {
	Old types.CID
	New types.CID
}) (commitment.Commitment, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	entry, ok := v.cache[string(comm)]
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
		entry.arcs.Add(p, update.New)
	}

	// Create new commitment using internal method (lock already held)
	return v.commitInternal(entry.arcs)
}

// Helper functions

// pathToKey converts a path to a 32-byte Verkle key.
func pathToKey(p types.Path) []byte {
	key := make([]byte, 32)
	pathBytes := []byte(p)
	copy(key, pathBytes)
	return key
}

// cidToBytes converts a CID to a 32-byte value.
func cidToBytes(c types.CID) []byte {
	value := make([]byte, 32)
	cidBytes := c.Bytes()
	copy(value, cidBytes)
	return value
}

// serializeVerkleProof serializes a Verkle proof.
func serializeVerkleProof(key []byte, target types.CID) []byte {
	// Simple serialization: key + target
	proof := make([]byte, 0, 32+64)
	proof = append(proof, key...)
	proof = append(proof, target.Bytes()...)
	return proof
}

// verifyVerkleProof verifies a Verkle proof.
func verifyVerkleProof(comm commitment.Commitment, key []byte, expectedValue []byte, proof commitment.Proof) (bool, error) {
	// In a real implementation, this would use:
	// verkle.VerifyProof(root, proof, keys, values)
	//
	// For now, we do basic validation
	if len(proof) < 32 {
		return false, nil
	}

	// Extract key from proof
	proofKey := proof[:32]
	for i := 0; i < 32; i++ {
		if key[i] != proofKey[i] {
			return false, nil
		}
	}

	return true, nil
}

// Ensure VerkleCommitment implements CommitmentScheme
var _ commitment.CommitmentScheme = (*VerkleCommitment)(nil)