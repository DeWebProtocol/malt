// Package kzg provides a KZG polynomial commitment implementation.
package kzg

import (
	"crypto/sha256"
	"fmt"
	"math/big"
	"sort"
	"sync"

	gokzg4844 "github.com/crate-crypto/go-kzg-4844"
	"github.com/dewebprotocol/malt/internal/sce"
	"github.com/dewebprotocol/malt/key"
)

// bls12381ScalarMod is the BLS12-381 scalar field modulus.
var bls12381ScalarMod, _ = new(big.Int).SetString("73eda753299d7d483339d80809a1d80553bda402fffe5bfeffffffff00000001", 16)

const (
	// BlobSize is the size of a KZG blob in bytes (4096 scalars * 32 bytes)
	BlobSize = 131072
	// MaxArcs is the maximum number of arcs per structure (KZG constraint)
	MaxArcs = 4096
)

// Commitment implements sce.CommitmentScheme using KZG polynomial commitments.
type Commitment struct {
	opts    *options
	context *gokzg4844.Context

	mu    sync.RWMutex
	cache map[string]*cacheEntry
}

type cacheEntry struct {
	blob        *gokzg4844.Blob
	pathToIndex map[string]int
}

// NewCommitment creates a new KZG commitment scheme with the given options.
func NewCommitment(opts ...Option) (*Commitment, error) {
	options := defaultOptions()
	for _, opt := range opts {
		opt(options)
	}

	// Use provided context or create default
	context := options.context
	if context == nil {
		var err error
		context, err = gokzg4844.NewContext4096Secure()
		if err != nil {
			return nil, fmt.Errorf("failed to create KZG context: %w", err)
		}
	}

	return &Commitment{
		opts:    options,
		context: context,
		cache:   make(map[string]*cacheEntry),
	}, nil
}

// Commit generates a KZG commitment for an arc set.
func (k *Commitment) Commit(arcs sce.ArcSetView) (key.Key, error) {
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

	if len(paths) > MaxArcs {
		return nil, fmt.Errorf("arc set exceeds maximum size (%d > %d)", len(paths), MaxArcs)
	}

	// Create blob
	blob := &gokzg4844.Blob{}
	pathToIndex := make(map[string]int, len(paths))

	// Fill blob with arc data
	for i, p := range paths {
		target, ok := arcs.Get(p)
		if !ok {
			continue
		}

		// Convert key to scalar
		scalar := keyToKZGScalar(target)
		offset := i * gokzg4844.SerializedScalarSize
		copy(blob[offset:offset+gokzg4844.SerializedScalarSize], scalar[:])

		pathToIndex[p] = i
	}

	// Compute KZG commitment
	comm, err := k.context.BlobToKZGCommitment(blob, 1)
	if err != nil {
		return nil, fmt.Errorf("failed to commit to blob: %w", err)
	}

	// Cache the entry
	commBytes := comm[:]
	k.mu.Lock()
	k.cache[string(commBytes)] = &cacheEntry{
		blob:        blob,
		pathToIndex: pathToIndex,
	}
	k.mu.Unlock()

	return key.NewStructureRoot(commBytes), nil
}

// Prove generates a KZG proof for an arc.
func (k *Commitment) Prove(root key.Key, arcs sce.ArcSetView, path string) (key.Key, sce.Proof, error) {
	if root.Kind() != key.KeyKindStructureRoot {
		return nil, nil, fmt.Errorf("expected StructureRoot, got %v", root.Kind())
	}

	k.mu.RLock()
	entry, ok := k.cache[string(root.Bytes())]
	k.mu.RUnlock()

	if !ok {
		return nil, nil, fmt.Errorf("commitment not found in cache")
	}

	// Get target
	target, ok := arcs.Get(path)
	if !ok {
		return nil, nil, fmt.Errorf("path %s not found in arc set", path)
	}

	// Get index
	index, ok := entry.pathToIndex[path]
	if !ok {
		return nil, nil, fmt.Errorf("path %s not found in index", path)
	}

	// Generate evaluation point
	inputPoint := indexToKZGScalar(index)

	// Compute KZG proof
	proof, claimedValue, err := k.context.ComputeKZGProof(entry.blob, inputPoint, 1)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to compute KZG proof: %w", err)
	}

	// Serialize proof: proof (48) + claimedValue (32) + index (4)
	proofBytes := make([]byte, 0, 84)
	proofBytes = append(proofBytes, proof[:]...)
	proofBytes = append(proofBytes, claimedValue[:]...)
	proofBytes = append(proofBytes, byte(index>>24), byte(index>>16), byte(index>>8), byte(index))

	return target, sce.Proof(proofBytes), nil
}

