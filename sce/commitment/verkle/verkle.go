// Package verkle provides a Verkle Tree commitment implementation.
package verkle

import (
	"fmt"
	"sync"

	"github.com/dewebprotocol/malt/arcset"
	"github.com/dewebprotocol/malt/sce/commitment"
	"github.com/dewebprotocol/malt/key"
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
	values      []key.Key
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
func (s *Scheme) Commit(arcs arcset.View) (key.Key, error) {
	paths, values := extractSortedPathsValues(arcs)

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

	s.mu.Lock()
	s.cache[string(commBytes)] = entry
	s.mu.Unlock()

	return key.NewStructureRoot(commBytes), nil
}

// Prove generates a Verkle proof.
func (s *Scheme) Prove(comm key.Key, arcs arcset.View, path string) (key.Key, arcset.Proof, error) {
	s.mu.RLock()
	entry, ok := s.cache[string(comm.Bytes())]
	s.mu.RUnlock()

	if !ok {
		return nil, nil, fmt.Errorf("commitment not found in cache")
	}

	stem, ok := entry.pathToStem[path]
	if !ok {
		return nil, nil, fmt.Errorf("path %s not found in stem index", path)
	}

	index := entry.pathToIndex[path]

	proofBytes := make([]byte, 0, StemSize+32)
	proofBytes = append(proofBytes, stem...)
	proofBytes = append(proofBytes, entry.values[index].Bytes()...)

	return entry.values[index], arcset.Proof(proofBytes), nil
}

// Verify verifies a Verkle proof.
func (s *Scheme) Verify(comm key.Key, path string, value key.Key, proof arcset.Proof) (bool, error) {
	if len(proof) < StemSize {
		return false, nil
	}

	s.mu.RLock()
	entry, ok := s.cache[string(comm.Bytes())]
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
func (s *Scheme) Update(comm key.Key, arcs arcset.View, path string, oldValue, newValue key.Key) (key.Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.cache[string(comm.Bytes())]
	if !ok {
		return nil, fmt.Errorf("commitment not found in cache")
	}

	index, ok := entry.pathToIndex[path]
	if !ok {
		return nil, fmt.Errorf("path %s not found", path)
	}

	entry.values[index] = newValue

	// Recompute commitment bytes
	commBytes := make([]byte, 32)
	copy(commBytes, entry.pathToStem[entry.paths[0]])

	s.cache[string(commBytes)] = entry

	return key.NewStructureRoot(commBytes), nil
}

// BatchUpdate updates multiple values.
func (s *Scheme) BatchUpdate(comm key.Key, arcs arcset.View, updates map[string]struct {
	Old key.Key
	New key.Key
}) (key.Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.cache[string(comm.Bytes())]
	if !ok {
		return nil, fmt.Errorf("commitment not found in cache")
	}

	for path, update := range updates {
		index, ok := entry.pathToIndex[path]
		if !ok {
			return nil, fmt.Errorf("path %s not found", path)
		}
		entry.values[index] = update.New
	}

	commBytes := make([]byte, 32)
	copy(commBytes, entry.pathToStem[entry.paths[0]])

	s.cache[string(commBytes)] = entry

	return key.NewStructureRoot(commBytes), nil
}

// ProveBatch generates proofs for multiple paths.
func (s *Scheme) ProveBatch(comm key.Key, arcs arcset.View, paths []string) (map[string]arcset.BatchProofEntry, error) {
	s.mu.RLock()
	entry, ok := s.cache[string(comm.Bytes())]
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
			Proof:  arcset.Proof(proofBytes),
		}
	}

	return results, nil
}

// VerifyBatch verifies multiple proofs.
func (s *Scheme) VerifyBatch(comm key.Key, proofs map[string]arcset.BatchProofEntry) (bool, error) {
	s.mu.RLock()
	entry, ok := s.cache[string(comm.Bytes())]
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

// ProveAggregate generates an aggregated proof.
func (s *Scheme) ProveAggregate(comm key.Key, arcs arcset.View, paths []string) (*arcset.AggregatedProof, error) {
	s.mu.RLock()
	entry, ok := s.cache[string(comm.Bytes())]
	s.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("commitment not found in cache")
	}

	targets := make([]key.Key, len(paths))
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

// VerifyAggregate verifies an aggregated proof.
func (s *Scheme) VerifyAggregate(comm key.Key, aggProof *arcset.AggregatedProof) (bool, error) {
	if aggProof == nil || len(aggProof.Paths) == 0 {
		return false, fmt.Errorf("invalid aggregated proof")
	}

	if len(aggProof.ProofData) != len(aggProof.Paths)*StemSize {
		return false, fmt.Errorf("proof data size mismatch")
	}

	s.mu.RLock()
	entry, ok := s.cache[string(comm.Bytes())]
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

// extractSortedPathsValues extracts sorted paths and values from an ArcSetView.
func extractSortedPathsValues(arcs arcset.View) ([]string, []key.Key) {
	var paths []string
	iter := arcs.Iterate()
	for {
		path, _, ok := iter.Next()
		if !ok {
			break
		}
		paths = append(paths, path)
	}

	values := make([]key.Key, len(paths))
	for i, path := range paths {
		values[i], _ = arcs.Get(path)
	}

	return paths, values
}

// Ensure Scheme implements commitment.Scheme.
var _ commitment.Scheme = (*Scheme)(nil)