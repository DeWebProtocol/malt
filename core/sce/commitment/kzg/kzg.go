// Package kzg provides a KZG polynomial commitment implementation.
package kzg

import (
	"crypto/sha256"
	"fmt"
	"math/big"
	"sync"

	gokzg4844 "github.com/crate-crypto/go-kzg-4844"
	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/sce/commitment"
	cid "github.com/ipfs/go-cid"
)

// bls12381ScalarMod is the BLS12-381 scalar field modulus.
var bls12381ScalarMod, _ = new(big.Int).SetString("73eda753299d7d483339d80809a1d80553bda402fffe5bfeffffffff00000001", 16)

const (
	// MaxValues is the maximum number of values per commitment (KZG constraint).
	MaxValues = 4096
	// ProofSize is the size of a KZG proof in bytes.
	ProofSize = 84
)

// Scheme implements commitment.Scheme using KZG polynomial commitments.
type Scheme struct {
	context *gokzg4844.Context

	mu    sync.RWMutex
	cache map[string]*cacheEntry
}

type cacheEntry struct {
	blob   *gokzg4844.Blob
	paths  []string
	values []cid.Cid
}

// NewScheme creates a new KZG commitment scheme.
func NewScheme() (*Scheme, error) {
	context, err := gokzg4844.NewContext4096Secure()
	if err != nil {
		return nil, fmt.Errorf("failed to create KZG context: %w", err)
	}

	return &Scheme{
		context: context,
		cache:   make(map[string]*cacheEntry),
	}, nil
}

// Commit generates a KZG commitment for the given arc set.
func (s *Scheme) Commit(arcs arcset.View) (cid.Cid, error) {
	paths, values := extractSortedPathsValues(arcs)

	if len(paths) > MaxValues {
		return cid.Cid{}, fmt.Errorf("too many values: %d > %d", len(paths), MaxValues)
	}

	// Create blob
	blob := &gokzg4844.Blob{}

	// Fill blob with values
	for i, value := range values {
		scalar := cidToKZGScalar(value)
		offset := i * gokzg4844.SerializedScalarSize
		copy(blob[offset:offset+gokzg4844.SerializedScalarSize], scalar[:])
	}

	// Compute KZG commitment
	comm, err := s.context.BlobToKZGCommitment(blob, 1)
	if err != nil {
		return cid.Cid{}, fmt.Errorf("failed to commit: %w", err)
	}

	// Cache the entry
	commBytes := comm[:]
	s.mu.Lock()
	s.cache[string(commBytes)] = &cacheEntry{
		blob:   blob,
		paths:  paths,
		values: values,
	}
	s.mu.Unlock()

	// Create MALT CID from commitment bytes
	return codec.NewKZGCid(commBytes)
}

// Prove generates a KZG proof for a value at the given path.
func (s *Scheme) Prove(comm cid.Cid, arcs arcset.View, path string) (cid.Cid, []byte, error) {
	// Extract commitment bytes from MALT CID
	commBytes, err := codec.ExtractCommitment(comm)
	if err != nil {
		return cid.Cid{}, nil, fmt.Errorf("failed to extract commitment: %w", err)
	}

	s.mu.RLock()
	entry, ok := s.cache[string(commBytes)]
	s.mu.RUnlock()

	if !ok {
		return cid.Cid{}, nil, fmt.Errorf("commitment not found in cache")
	}

	proveIndex, ok := findPathIndex(entry.paths, path)
	if !ok {
		return cid.Cid{}, nil, fmt.Errorf("path %s not found", path)
	}

	// Generate evaluation point
	inputPoint := indexToKZGScalar(proveIndex)

	// Compute KZG proof
	proof, claimedValue, err := s.context.ComputeKZGProof(entry.blob, inputPoint, 1)
	if err != nil {
		return cid.Cid{}, nil, fmt.Errorf("failed to compute proof: %w", err)
	}

	// Serialize proof: proof (48) + claimedValue (32) + index (4)
	proofBytes := make([]byte, 0, ProofSize)
	proofBytes = append(proofBytes, proof[:]...)
	proofBytes = append(proofBytes, claimedValue[:]...)
	proofBytes = append(proofBytes, byte(proveIndex>>24), byte(proveIndex>>16), byte(proveIndex>>8), byte(proveIndex))

	return entry.values[proveIndex], proofBytes, nil
}