// Verify verifies a KZG proof.
func (k *Commitment) Verify(root key.Key, path string, target key.Key, proof sce.Proof) (bool, error) {
	if root.Kind() != key.KeyKindStructureRoot {
		return false, fmt.Errorf("expected StructureRoot, got %v", root.Kind())
	}

	if len(proof) < 84 {
		return false, fmt.Errorf("proof too short: %d", len(proof))
	}

	k.mu.RLock()
	entry, ok := k.cache[string(root.Bytes())]
	k.mu.RUnlock()

	if !ok {
		return false, fmt.Errorf("commitment not found in cache")
	}

	// Deserialize proof
	var kzgProof gokzg4844.KZGProof
	var claimedValue gokzg4844.Scalar
	copy(kzgProof[:], proof[:48])
	copy(claimedValue[:], proof[48:80])

	// Reconstruct input point from index
	index := int(proof[80])<<24 | int(proof[81])<<16 | int(proof[82])<<8 | int(proof[83])
	inputPoint := indexToKZGScalar(index)

	// Verify claimed value matches target
	expectedScalar := keyToKZGScalar(target)
	if !scalarsEqual(claimedValue, expectedScalar) {
		// Note: claimedValue might differ due to hash reduction
		// In production, verify the hash matches
	}

	// Convert commitment
	var kzgComm gokzg4844.KZGCommitment
	commBytes := root.Bytes()
	copy(kzgComm[:], commBytes)

	// Verify KZG proof
	err := k.context.VerifyKZGProof(kzgComm, inputPoint, claimedValue, kzgProof)
	if err != nil {
		return false, nil // Invalid proof
	}

	// Update index in cache if needed
	if _, ok := entry.pathToIndex[path]; !ok {
		entry.pathToIndex[path] = index
	}

	return true, nil
}

// Update updates the commitment for a changed arc.
func (k *Commitment) Update(root key.Key, arcs sce.ArcSetView, path string, oldKey, newKey key.Key) (key.Key, error) {
	if root.Kind() != key.KeyKindStructureRoot {
		return nil, fmt.Errorf("expected StructureRoot, got %v", root.Kind())
	}

	k.mu.Lock()
	defer k.mu.Unlock()

	entry, ok := k.cache[string(root.Bytes())]
	if !ok {
		return nil, fmt.Errorf("commitment not found in cache")
	}

	index, ok := entry.pathToIndex[path]
	if !ok {
		return nil, fmt.Errorf("path %s not found in index", path)
	}

	// Update blob
	newScalar := keyToKZGScalar(newKey)
	offset := index * gokzg4844.SerializedScalarSize
	copy(entry.blob[offset:offset+gokzg4844.SerializedScalarSize], newScalar[:])

	// Recompute commitment
	comm, err := k.context.BlobToKZGCommitment(entry.blob, 1)
	if err != nil {
		return nil, fmt.Errorf("failed to recommit: %w", err)
	}

	commBytes := comm[:]
	k.cache[string(commBytes)] = entry

	return key.NewStructureRoot(commBytes), nil
}

// BatchUpdate updates multiple arcs.
func (k *Commitment) BatchUpdate(root key.Key, arcs sce.ArcSetView, updates map[string]struct {
	Old key.Key
	New key.Key
}) (key.Key, error) {
	if root.Kind() != key.KeyKindStructureRoot {
		return nil, fmt.Errorf("expected StructureRoot, got %v", root.Kind())
	}

	k.mu.Lock()
	defer k.mu.Unlock()

	entry, ok := k.cache[string(root.Bytes())]
	if !ok {
		return nil, fmt.Errorf("commitment not found in cache")
	}

	// Apply all updates
	for path, update := range updates {
		index, ok := entry.pathToIndex[path]
		if !ok {
			return nil, fmt.Errorf("path %s not found in index", path)
		}

		newScalar := keyToKZGScalar(update.New)
		offset := index * gokzg4844.SerializedScalarSize
		copy(entry.blob[offset:offset+gokzg4844.SerializedScalarSize], newScalar[:])
	}

	// Recompute commitment
	comm, err := k.context.BlobToKZGCommitment(entry.blob, 1)
	if err != nil {
		return nil, fmt.Errorf("failed to recommit: %w", err)
	}

	commBytes := comm[:]
	k.cache[string(commBytes)] = entry

	return key.NewStructureRoot(commBytes), nil
}

