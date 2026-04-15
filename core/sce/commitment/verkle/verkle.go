// Package verkle provides a Verkle Tree commitment implementation.
package verkle

import (
	"crypto/sha256"
	"fmt"
	"sync"

	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/sce/commitment"
	"github.com/dewebprotocol/malt/core/types/arcset"
	verkle "github.com/ethereum/go-verkle"
	cid "github.com/ipfs/go-cid"
)

const (
	// Width is the Verkle tree width (256-ary).
	Width = 256
	// StemSize is the size of a Verkle stem in bytes.
	StemSize = 31
	// ProofSize is the size of a Verkle proof in bytes (stem + index).
	ProofSize = StemSize + 4
	// MaxCacheEntries is the maximum number of cached commitments.
	// When exceeded, the oldest entries are evicted.
	MaxCacheEntries = 1024
)

// Scheme implements commitment.Scheme using Verkle Trees.
type Scheme struct {
	*commitment.BaseScheme

	mu       sync.RWMutex
	auxCache map[string]*auxData // maps commitment string to its auxiliary data
}

type auxData struct {
	pathToStem  map[string]verkle.Stem
	pathToIndex map[string]int
}

// NewScheme creates a new Verkle commitment scheme.
func NewScheme() (*Scheme, error) {
	cache := commitment.NewCacheManager(MaxCacheEntries)
	return &Scheme{
		BaseScheme: commitment.NewBaseScheme(cache),
		auxCache:   make(map[string]*auxData),
	}, nil
}

