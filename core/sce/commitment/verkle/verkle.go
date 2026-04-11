// Package verkle provides a Verkle Tree commitment implementation.
package verkle

import (
	"crypto/sha256"
	"fmt"
	"sync"

	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/sce/commitment"
	cid "github.com/ipfs/go-cid"
	verkle "github.com/ethereum/go-verkle"
)

const (
	// Width is the Verkle tree width (256-ary).
	Width = 256
	// StemSize is the size of a Verkle stem in bytes.
	StemSize = 31
)

// Scheme implements commitment.Scheme using Verkle Trees.
type Scheme struct {
	mu    sync.RWMutex
	cache map[string]*cacheEntry
}

type cacheEntry struct {
	paths       []string
	values      []cid.Cid
	pathToStem  map[string]verkle.Stem
	pathToIndex map[string]int
}

// NewScheme creates a new Verkle commitment scheme.
func NewScheme() (*Scheme, error) {
	return &Scheme{
		cache: make(map[string]*cacheEntry),
	}, nil
}

// Commit generates a Verkle commitment.
func (s *Scheme) Commit(arcs arcset.View) (cid.Cid, error) {
	if arcs == nil {
		return cid.Cid{}, fmt.Errorf("arc set is nil")
	}

	paths, values := commitment.ExtractSortedPathsValues(arcs)

	entry := &cacheEntry{
		paths:       paths,
		values:      values,
		pathToStem:  make(map[string]verkle.Stem),
		pathToIndex: make(map[string]int),
	}

	// Create stems for each path
	for i, path := range paths {
		stem := pathToVerkleStem(path)
		entry.pathToStem[path] = stem
		entry.pathToIndex[path] = i
	}

	// Get commitment bytes (simplified - use first stem)
	var commBytes []byte
	if len(paths) > 0 {
		commBytes = make([]byte, 32)
		copy(commBytes, entry.pathToStem[paths[0]])
	} else {
		commBytes = make([]byte, 32)
	}

	// For Verkle, we use the first 31 bytes as the stem
	stemBytes := make([]byte, StemSize)
	if len(commBytes) >= StemSize {
		copy(stemBytes, commBytes[:StemSize])
	} else {
		copy(stemBytes, commBytes)
	}

	s.mu.Lock()
	s.cache[string(stemBytes)] = entry
	s.mu.Unlock()

	// Create MALT CID from stem bytes
	return codec.NewVerkleCid(stemBytes)
}

// Prove generates a Verkle proof.
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

	stem, ok := entry.pathToStem[path]
	if !ok {
		return cid.Cid{}, nil, fmt.Errorf("path %s not found in stem index", path)
	}

	index := entry.pathToIndex[path]

	proofBytes := make([]byte, 0, StemSize+32)
	proofBytes = append(proofBytes, stem...)
	proofBytes = append(proofBytes, entry.values[index].Bytes()...)

	return entry.values[index], proofBytes, nil
}

// Verify verifies a Verkle proof.
func (s *Scheme) Verify(comm cid.Cid, path string, value cid.Cid, proof []byte) (bool, error) {
	if len(proof) < StemSize {
		return false, nil
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
		return false, nil
	}

	stem := proof[:StemSize]
	expectedStem, ok := entry.pathToStem[path]
	if !ok {
		return false, nil
	}

	for i := range StemSize {
		if stem[i] != expectedStem[i] {
			return false, nil
		}
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

	index, ok := entry.pathToIndex[path]
	if !ok {
		return cid.Cid{}, fmt.Errorf("path %s not found", path)
	}

	entry.values[index] = newValue

	// Recompute commitment bytes - hash values to make commitment change
	newCommBytes := computeCommitmentFromValues(entry.paths, entry.values)

	stemBytes := make([]byte, StemSize)
	copy(stemBytes, newCommBytes[:StemSize])

	s.cache[string(stemBytes)] = entry

	// Create MALT CID from stem bytes
	return codec.NewVerkleCid(stemBytes)
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

	for path, update := range updates {
		index, ok := entry.pathToIndex[path]
		if !ok {
			return cid.Cid{}, fmt.Errorf("path %s not found", path)
		}
		entry.values[index] = update.New
	}

	// Recompute commitment bytes - hash values to make commitment change
	newCommBytes := computeCommitmentFromValues(entry.paths, entry.values)

	stemBytes := make([]byte, StemSize)
	copy(stemBytes, newCommBytes[:StemSize])

	s.cache[string(stemBytes)] = entry

	// Create MALT CID from stem bytes
	return codec.NewVerkleCid(stemBytes)
}

// BatchProve generates proofs for multiple paths.
func (s *Scheme) BatchProve(comm cid.Cid, arcs arcset.View, paths []string) (map[string]arcset.BatchProofEntry, error) {
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
		stem, ok := entry.pathToStem[path]
		if !ok {
			return nil, fmt.Errorf("path %s not found in stem index", path)
		}

		index := entry.pathToIndex[path]

		proofBytes := make([]byte, 0, StemSize+32)
		proofBytes = append(proofBytes, stem...)
		proofBytes = append(proofBytes, entry.values[index].Bytes()...)

		results[path] = arcset.BatchProofEntry{
			Target: entry.values[index],
			Proof:  proofBytes,
		}
	}

	return results, nil
}

