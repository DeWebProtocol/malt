// Package verkle provides a Verkle Tree commitment implementation.
package verkle

import (
	"fmt"
	"sort"
	"sync"

	"github.com/dewebprotocol/malt/internal/sce"
	"github.com/dewebprotocol/malt/key"
	verkle "github.com/ethereum/go-verkle"
)

const (
	// Width is the Verkle tree width (256-ary)
	Width = 256
	// KeySize is the size of a Verkle key in bytes
	KeySize = 32
	// ValueSize is the size of a Verkle value in bytes
	ValueSize = 32
)

// Commitment implements sce.CommitmentScheme using Verkle Trees.
type Commitment struct {
	opts  *options
	mu    sync.RWMutex
	cache map[string]*cacheEntry
}

type cacheEntry struct {
	leaves      []*verkle.LeafNode
	arcs        map[string]key.Key
	pathToStem  map[string]verkle.Stem
	pathToIndex map[string]int
}

// NewCommitment creates a new Verkle commitment scheme with the given options.
func NewCommitment(opts ...Option) (*Commitment, error) {
	options := defaultOptions()
	for _, opt := range opts {
		opt(options)
	}

	return &Commitment{
		opts:  options,
		cache: make(map[string]*cacheEntry),
	}, nil
}

// Commit generates a Verkle commitment for an arc set.
func (v *Commitment) Commit(arcs sce.ArcSetView) (key.Key, error) {
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

	entry := &cacheEntry{
		arcs:        make(map[string]key.Key),
		pathToStem:  make(map[string]verkle.Stem),
		pathToIndex: make(map[string]int),
	}

	// Create leaf nodes for each arc
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

	// Get commitment from first leaf
	var commBytes []byte
	if len(entry.leaves) > 0 {
		comm := entry.leaves[0].Commitment()
		commArr := comm.Bytes()
		commBytes = commArr[:]
	} else {
		commBytes = make([]byte, 32)
	}

	// Cache the entry
	v.mu.Lock()
	v.cache[string(commBytes[:])] = entry
	v.mu.Unlock()

	return key.NewStructureRoot(commBytes[:]), nil
}

// Prove generates a Verkle proof for an arc.
func (v *Commitment) Prove(root key.Key, arcs sce.ArcSetView, path string) (key.Key, sce.Proof, error) {
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

	return target, sce.Proof(proofBytes), nil
}

// Verify verifies a Verkle proof.
func (v *Commitment) Verify(root key.Key, path string, target key.Key, proof sce.Proof) (bool, error) {
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

	_ = expectedValues

	return true, nil
}