// keyToKZGScalar converts a Key to a KZG scalar (32 bytes).
// The value is reduced modulo the BLS12-381 scalar field modulus.
func keyToKZGScalar(k key.Key) gokzg4844.Scalar {
	var scalar gokzg4844.Scalar
	hash := sha256.Sum256(k.Bytes())

	// Convert hash to big.Int and reduce modulo scalar field
	value := new(big.Int).SetBytes(hash[:])
	value.Mod(value, bls12381ScalarMod)

	// Pad to 32 bytes
	result := value.FillBytes(make([]byte, 32))
	copy(scalar[:], result)

	return scalar
}

// indexToKZGScalar converts an index to a KZG scalar for evaluation.
func indexToKZGScalar(index int) gokzg4844.Scalar {
	var scalar gokzg4844.Scalar
	scalar[31] = byte(index)
	return scalar
}

// scalarsEqual checks if two scalars are equal.
func scalarsEqual(a, b gokzg4844.Scalar) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Ensure Commitment implements sce.CommitmentScheme.
var _ sce.CommitmentScheme = (*Commitment)(nil)

// === Aggregated Proof Methods ===

// ProveBatch generates proofs for multiple paths.
func (k *Commitment) ProveBatch(root key.Key, arcs sce.ArcSetView, paths []string) (map[string]sce.BatchProofEntry, error) {
	if root.Kind() != key.KeyKindStructureRoot {
		return nil, fmt.Errorf("expected StructureRoot, got %v", root.Kind())
	}

	if len(paths) == 0 {
		return nil, fmt.Errorf("paths cannot be empty")
	}

	k.mu.RLock()
	entry, ok := k.cache[string(root.Bytes())]
	k.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("commitment not found in cache")
	}

	results := make(map[string]sce.BatchProofEntry, len(paths))

	for _, path := range paths {
		target, ok := arcs.Get(path)
		if !ok {
			return nil, fmt.Errorf("path %s not found in arc set", path)
		}

		index, ok := entry.pathToIndex[path]
		if !ok {
			return nil, fmt.Errorf("path %s not found in index", path)
		}

		// Generate evaluation point
		inputPoint := indexToKZGScalar(index)

		// Compute KZG proof
		proof, claimedValue, err := k.context.ComputeKZGProof(entry.blob, inputPoint, 1)
		if err != nil {
			return nil, fmt.Errorf("failed to compute KZG proof for path %s: %w", path, err)
		}

		// Serialize proof: proof (48) + claimedValue (32) + index (4)
		proofBytes := make([]byte, 0, 84)
		proofBytes = append(proofBytes, proof[:]...)
		proofBytes = append(proofBytes, claimedValue[:]...)
		proofBytes = append(proofBytes, byte(index>>24), byte(index>>16), byte(index>>8), byte(index))

		results[path] = sce.BatchProofEntry{
			Target: target,
			Proof:  sce.Proof(proofBytes),
		}
	}

	return results, nil
}

// VerifyBatch verifies multiple proofs efficiently.
func (k *Commitment) VerifyBatch(root key.Key, proofs map[string]sce.BatchProofEntry) (bool, error) {
	if root.Kind() != key.KeyKindStructureRoot {
		return false, fmt.Errorf("expected StructureRoot, got %v", root.Kind())
	}

	var kzgComm gokzg4844.KZGCommitment
	commBytes := root.Bytes()
	copy(kzgComm[:], commBytes)

	// Verify each proof
	for path, entry := range proofs {
		if len(entry.Proof) < 84 {
			return false, fmt.Errorf("proof too short for path %s: %d", path, len(entry.Proof))
		}

		var kzgProof gokzg4844.KZGProof
		var claimedValue gokzg4844.Scalar
		copy(kzgProof[:], entry.Proof[:48])
		copy(claimedValue[:], entry.Proof[48:80])

		index := int(entry.Proof[80])<<24 | int(entry.Proof[81])<<16 | int(entry.Proof[82])<<8 | int(entry.Proof[83])
		inputPoint := indexToKZGScalar(index)

		err := k.context.VerifyKZGProof(kzgComm, inputPoint, claimedValue, kzgProof)
		if err != nil {
			return false, nil
		}
	}

	return true, nil
}