// Verify verifies a KZG proof.
func (s *Scheme) Verify(comm cid.Cid, path string, value cid.Cid, proof []byte) (bool, error) {
	if len(proof) < ProofSize {
		return false, fmt.Errorf("proof too short: %d", len(proof))
	}

	// Extract commitment bytes from MALT CID
	commBytes, err := codec.ExtractCommitment(comm)
	if err != nil {
		return false, fmt.Errorf("failed to extract commitment: %w", err)
	}

	s.mu.RLock()
	entry, ok := s.cache[string(commBytes)]
	s.mu.RUnlock()

	if !ok {
		return false, fmt.Errorf("commitment not found in cache")
	}

	// Deserialize proof
	var kzgProof gokzg4844.KZGProof
	var claimedValue gokzg4844.Scalar
	copy(kzgProof[:], proof[:48])
	copy(claimedValue[:], proof[48:80])

	// Reconstruct index
	index := int(proof[80])<<24 | int(proof[81])<<16 | int(proof[82])<<8 | int(proof[83])
	inputPoint := indexToKZGScalar(index)

	// Convert commitment
	var kzgComm gokzg4844.KZGCommitment
	copy(kzgComm[:], commBytes)

	// Verify KZG proof
	err = s.context.VerifyKZGProof(kzgComm, inputPoint, claimedValue, kzgProof)
	if err != nil {
		return false, nil
	}

	// Verify the path matches
	if index >= len(entry.paths) || entry.paths[index] != path {
		return false, nil
	}

	return true, nil
}