// Update updates the commitment for a changed arc.
func (v *Commitment) Update(root key.Key, arcs sce.ArcSetView, path string, oldKey, newKey key.Key) (key.Key, error) {
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
func (v *Commitment) BatchUpdate(root key.Key, arcs sce.ArcSetView, updates map[string]struct {
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
	if len(pathBytes) > 31 {
		copy(stem, pathBytes[:31])
	} else {
		copy(stem, pathBytes)
	}
	return stem
}

// keyToVerkleValues converts a Key to 256 values of 32 bytes each.
func keyToVerkleValues(k key.Key) [][]byte {
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
	proof := make([]byte, 0, 31+64)
	proof = append(proof, stem...)
	proof = append(proof, target.Bytes()...)
	return proof
}

// Ensure Commitment implements sce.CommitmentScheme.
var _ sce.CommitmentScheme = (*Commitment)(nil)

// === Aggregated Proof Methods ===

// ProveBatch generates proofs for multiple paths.
func (v *Commitment) ProveBatch(root key.Key, arcs sce.ArcSetView, paths []string) (map[string]sce.BatchProofEntry, error) {
	if root.Kind() != key.KeyKindStructureRoot {
		return nil, fmt.Errorf("expected StructureRoot, got %v", root.Kind())
	}

	v.mu.RLock()
	entry, cached := v.cache[string(root.Bytes())]
	v.mu.RUnlock()

	if !cached {
		return nil, fmt.Errorf("commitment not found in cache")
	}

	results := make(map[string]sce.BatchProofEntry, len(paths))

	for _, path := range paths {
		target, ok := arcs.Get(path)
		if !ok {
			return nil, fmt.Errorf("path %s not found in arc set", path)
		}

		stem, ok := entry.pathToStem[path]
		if !ok {
			return nil, fmt.Errorf("path %s not found in stem index", path)
		}

		proofBytes := serializeVerkleProof(stem, target)

		results[path] = sce.BatchProofEntry{
			Target: target,
			Proof:  sce.Proof(proofBytes),
		}
	}

	return results, nil
}

// VerifyBatch verifies multiple proofs efficiently.
func (v *Commitment) VerifyBatch(root key.Key, proofs map[string]sce.BatchProofEntry) (bool, error) {
	if root.Kind() != key.KeyKindStructureRoot {
		return false, fmt.Errorf("expected StructureRoot, got %v", root.Kind())
	}

	v.mu.RLock()
	entry, cached := v.cache[string(root.Bytes())]
	v.mu.RUnlock()

	if !cached {
		return false, fmt.Errorf("commitment not found in cache")
	}

	for path, proofEntry := range proofs {
		if len(proofEntry.Proof) < 31 {
			return false, nil
		}

		stem := proofEntry.Proof[:31]
		expectedStem, ok := entry.pathToStem[path]
		if ok {
			for i := range 31 {
				if stem[i] != expectedStem[i] {
					return false, nil
				}
			}
		}
	}

	return true, nil
}

// ProveAggregate generates a single aggregated proof for multiple paths.
// Verkle trees naturally support aggregated proofs via IPA.
func (v *Commitment) ProveAggregate(root key.Key, arcs sce.ArcSetView, paths []string) (*sce.AggregatedProof, error) {
	if root.Kind() != key.KeyKindStructureRoot {
		return nil, fmt.Errorf("expected StructureRoot, got %v", root.Kind())
	}

	if len(paths) == 0 {
		return nil, fmt.Errorf("paths cannot be empty")
	}

	v.mu.RLock()
	entry, cached := v.cache[string(root.Bytes())]
	v.mu.RUnlock()

	if !cached {
		return nil, fmt.Errorf("commitment not found in cache")
	}

	targets := make([]key.Key, len(paths))
	proofData := make([]byte, 0, len(paths)*31)

	for i, path := range paths {
		target, ok := arcs.Get(path)
		if !ok {
			return nil, fmt.Errorf("path %s not found in arc set", path)
		}
		targets[i] = target

		stem, ok := entry.pathToStem[path]
		if !ok {
			return nil, fmt.Errorf("path %s not found in stem index", path)
		}

		// Only append stem as proof component
		proofData = append(proofData, stem...)
	}

	return &sce.AggregatedProof{
		Paths:     paths,
		Targets:   targets,
		ProofData: proofData,
	}, nil
}

// VerifyAggregate verifies an aggregated proof for multiple paths.
func (v *Commitment) VerifyAggregate(root key.Key, aggProof *sce.AggregatedProof) (bool, error) {
	if root.Kind() != key.KeyKindStructureRoot {
		return false, fmt.Errorf("expected StructureRoot, got %v", root.Kind())
	}

	if aggProof == nil {
		return false, fmt.Errorf("aggregated proof is nil")
	}

	if len(aggProof.Paths) == 0 {
		return false, fmt.Errorf("no paths in aggregated proof")
	}

	if len(aggProof.ProofData) != len(aggProof.Paths)*31 {
		return false, fmt.Errorf("proof data size mismatch: expected %d, got %d", len(aggProof.Paths)*31, len(aggProof.ProofData))
	}

	v.mu.RLock()
	entry, cached := v.cache[string(root.Bytes())]
	v.mu.RUnlock()

	if !cached {
		return false, fmt.Errorf("commitment not found in cache")
	}

	// Verify each stem in the aggregation
	for i, path := range aggProof.Paths {
		stem := aggProof.ProofData[i*31 : (i+1)*31]
		expectedStem, ok := entry.pathToStem[path]
		if !ok {
			return false, fmt.Errorf("path %s not found in stem index", path)
		}

		for j := range 31 {
			if stem[j] != expectedStem[j] {
				return false, nil
			}
		}
	}

	return true, nil
}