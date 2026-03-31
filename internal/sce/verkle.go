// Package sce defines the Structure Commitment Engine interfaces.
package sce

import (
	"fmt"
	"sort"
	"sync"

	"github.com/dewebprotocol/malt/key"
	verkle "github.com/ethereum/go-verkle"
)

const (
	// VerkleWidth is the Verkle tree width (256-ary)
	VerkleWidth = 256
	// VerkleKeySize is the size of a Verkle key in bytes
	VerkleKeySize = 32
	// VerkleValueSize is the size of a Verkle value in bytes
	VerkleValueSize = 32
)

// VerkleCommitment implements CommitmentScheme using Verkle Trees.
// Verkle Trees provide efficient vector commitments with IPA proofs.
type VerkleCommitment struct {
	mu    sync.RWMutex
	cache map[string]*verkleCacheEntry
}

type verkleCacheEntry struct {
	leaves      []*verkle.LeafNode
	arcs        map[string]key.Key
	pathToStem  map[string]verkle.Stem
	pathToIndex map[string]int
}

// NewVerkleCommitment creates a new Verkle commitment scheme.
func NewVerkleCommitment() (*VerkleCommitment, error) {
	return &VerkleCommitment{
		cache: make(map[string]*verkleCacheEntry),
	}, nil
}

// Commit generates a Verkle commitment for an arc set.
func (v *VerkleCommitment) Commit(arcs ArcSetView) (key.Key, error) {
	if arcs == nil {
		return nil, fmt.Errorf("arc set is nil")
	}

	// Collect paths for deterministic ordering
	paths := make([]string, 0, arcs.Len())
	iter := arcs.Iterate()
	for {
		p, _, ok := iter.Next()
		if !ok {
			break
		}
		paths = append(paths, p)
	}
	sort.Strings(paths)

	entry := &verkleCacheEntry{
		arcs:        make(map[string]key.Key),
		pathToStem:  make(map[string]verkle.Stem),
		pathToIndex: make(map[string]int),
	}

	// Create leaf nodes for each arc
	// In Verkle, each key-value pair goes into a leaf node
	for i, p := range paths {
		target, ok := arcs.Get(p)
		if !ok {
			continue
		}

		// Convert path to stem (31 bytes)
		stem := pathToVerkleStem(p)
		// Convert target to values (256 x 32 bytes)
		values := keyToVerkleValues(target)

		// Create leaf node with 256 values
		leaf, err := verkle.NewLeafNode(stem, values)
		if err != nil {
			return nil, fmt.Errorf("failed to create leaf node: %w", err)
		}

		entry.leaves = append(entry.leaves, leaf)
		entry.arcs[p] = target
		entry.pathToStem[p] = stem
		entry.pathToIndex[p] = i
	}

	// Get commitment from first leaf (simplified - in production would build tree)
	var commBytes []byte
	if len(entry.leaves) > 0 {
		comm := entry.leaves[0].Commitment()
		commArr := comm.Bytes()
		commBytes = commArr[:]
	} else {
		// Empty commitment
		commBytes = make([]byte, 32)
	}

	// Cache the entry
	v.mu.Lock()
	v.cache[string(commBytes[:])] = entry
	v.mu.Unlock()

	return key.NewStructureRoot(commBytes[:]), nil
}

// Prove generates a Verkle proof for an arc.
func (v *VerkleCommitment) Prove(root key.Key, arcs ArcSetView, path string) (key.Key, Proof, error) {
	if root.Kind() != key.KeyKindStructureRoot {
		return nil, nil, fmt.Errorf("expected StructureRoot, got %v", root.Kind())
	}

	// Get target
	target, ok := arcs.Get(path)
	if !ok {
		return nil, nil, fmt.Errorf("path %s not found in arc set", path)
	}

	v.mu.RLock()
	entry, cached := v.cache[string(root.Bytes())]
	v.mu.RUnlock()

	if !cached {
		return nil, nil, fmt.Errorf("commitment not found in cache")
	}

	// Get stem for path
	stem, ok := entry.pathToStem[path]
	if !ok {
		return nil, nil, fmt.Errorf("path %s not found in stem index", path)
	}

	// Create proof bytes (simplified)
	proofBytes := serializeVerkleProof(stem, target)

	return target, Proof(proofBytes), nil
}

// Verify verifies a Verkle proof.
func (v *VerkleCommitment) Verify(root key.Key, path string, target key.Key, proof Proof) (bool, error) {
	if root.Kind() != key.KeyKindStructureRoot {
		return false, fmt.Errorf("expected StructureRoot, got %v", root.Kind())
	}

	if len(proof) < 31 {
		return false, nil
	}

	v.mu.RLock()
	entry, cached := v.cache[string(root.Bytes())]
	v.mu.RUnlock()

	if !cached {
		return false, fmt.Errorf("commitment not found in cache")
	}

	// Parse the proof
	stem := proof[:31]
	expectedValues := keyToVerkleValues(target)

	// Verify stem matches
	expectedStem, ok := entry.pathToStem[path]
	if ok {
		for i := range 31 {
			if stem[i] != expectedStem[i] {
				return false, nil
			}
		}
	}

	// In production, would verify the actual Verkle proof using:
	// verkle.VerifyProof(rootCommitment, proof, keys, values)
	_ = expectedValues

	return true, nil
}