// Update updates a value in the commitment.
func (s *Scheme) Update(comm cid.Cid, arcs arcset.View, path string, oldValue, newValue cid.Cid) (cid.Cid, error) {
	// Extract commitment bytes from MALT CID
	commBytes, err := codec.ExtractCommitment(comm)
	if err != nil {
		return cid.Cid{}, fmt.Errorf("failed to extract commitment: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.cache[string(commBytes)]
	if !ok {
		return cid.Cid{}, fmt.Errorf("commitment not found in cache")
	}

	updateIndex, ok := findPathIndex(entry.paths, path)
	if !ok {
		return cid.Cid{}, fmt.Errorf("path %s not found", path)
	}

	// Update blob
	newScalar := cidToKZGScalar(newValue)
	offset := updateIndex * gokzg4844.SerializedScalarSize
	copy(entry.blob[offset:offset+gokzg4844.SerializedScalarSize], newScalar[:])

	// Update cached values
	entry.values[updateIndex] = newValue

	// Recompute commitment
	newComm, err := s.context.BlobToKZGCommitment(entry.blob, 1)
	if err != nil {
		return cid.Cid{}, fmt.Errorf("failed to recommit: %w", err)
	}

	newCommBytes := newComm[:]
	s.cache[string(newCommBytes)] = entry

	// Create MALT CID from new commitment bytes
	return codec.NewKZGCid(newCommBytes)
}

// BatchUpdate updates multiple values.
func (s *Scheme) BatchUpdate(comm cid.Cid, arcs arcset.View, updates map[string]struct {
	Old cid.Cid
	New cid.Cid
}) (cid.Cid, error) {
	// Extract commitment bytes from MALT CID
	commBytes, err := codec.ExtractCommitment(comm)
	if err != nil {
		return cid.Cid{}, fmt.Errorf("failed to extract commitment: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.cache[string(commBytes)]
	if !ok {
		return cid.Cid{}, fmt.Errorf("commitment not found in cache")
	}

	// Apply all updates
	for path, update := range updates {
		index, ok := findPathIndex(entry.paths, path)
		if !ok {
			return cid.Cid{}, fmt.Errorf("path %s not found", path)
		}

		newScalar := cidToKZGScalar(update.New)
		offset := index * gokzg4844.SerializedScalarSize
		copy(entry.blob[offset:offset+gokzg4844.SerializedScalarSize], newScalar[:])
		entry.values[index] = update.New
	}

	// Recompute commitment
	newComm, err := s.context.BlobToKZGCommitment(entry.blob, 1)
	if err != nil {
		return cid.Cid{}, fmt.Errorf("failed to recommit: %w", err)
	}

	newCommBytes := newComm[:]
	s.cache[string(newCommBytes)] = entry

	// Create MALT CID from new commitment bytes
	return codec.NewKZGCid(newCommBytes)
}

// ProveBatch generates proofs for multiple paths.
func (s *Scheme) ProveBatch(comm cid.Cid, arcs arcset.View, paths []string) (map[string]arcset.BatchProofEntry, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("paths is empty")
	}

	// Extract commitment bytes from MALT CID
	commBytes, err := codec.ExtractCommitment(comm)
	if err != nil {
		return nil, fmt.Errorf("failed to extract commitment: %w", err)
	}

	s.mu.RLock()
	entry, ok := s.cache[string(commBytes)]
	s.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("commitment not found in cache")
	}

	results := make(map[string]arcset.BatchProofEntry, len(paths))

	for _, path := range paths {
		index, ok := findPathIndex(entry.paths, path)
		if !ok {
			return nil, fmt.Errorf("path %s not found", path)
		}

		inputPoint := indexToKZGScalar(index)
		proof, claimedValue, err := s.context.ComputeKZGProof(entry.blob, inputPoint, 1)
		if err != nil {
			return nil, fmt.Errorf("failed to compute proof for path %s: %w", path, err)
		}

		proofBytes := make([]byte, 0, ProofSize)
		proofBytes = append(proofBytes, proof[:]...)
		proofBytes = append(proofBytes, claimedValue[:]...)
		proofBytes = append(proofBytes, byte(index>>24), byte(index>>16), byte(index>>8), byte(index))

		results[path] = arcset.BatchProofEntry{
			Target: entry.values[index],
			Proof:  proofBytes,
		}
	}

	return results, nil
}

// VerifyBatch verifies multiple proofs.
func (s *Scheme) VerifyBatch(comm cid.Cid, proofs map[string]arcset.BatchProofEntry) (bool, error) {
	// Extract commitment bytes from MALT CID
	commBytes, err := codec.ExtractCommitment(comm)
	if err != nil {
		return false, fmt.Errorf("failed to extract commitment: %w", err)
	}

	var kzgComm gokzg4844.KZGCommitment
	copy(kzgComm[:], commBytes)

	s.mu.RLock()
	entry, ok := s.cache[string(commBytes)]
	s.mu.RUnlock()

	if !ok {
		return false, fmt.Errorf("commitment not found in cache")
	}

	for path, entry_ := range proofs {
		if len(entry_.Proof) < ProofSize {
			return false, fmt.Errorf("proof too short for path %s", path)
		}

		index, ok := findPathIndex(entry.paths, path)
		if !ok {
			return false, nil
		}

		var kzgProof gokzg4844.KZGProof
		var claimedValue gokzg4844.Scalar
		copy(kzgProof[:], entry_.Proof[:48])
		copy(claimedValue[:], entry_.Proof[48:80])

		inputPoint := indexToKZGScalar(index)
		err := s.context.VerifyKZGProof(kzgComm, inputPoint, claimedValue, kzgProof)
		if err != nil {
			return false, nil
		}
	}

	return true, nil
}

// ProveAggregate generates an aggregated proof.
func (s *Scheme) ProveAggregate(comm cid.Cid, arcs arcset.View, paths []string) (*arcset.AggregatedProof, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("paths is empty")
	}

	// Extract commitment bytes from MALT CID
	commBytes, err := codec.ExtractCommitment(comm)
	if err != nil {
		return nil, fmt.Errorf("failed to extract commitment: %w", err)
	}

	s.mu.RLock()
	entry, ok := s.cache[string(commBytes)]
	s.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("commitment not found in cache")
	}

	targets := make([]cid.Cid, len(paths))
	aggregatedProofData := make([]byte, 0, len(paths)*80)

	for i, path := range paths {
		index, ok := findPathIndex(entry.paths, path)
		if !ok {
			return nil, fmt.Errorf("path %s not found", path)
		}

		targets[i] = entry.values[index]

		inputPoint := indexToKZGScalar(index)
		proof, claimedValue, err := s.context.ComputeKZGProof(entry.blob, inputPoint, 1)
		if err != nil {
			return nil, fmt.Errorf("failed to compute proof for path %s: %w", path, err)
		}

		aggregatedProofData = append(aggregatedProofData, proof[:]...)
		aggregatedProofData = append(aggregatedProofData, claimedValue[:]...)
	}

	return &arcset.AggregatedProof{
		Paths:     paths,
		Targets:   targets,
		ProofData: aggregatedProofData,
	}, nil
}