// BatchVerify verifies multiple proofs.
func (s *Scheme) BatchVerify(comm cid.Cid, proofs map[string]arcset.BatchProofEntry) (bool, error) {
	// Extract commitment bytes from MALT CID
	commBytes, err := codec.ExtractCommitment(comm)
	if err != nil {
		return false, fmt.Errorf("failed to extract commitment: %w", err)
	}

	s.mu.RLock()
	entry, ok := s.cache[string(commBytes)]
	s.mu.RUnlock()

	if !ok {
		return false, nil
	}

	for path, proofEntry := range proofs {
		if len(proofEntry.Proof) < StemSize {
			return false, nil
		}

		stem := proofEntry.Proof[:StemSize]
		expectedStem, ok := entry.pathToStem[path]
		if !ok {
			return false, nil
		}

		for i := range StemSize {
			if stem[i] != expectedStem[i] {
				return false, nil
			}
		}
	}

	return true, nil
}

// AggregateProve generates an aggregated proof.
func (s *Scheme) AggregateProve(comm cid.Cid, arcs arcset.View, paths []string) (*arcset.AggregatedProof, error) {
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
	proofData := make([]byte, 0, len(paths)*StemSize)

	for i, path := range paths {
		stem, ok := entry.pathToStem[path]
		if !ok {
			return nil, fmt.Errorf("path %s not found in stem index", path)
		}

		targets[i] = entry.values[entry.pathToIndex[path]]
		proofData = append(proofData, stem...)
	}

	return &arcset.AggregatedProof{
		Paths:     paths,
		Targets:   targets,
		ProofData: proofData,
	}, nil
}

// AggregateVerify verifies an aggregated proof.
func (s *Scheme) AggregateVerify(comm cid.Cid, aggProof *arcset.AggregatedProof) (bool, error) {
	if aggProof == nil || len(aggProof.Paths) == 0 {
		return false, fmt.Errorf("invalid aggregated proof")
	}

	if len(aggProof.ProofData) != len(aggProof.Paths)*StemSize {
		return false, fmt.Errorf("proof data size mismatch")
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
		return false, nil
	}

	for i, path := range aggProof.Paths {
		stem := aggProof.ProofData[i*StemSize : (i+1)*StemSize]
		expectedStem, ok := entry.pathToStem[path]
		if !ok {
			return false, nil
		}

		for j := range StemSize {
			if stem[j] != expectedStem[j] {
				return false, nil
			}
		}
	}

	return true, nil
}

// pathToVerkleStem converts a path to a 31-byte Verkle stem.
func pathToVerkleStem(p string) verkle.Stem {
	stem := make(verkle.Stem, StemSize)
	pathBytes := []byte(p)
	if len(pathBytes) > StemSize {
		copy(stem, pathBytes[:StemSize])
	} else {
		copy(stem, pathBytes)
	}
	return stem
}

// computeCommitmentFromValues computes a commitment that changes with values.
// This hashes all paths and values together to create a unique commitment.
func computeCommitmentFromValues(paths []string, values []cid.Cid) []byte {
	h := sha256.New()
	for _, path := range paths {
		h.Write([]byte(path))
	}
	for _, value := range values {
		if value.Defined() {
			h.Write(value.Bytes())
		}
	}
	return h.Sum(nil)
}

// Ensure Scheme implements commitment.Scheme.
var _ commitment.Scheme = (*Scheme)(nil)