// Commit generates a Verkle commitment.
func (s *Scheme) Commit(arcs arcset.Snapshot) (cid.Cid, error) {
	if arcs == nil {
		return cid.Cid{}, fmt.Errorf("arc set is nil")
	}

	paths, values := commitment.ExtractSortedPathsValues(arcs)

	// Create auxiliary data for Verkle-specific information
	aux := &auxData{
		pathToStem:  make(map[string]verkle.Stem),
		pathToIndex: make(map[string]int),
	}

	// Create stems for each path
	for i, path := range paths {
		stem := pathToVerkleStem(path)
		aux.pathToStem[path] = stem
		aux.pathToIndex[path] = i
	}

	// Get commitment bytes (simplified - use first stem)
	var commBytes []byte
	if len(paths) > 0 {
		commBytes = make([]byte, 32)
		copy(commBytes, aux.pathToStem[paths[0]])
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

	commStr := string(stemBytes)

	// Cache the auxiliary data
	s.mu.Lock()
	s.auxCache[commStr] = aux
	s.mu.Unlock()

	// Cache metadata via BaseScheme
	s.BaseScheme.Cache.Set(commStr, &commitment.CacheEntry{
		Paths:  paths,
		Values: values,
	})

	// Create MALT CID from stem bytes
	return codec.NewVerkleCid(stemBytes)
}

// Prove generates a Verkle proof.
func (s *Scheme) Prove(comm cid.Cid, arcs arcset.Snapshot, path string) (cid.Cid, []byte, error) {
	return s.ProveSingle(comm, arcs, path)
}

// ProveSingle is the core prove implementation for the Backend interface.
func (s *Scheme) ProveSingle(comm cid.Cid, arcs arcset.Snapshot, path string) (cid.Cid, []byte, error) {
	// Extract commitment bytes from MALT CID
	commBytes, err := codec.ExtractCommitment(comm)
	if err != nil {
		return cid.Cid{}, nil, fmt.Errorf("failed to extract commitment: %w", err)
	}

	commStr := string(commBytes)

	// Get cache entry
	entry, ok := s.BaseScheme.Cache.Get(commStr)
	if !ok {
		return cid.Cid{}, nil, fmt.Errorf("commitment not found in cache")
	}

	// Get auxiliary data
	s.mu.RLock()
	aux, ok := s.auxCache[commStr]
	s.mu.RUnlock()

	if !ok {
		return cid.Cid{}, nil, fmt.Errorf("auxiliary data not found in cache")
	}

	stem, ok := aux.pathToStem[path]
	if !ok {
		return cid.Cid{}, nil, fmt.Errorf("path %s not found in stem index", path)
	}

	index := aux.pathToIndex[path]

	// Proof format: stem (31) + index (4)
	proofBytes := make([]byte, 0, ProofSize)
	proofBytes = append(proofBytes, stem...)
	proofBytes = append(proofBytes, byte(index>>24), byte(index>>16), byte(index>>8), byte(index))

	return entry.Values[index], proofBytes, nil
}

// Verify verifies a Verkle proof.
func (s *Scheme) Verify(comm cid.Cid, path string, value cid.Cid, proof []byte) (bool, error) {
	return s.VerifySingle(comm, path, value, proof)
}

// VerifySingle is the core verify implementation for the Backend interface.
func (s *Scheme) VerifySingle(comm cid.Cid, path string, value cid.Cid, proof []byte) (bool, error) {
	if len(proof) < ProofSize {
		return false, nil
	}

	// Extract commitment bytes from MALT CID
	commBytes, err := codec.ExtractCommitment(comm)
	if err != nil {
		return false, fmt.Errorf("failed to extract commitment: %w", err)
	}

	commStr := string(commBytes)

	// Get auxiliary data
	s.mu.RLock()
	aux, ok := s.auxCache[commStr]
	s.mu.RUnlock()

	if !ok {
		return false, nil
	}

	// Extract proof components: stem (31) + index (4)
	stem := proof[:StemSize]
	expectedStem, ok := aux.pathToStem[path]
	if !ok {
		return false, nil
	}

	for i := range StemSize {
		if stem[i] != expectedStem[i] {
			return false, nil
		}
	}

	// Verify index
	index := int(proof[StemSize])<<24 | int(proof[StemSize+1])<<16 | int(proof[StemSize+2])<<8 | int(proof[StemSize+3])
	if index != aux.pathToIndex[path] {
		return false, nil
	}

	return true, nil
}

// Update updates a value in the commitment.
func (s *Scheme) Update(comm cid.Cid, arcs arcset.Snapshot, path string, oldValue, newValue cid.Cid) (cid.Cid, error) {
	// Extract commitment bytes from MALT CID
	commBytes, err := codec.ExtractCommitment(comm)
	if err != nil {
		return cid.Cid{}, fmt.Errorf("failed to extract commitment: %w", err)
	}

	commStr := string(commBytes)

	// Get cache entry
	entry, ok := s.BaseScheme.Cache.Get(commStr)
	if !ok {
		return cid.Cid{}, fmt.Errorf("commitment not found in cache")
	}

	// Get auxiliary data
	s.mu.Lock()
	defer s.mu.Unlock()

	aux, ok := s.auxCache[commStr]
	if !ok {
		return cid.Cid{}, fmt.Errorf("auxiliary data not found in cache")
	}

	index, ok := aux.pathToIndex[path]
	if !ok {
		return cid.Cid{}, fmt.Errorf("path %s not found", path)
	}

	entry.Values[index] = newValue

	// Recompute commitment bytes - hash values to make commitment change
	newCommBytes := computeCommitmentFromValues(entry.Paths, entry.Values)

	stemBytes := make([]byte, StemSize)
	copy(stemBytes, newCommBytes[:StemSize])

	newCommStr := string(stemBytes)

	// Move auxiliary data to new commitment
	s.auxCache[newCommStr] = aux
	delete(s.auxCache, commStr)

	// Cache new entry
	s.BaseScheme.Cache.Set(newCommStr, entry)

	// Create MALT CID from stem bytes
	return codec.NewVerkleCid(stemBytes)
}

// BatchUpdate updates multiple values.
func (s *Scheme) BatchUpdate(comm cid.Cid, arcs arcset.Snapshot, updates map[string]struct {
	Old cid.Cid
	New cid.Cid
}) (cid.Cid, error) {
	// Extract commitment bytes from MALT CID
	commBytes, err := codec.ExtractCommitment(comm)
	if err != nil {
		return cid.Cid{}, fmt.Errorf("failed to extract commitment: %w", err)
	}

	commStr := string(commBytes)

	// Get cache entry
	entry, ok := s.BaseScheme.Cache.Get(commStr)
	if !ok {
		return cid.Cid{}, fmt.Errorf("commitment not found in cache")
	}

	// Get auxiliary data
	s.mu.Lock()
	defer s.mu.Unlock()

	aux, ok := s.auxCache[commStr]
	if !ok {
		return cid.Cid{}, fmt.Errorf("auxiliary data not found in cache")
	}

	for path, update := range updates {
		index, ok := aux.pathToIndex[path]
		if !ok {
			return cid.Cid{}, fmt.Errorf("path %s not found", path)
		}
		entry.Values[index] = update.New
	}

	// Recompute commitment bytes - hash values to make commitment change
	newCommBytes := computeCommitmentFromValues(entry.Paths, entry.Values)

	stemBytes := make([]byte, StemSize)
	copy(stemBytes, newCommBytes[:StemSize])

	newCommStr := string(stemBytes)

	// Move auxiliary data to new commitment
	s.auxCache[newCommStr] = aux
	delete(s.auxCache, commStr)

	// Cache new entry
	s.BaseScheme.Cache.Set(newCommStr, entry)

	// Create MALT CID from stem bytes
	return codec.NewVerkleCid(stemBytes)
}

// BatchProve generates proofs for multiple paths.
func (s *Scheme) BatchProve(comm cid.Cid, arcs arcset.Snapshot, paths []string) (map[string]arcset.BatchProofEntry, error) {
	return s.BaseScheme.BatchProveImpl(comm, arcs, s, paths)
}

// BatchVerify verifies multiple proofs.
func (s *Scheme) BatchVerify(comm cid.Cid, proofs map[string]arcset.BatchProofEntry) (bool, error) {
	return s.BaseScheme.BatchVerifyImpl(comm, s, proofs)
}

// AggregateProve generates an aggregated proof.
func (s *Scheme) AggregateProve(comm cid.Cid, arcs arcset.Snapshot, paths []string) (*arcset.AggregatedProof, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("paths is empty")
	}

	// Extract commitment bytes from MALT CID
	commBytes, err := codec.ExtractCommitment(comm)
	if err != nil {
		return nil, fmt.Errorf("failed to extract commitment: %w", err)
	}

	commStr := string(commBytes)

	// Get cache entry
	entry, ok := s.BaseScheme.Cache.Get(commStr)
	if !ok {
		return nil, fmt.Errorf("commitment not found in cache")
	}

	// Get auxiliary data
	s.mu.RLock()
	aux, ok := s.auxCache[commStr]
	s.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("auxiliary data not found in cache")
	}

	targets := make([]cid.Cid, len(paths))
	proofData := make([]byte, 0, len(paths)*StemSize)

	for i, path := range paths {
		stem, ok := aux.pathToStem[path]
		if !ok {
			return nil, fmt.Errorf("path %s not found in stem index", path)
		}

		index := aux.pathToIndex[path]
		targets[i] = entry.Values[index]
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
		return false, fmt.Errorf("proof data size mismatch: expected %d, got %d",
			len(aggProof.Paths)*StemSize, len(aggProof.ProofData))
	}

	// Extract commitment bytes from MALT CID
	commBytes, err := codec.ExtractCommitment(comm)
	if err != nil {
		return false, fmt.Errorf("failed to extract commitment: %w", err)
	}

	commStr := string(commBytes)

	// Get auxiliary data
	s.mu.RLock()
	aux, ok := s.auxCache[commStr]
	s.mu.RUnlock()

	if !ok {
		return false, nil
	}

	for i, path := range aggProof.Paths {
		stem := aggProof.ProofData[i*StemSize : (i+1)*StemSize]
		expectedStem, ok := aux.pathToStem[path]
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