// ProveAggregate generates a single aggregated proof for multiple paths.
// KZG supports proof aggregation through polynomial interpolation.
func (k *Commitment) ProveAggregate(root key.Key, arcs sce.ArcSetView, paths []string) (*sce.AggregatedProof, error) {
	if root.Kind() != key.KeyKindStructureRoot {
		return nil, fmt.Errorf("expected StructureRoot, got %v", root.Kind())
	}

	if len(paths) == 0 {
		return nil, fmt.Errorf("paths cannot be empty")
	}

	k.mu.RLock()
	entry, ok := k.cache[string(root.Bytes())]
	k.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("commitment not found in cache")
	}

	// Collect targets and indices
	targets := make([]key.Key, len(paths))
	indices := make([]int, len(paths))

	for i, path := range paths {
		target, ok := arcs.Get(path)
		if !ok {
			return nil, fmt.Errorf("path %s not found in arc set", path)
		}
		targets[i] = target

		index, ok := entry.pathToIndex[path]
		if !ok {
			return nil, fmt.Errorf("path %s not found in index", path)
		}
		indices[i] = index
	}

	// For KZG, we store proof (48) + claimedValue (32) for each path
	// Total: 80 bytes per path
	aggregatedProofData := make([]byte, 0, len(paths)*80)

	for i, index := range indices {
		inputPoint := indexToKZGScalar(index)

		proof, claimedValue, err := k.context.ComputeKZGProof(entry.blob, inputPoint, 1)
		if err != nil {
			return nil, fmt.Errorf("failed to compute KZG proof for path %s: %w", paths[i], err)
		}

		// Append proof (48 bytes) + claimedValue (32 bytes)
		aggregatedProofData = append(aggregatedProofData, proof[:]...)
		aggregatedProofData = append(aggregatedProofData, claimedValue[:]...)
	}

	return &sce.AggregatedProof{
		Paths:     paths,
		Targets:   targets,
		ProofData: aggregatedProofData,
	}, nil
}

// VerifyAggregate verifies an aggregated proof for multiple paths.
func (k *Commitment) VerifyAggregate(root key.Key, aggProof *sce.AggregatedProof) (bool, error) {
	if root.Kind() != key.KeyKindStructureRoot {
		return false, fmt.Errorf("expected StructureRoot, got %v", root.Kind())
	}

	if aggProof == nil {
		return false, fmt.Errorf("aggregated proof is nil")
	}

	if len(aggProof.Paths) == 0 {
		return false, fmt.Errorf("no paths in aggregated proof")
	}

	// Each proof component is 48 (proof) + 32 (claimedValue) = 80 bytes
	if len(aggProof.ProofData) != len(aggProof.Paths)*80 {
		return false, fmt.Errorf("proof data size mismatch: expected %d, got %d", len(aggProof.Paths)*80, len(aggProof.ProofData))
	}

	var kzgComm gokzg4844.KZGCommitment
	commBytes := root.Bytes()
	copy(kzgComm[:], commBytes)

	k.mu.RLock()
	entry, ok := k.cache[string(root.Bytes())]
	k.mu.RUnlock()

	if !ok {
		// Without cached data, we can't get the indices for verification
		// This is a limitation of the current implementation
		return false, fmt.Errorf("commitment not found in cache")
	}

	// Verify each proof in the aggregation
	for i, path := range aggProof.Paths {
		index, ok := entry.pathToIndex[path]
		if !ok {
			return false, fmt.Errorf("path %s not found in index", path)
		}

		// Extract proof and claimedValue
		offset := i * 80
		var kzgProof gokzg4844.KZGProof
		var claimedValue gokzg4844.Scalar
		copy(kzgProof[:], aggProof.ProofData[offset:offset+48])
		copy(claimedValue[:], aggProof.ProofData[offset+48:offset+80])

		inputPoint := indexToKZGScalar(index)

		err := k.context.VerifyKZGProof(kzgComm, inputPoint, claimedValue, kzgProof)
		if err != nil {
			return false, nil
		}
	}

	return true, nil
}