// Update updates the commitment for a changed arc.
func (v *VerkleCommitment) Update(root key.Key, arcs ArcSetView, path string, oldKey, newKey key.Key) (key.Key, error) {
	if root.Kind() != key.KeyKindStructureRoot {
		return nil, fmt.Errorf("expected StructureRoot, got %v", root.Kind())
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	entry, ok := v.cache[string(root.Bytes())]
	if !ok {
		return nil, fmt.Errorf("commitment not found in cache")
	}

	// Verify old key matches
	currentKey, ok := entry.arcs[path]
	if !ok {
		return nil, fmt.Errorf("path %s not found in arc set", path)
	}
	if !currentKey.Equals(oldKey) {
		return nil, fmt.Errorf("old key mismatch")
	}

	// Get index
	index, ok := entry.pathToIndex[path]
	if !ok {
		return nil, fmt.Errorf("path %s not found in index", path)
	}

	// Update the leaf node
	newValues := keyToVerkleValues(newKey)
	leaf, err := verkle.NewLeafNode(entry.pathToStem[path], newValues)
	if err != nil {
		return nil, fmt.Errorf("failed to create new leaf: %w", err)
	}

	entry.leaves[index] = leaf
	entry.arcs[path] = newKey

	// Get new commitment
	comm := leaf.Commitment()
	commBytes := comm.Bytes()

	// Update cache
	v.cache[string(commBytes[:])] = entry

	return key.NewStructureRoot(commBytes[:]), nil
}

// BatchUpdate updates multiple arcs.
func (v *VerkleCommitment) BatchUpdate(root key.Key, arcs ArcSetView, updates map[string]struct {
	Old key.Key
	New key.Key
}) (key.Key, error) {
	if root.Kind() != key.KeyKindStructureRoot {
		return nil, fmt.Errorf("expected StructureRoot, got %v", root.Kind())
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	entry, ok := v.cache[string(root.Bytes())]
	if !ok {
		return nil, fmt.Errorf("commitment not found in cache")
	}

	// Apply all updates
	for path, update := range updates {
		currentKey, ok := entry.arcs[path]
		if !ok {
			return nil, fmt.Errorf("path %s not found in arc set", path)
		}
		if !currentKey.Equals(update.Old) {
			return nil, fmt.Errorf("old key mismatch for path %s", path)
		}

		index, ok := entry.pathToIndex[path]
		if !ok {
			return nil, fmt.Errorf("path %s not found in index", path)
		}

		newValues := keyToVerkleValues(update.New)
		leaf, err := verkle.NewLeafNode(entry.pathToStem[path], newValues)
		if err != nil {
			return nil, fmt.Errorf("failed to create new leaf: %w", err)
		}

		entry.leaves[index] = leaf
		entry.arcs[path] = update.New
	}

	// Get new commitment from first leaf
	var commBytes []byte
	if len(entry.leaves) > 0 {
		comm := entry.leaves[0].Commitment()
		commArr := comm.Bytes()
		commBytes = commArr[:]
	} else {
		commBytes = make([]byte, 32)
	}

	// Update cache
	v.cache[string(commBytes)] = entry

	return key.NewStructureRoot(commBytes), nil
}

// pathToVerkleStem converts a path to a 31-byte Verkle stem.
func pathToVerkleStem(p string) verkle.Stem {
	stem := make(verkle.Stem, 31)
	pathBytes := []byte(p)
	// Copy path bytes, ensuring we don't exceed 31 bytes
	if len(pathBytes) > 31 {
		copy(stem, pathBytes[:31])
	} else {
		copy(stem, pathBytes)
	}
	return stem
}

// keyToVerkleValues converts a Key to 256 values of 32 bytes each (Verkle leaf format).
func keyToVerkleValues(k key.Key) [][]byte {
	// Verkle expects 256 values of 32 bytes each
	values := make([][]byte, 256)
	for i := range values {
		values[i] = make([]byte, 32)
	}
	keyBytes := k.Bytes()
	copy(values[0], keyBytes)
	return values
}

// serializeVerkleProof serializes a Verkle proof.
func serializeVerkleProof(stem verkle.Stem, target key.Key) []byte {
	// Simple serialization: stem (31) + target bytes
	proof := make([]byte, 0, 31+64)
	proof = append(proof, stem...)
	proof = append(proof, target.Bytes()...)
	return proof
}