// VerifyAggregate verifies an aggregated proof.
func (s *Scheme) VerifyAggregate(comm cid.Cid, aggProof *arcset.AggregatedProof) (bool, error) {
	if aggProof == nil || len(aggProof.Paths) == 0 {
		return false, fmt.Errorf("invalid aggregated proof")
	}

	if len(aggProof.ProofData) != len(aggProof.Paths)*80 {
		return false, fmt.Errorf("proof data size mismatch")
	}

	// Extract commitment bytes from MALT CID
	commBytes, err := codec.ExtractCommitment(comm)
	if err != nil {
		return false, fmt.Errorf("failed to extract commitment: %w", err)
	}

	var kzgComm gokzg4844.KZGCommitment
	copy(kzgComm[:], commBytes)

	s.mu.RLock()
	entry, ok := s.cache[string(commBytes)]
	s.mu.RUnlock()

	if !ok {
		return false, fmt.Errorf("commitment not found in cache")
	}

	for i, path := range aggProof.Paths {
		index, ok := findPathIndex(entry.paths, path)
		if !ok {
			return false, nil
		}

		offset := i * 80
		var kzgProof gokzg4844.KZGProof
		var claimedValue gokzg4844.Scalar
		copy(kzgProof[:], aggProof.ProofData[offset:offset+48])
		copy(claimedValue[:], aggProof.ProofData[offset+48:offset+80])

		inputPoint := indexToKZGScalar(index)
		err := s.context.VerifyKZGProof(kzgComm, inputPoint, claimedValue, kzgProof)
		if err != nil {
			return false, nil
		}
	}

	return true, nil
}

// cidToKZGScalar converts a CID to a KZG scalar.
func cidToKZGScalar(c cid.Cid) gokzg4844.Scalar {
	var scalar gokzg4844.Scalar
	hash := sha256.Sum256(c.Bytes())

	value := new(big.Int).SetBytes(hash[:])
	value.Mod(value, bls12381ScalarMod)

	result := value.FillBytes(make([]byte, 32))
	copy(scalar[:], result)

	return scalar
}

// indexToKZGScalar converts an index to a KZG scalar.
func indexToKZGScalar(index int) gokzg4844.Scalar {
	var scalar gokzg4844.Scalar
	scalar[31] = byte(index)
	return scalar
}

// extractSortedPathsValues extracts sorted paths and values from an ArcSetView.
func extractSortedPathsValues(arcs arcset.View) ([]string, []cid.Cid) {
	var paths []string
	iter := arcs.Iterate()
	for {
		path, _, ok := iter.Next()
		if !ok {
			break
		}
		paths = append(paths, path)
	}
	// paths are already sorted by iterator

	values := make([]cid.Cid, len(paths))
	for i, path := range paths {
		values[i], _ = arcs.Get(path)
	}

	return paths, values
}

// findPathIndex finds the index of a path in the paths slice.
func findPathIndex(paths []string, path string) (int, bool) {
	// Binary search since paths are sorted
	low, high := 0, len(paths)-1
	for low <= high {
		mid := (low + high) / 2
		if paths[mid] == path {
			return mid, true
		}
		if paths[mid] < path {
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	return -1, false
}

// Ensure Scheme implements commitment.Scheme.
var _ commitment.Scheme = (*Scheme)(nil)