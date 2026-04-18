// Package ipa provides an IPA (Inner Product Argument) commitment implementation.
package ipa

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sync"

	"github.com/crate-crypto/go-ipa/bandersnatch/fr"
	"github.com/crate-crypto/go-ipa/banderwagon"
	"github.com/crate-crypto/go-ipa/common"
	ipa "github.com/crate-crypto/go-ipa/ipa"
	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/sce/commitment"
	cid "github.com/ipfs/go-cid"
)

const (
	// MaxValues is the maximum number of values per commitment.
	MaxValues = 256
	// ProofSize is the size of a single IPA proof in bytes.
	// For 256 elements: numRounds=8, size=4 + 8*32(L) + 8*32(R) + 32(A_scalar) + 4(index) = 552
	ProofSize = 552
	// MaxCacheEntries is the maximum number of cached commitments.
	// When exceeded, the oldest entries are evicted.
	MaxCacheEntries = 1024
)

// Scheme implements commitment.Scheme using Inner Product Arguments.
type Scheme struct {
	*commitment.BaseScheme
	ipaConfig *ipa.IPAConfig

	mu       sync.RWMutex
	auxCache map[string]*auxData // maps commitment string to its auxiliary data
}

type auxData struct {
	vector []fr.Element
}

// NewScheme creates a new IPA commitment scheme.
func NewScheme() (*Scheme, error) {
	ipaConfig, err := ipa.NewIPASettings()
	if err != nil {
		return nil, fmt.Errorf("failed to create IPA settings: %w", err)
	}

	cache := commitment.NewCacheManager(MaxCacheEntries)
	return &Scheme{
		BaseScheme: commitment.NewBaseScheme(cache),
		ipaConfig:  ipaConfig,
		auxCache:   make(map[string]*auxData),
	}, nil
}

// MaxValues returns the maximum number of authenticated slots.
func (s *Scheme) MaxValues() int {
	return MaxValues
}

// CommitValues commits a stable indexed value vector.
func (s *Scheme) CommitValues(values []cid.Cid) (cid.Cid, error) {
	return s.commitValues(nil, values)
}

// Commit generates an IPA commitment.
func (s *Scheme) Commit(arcs arcset.ArcSet) (cid.Cid, error) {
	if arcs == nil {
		return cid.Cid{}, fmt.Errorf("arc set is nil")
	}

	paths, values := commitment.ExtractSortedPathsValues(arcs)
	return s.commitValues(paths, values)
}

// Prove generates an IPA proof.
func (s *Scheme) Prove(comm cid.Cid, arcs arcset.ArcSet, path string) (cid.Cid, []byte, error) {
	return s.ProveSingle(comm, arcs, path)
}

// ProveSingle is the core prove implementation for the Backend interface.
func (s *Scheme) ProveSingle(comm cid.Cid, arcs arcset.ArcSet, path string) (cid.Cid, []byte, error) {
	var (
		paths  []string
		values []cid.Cid
	)
	if arcs != nil {
		paths, values = commitment.ExtractSortedPathsValues(arcs)
	}
	entry, _, err := s.ensureState(comm, paths, values)
	if err != nil {
		return cid.Cid{}, nil, err
	}

	proveIndex, ok := commitment.FindPathIndex(entry.Paths, path)
	if !ok {
		return cid.Cid{}, nil, fmt.Errorf("path %s not found", path)
	}
	return s.ProveIndex(comm, values, uint64(proveIndex))
}

// Verify verifies an IPA proof.
func (s *Scheme) Verify(comm cid.Cid, path string, value cid.Cid, proof []byte) (bool, error) {
	return s.VerifySingle(comm, path, value, proof)
}

// VerifySingle is the core verify implementation for the Backend interface.
func (s *Scheme) VerifySingle(comm cid.Cid, path string, value cid.Cid, proof []byte) (bool, error) {
	// Extract commitment bytes from MALT CID
	commBytes, err := codec.ExtractCommitment(comm)
	if err != nil {
		return false, fmt.Errorf("failed to extract commitment: %w", err)
	}

	commStr := string(commBytes)

	// Get cache entry
	entry, ok := s.BaseScheme.Cache.Get(commStr)
	if !ok {
		return false, fmt.Errorf("commitment not found in cache")
	}

	index, ok := commitment.FindPathIndex(entry.Paths, path)
	if !ok {
		return false, nil
	}
	return s.VerifyIndex(comm, uint64(index), value, proof)
}

// Update updates a value in the commitment.
func (s *Scheme) Update(comm cid.Cid, arcs arcset.ArcSet, path string, oldValue, newValue cid.Cid) (cid.Cid, error) {
	var (
		paths  []string
		values []cid.Cid
	)
	if arcs != nil {
		paths, values = commitment.ExtractSortedPathsValues(arcs)
	}
	entry, _, err := s.ensureState(comm, paths, values)
	if err != nil {
		return cid.Cid{}, err
	}

	updateIndex, ok := commitment.FindPathIndex(entry.Paths, path)
	if !ok {
		return cid.Cid{}, fmt.Errorf("path %s not found", path)
	}
	return s.ReplaceIndex(comm, values, uint64(updateIndex), oldValue, newValue)
}

// BatchUpdate updates multiple values.
func (s *Scheme) BatchUpdate(comm cid.Cid, arcs arcset.ArcSet, updates map[string]struct {
	Old cid.Cid
	New cid.Cid
}) (cid.Cid, error) {
	paths, values := commitment.ExtractSortedPathsValues(arcs)
	entry, aux, err := s.ensureState(comm, paths, values)
	if err != nil {
		return cid.Cid{}, err
	}

	commBytes, err := codec.ExtractCommitment(comm)
	if err != nil {
		return cid.Cid{}, fmt.Errorf("failed to extract commitment: %w", err)
	}
	commStr := string(commBytes)

	s.mu.Lock()
	defer s.mu.Unlock()

	var currentComm banderwagon.Element
	if err := currentComm.SetBytes(commBytes); err != nil {
		return cid.Cid{}, fmt.Errorf("failed to reconstruct commitment: %w", err)
	}

	for path, update := range updates {
		index, ok := commitment.FindPathIndex(entry.Paths, path)
		if !ok {
			return cid.Cid{}, fmt.Errorf("path %s not found", path)
		}
		if !entry.Values[index].Equals(update.Old) {
			return cid.Cid{}, fmt.Errorf("old value mismatch for path %s", path)
		}

		newElement := cidToFieldElement(update.New)
		var diff fr.Element
		diff.Sub(&newElement, &aux.vector[index])

		var updateElem banderwagon.Element
		updateElem.ScalarMul(&s.ipaConfig.SRS[index], &diff)

		var newComm banderwagon.Element
		newComm.Add(&currentComm, &updateElem)
		currentComm = newComm

		aux.vector[index] = newElement
		entry.Values[index] = update.New
	}

	result := currentComm.Bytes()
	commBytes = result[:]
	newCommStr := string(commBytes)

	// Move auxiliary data to new commitment
	s.auxCache[newCommStr] = aux
	delete(s.auxCache, commStr)

	// Cache new entry
	s.BaseScheme.Cache.Set(newCommStr, entry)

	// Create MALT CID from new commitment bytes
	return codec.NewIPACid(commBytes)
}

// ProveIndex proves the value at a stable index.
func (s *Scheme) ProveIndex(comm cid.Cid, values []cid.Cid, index uint64) (cid.Cid, []byte, error) {
	entry, aux, err := s.ensureState(comm, nil, values)
	if err != nil {
		return cid.Cid{}, nil, err
	}
	if index >= uint64(len(entry.Values)) {
		return cid.Cid{}, nil, fmt.Errorf("index %d out of range", index)
	}

	commBytes, err := codec.ExtractCommitment(comm)
	if err != nil {
		return cid.Cid{}, nil, fmt.Errorf("failed to extract commitment: %w", err)
	}

	transcript := common.NewTranscript("malt-ipa")

	var c banderwagon.Element
	if err := c.SetBytes(commBytes); err != nil {
		return cid.Cid{}, nil, fmt.Errorf("failed to reconstruct commitment: %w", err)
	}

	var evalPoint fr.Element
	evalPoint.SetUint64(index)

	proof, err := ipa.CreateIPAProof(transcript, s.ipaConfig, c, aux.vector, evalPoint)
	if err != nil {
		return cid.Cid{}, nil, fmt.Errorf("failed to create IPA proof: %w", err)
	}

	proofBytes, err := s.serializeProof(&proof, int(index))
	if err != nil {
		return cid.Cid{}, nil, fmt.Errorf("failed to serialize proof: %w", err)
	}

	return entry.Values[index], proofBytes, nil
}

// VerifyIndex verifies a proof for a stable index without requiring cache state.
func (s *Scheme) VerifyIndex(comm cid.Cid, index uint64, value cid.Cid, proof []byte) (bool, error) {
	commBytes, err := codec.ExtractCommitment(comm)
	if err != nil {
		return false, fmt.Errorf("failed to extract commitment: %w", err)
	}

	ipaProof, evalPoint, err := s.deserializeProof(proof)
	if err != nil {
		return false, fmt.Errorf("failed to deserialize proof: %w", err)
	}
	if evalPoint != index {
		return false, nil
	}

	transcript := common.NewTranscript("malt-ipa")

	var c banderwagon.Element
	if err := c.SetBytes(commBytes); err != nil {
		return false, fmt.Errorf("failed to reconstruct commitment: %w", err)
	}

	var evalPointFr fr.Element
	evalPointFr.SetUint64(index)

	output := cidToFieldElement(value)
	ok, err := ipa.CheckIPAProof(transcript, s.ipaConfig, c, *ipaProof, evalPointFr, output)
	if err != nil {
		return false, fmt.Errorf("failed to check IPA proof: %w", err)
	}
	return ok, nil
}

// ReplaceIndex performs an index-stable replacement.
func (s *Scheme) ReplaceIndex(comm cid.Cid, values []cid.Cid, index uint64, oldValue, newValue cid.Cid) (cid.Cid, error) {
	entry, aux, err := s.ensureState(comm, nil, values)
	if err != nil {
		return cid.Cid{}, err
	}
	if index >= uint64(len(entry.Values)) {
		return cid.Cid{}, fmt.Errorf("index %d out of range", index)
	}
	if !entry.Values[index].Equals(oldValue) {
		return cid.Cid{}, fmt.Errorf("old value mismatch at index %d", index)
	}

	commBytes, err := codec.ExtractCommitment(comm)
	if err != nil {
		return cid.Cid{}, fmt.Errorf("failed to extract commitment: %w", err)
	}
	commStr := string(commBytes)

	s.mu.Lock()
	defer s.mu.Unlock()

	newElement := cidToFieldElement(newValue)
	var diff fr.Element
	diff.Sub(&newElement, &aux.vector[index])

	var current banderwagon.Element
	if err := current.SetBytes(commBytes); err != nil {
		return cid.Cid{}, fmt.Errorf("failed to reconstruct commitment: %w", err)
	}

	var updateElem banderwagon.Element
	updateElem.ScalarMul(&s.ipaConfig.SRS[index], &diff)

	var newComm banderwagon.Element
	newComm.Add(&current, &updateElem)
	result := newComm.Bytes()

	aux.vector[index] = newElement
	entry.Values[index] = newValue

	newCommStr := string(result[:])
	s.auxCache[newCommStr] = aux
	delete(s.auxCache, commStr)
	s.BaseScheme.Cache.Set(newCommStr, entry)

	return codec.NewIPACid(result[:])
}

// BatchProve generates proofs for multiple paths.
func (s *Scheme) BatchProve(comm cid.Cid, arcs arcset.ArcSet, paths []string) (map[string]arcset.BatchProofEntry, error) {
	return s.BaseScheme.BatchProveImpl(comm, arcs, s, paths)
}

// BatchVerify verifies multiple proofs.
func (s *Scheme) BatchVerify(comm cid.Cid, proofs map[string]arcset.BatchProofEntry) (bool, error) {
	return s.BaseScheme.BatchVerifyImpl(comm, s, proofs)
}

// AggregateProve generates an aggregated proof.
func (s *Scheme) AggregateProve(comm cid.Cid, arcs arcset.ArcSet, paths []string) (*arcset.AggregatedProof, error) {
	return s.BaseScheme.AggregateProveImpl(comm, arcs, s, paths, ProofSize)
}

// AggregateVerify verifies an aggregated proof.
func (s *Scheme) AggregateVerify(comm cid.Cid, aggProof *arcset.AggregatedProof) (bool, error) {
	return s.BaseScheme.AggregateVerifyImpl(comm, s, aggProof, ProofSize)
}

// serializeProof serializes an IPA proof with index information.
func (s *Scheme) serializeProof(proof *ipa.IPAProof, index int) ([]byte, error) {
	numRounds := len(proof.L)
	totalSize := 4 + (numRounds*2+1)*32 + 4 // +4 for index

	result := make([]byte, totalSize)
	binary.BigEndian.PutUint32(result[0:4], uint32(numRounds))

	offset := 4
	for _, p := range proof.L {
		pb := p.Bytes()
		copy(result[offset:offset+32], pb[:])
		offset += 32
	}
	for _, p := range proof.R {
		pb := p.Bytes()
		copy(result[offset:offset+32], pb[:])
		offset += 32
	}
	as := proof.A_scalar.BytesLE()
	copy(result[offset:offset+32], as[:])
	offset += 32

	// Append index as 4 bytes
	binary.BigEndian.PutUint32(result[offset:offset+4], uint32(index))

	return result, nil
}

// deserializeProof deserializes an IPA proof and returns the proof and index.
func (s *Scheme) deserializeProof(data []byte) (*ipa.IPAProof, uint64, error) {
	// Minimum size: 4 (numRounds) + 32 (A_scalar) + 4 (index)
	if len(data) < 40 {
		return nil, 0, fmt.Errorf("proof data too short")
	}

	numRounds := int(binary.BigEndian.Uint32(data[0:4]))
	expectedSize := 4 + (numRounds*2+1)*32 + 4 // +4 for index
	if len(data) != expectedSize {
		return nil, 0, fmt.Errorf("proof data has wrong size: expected %d, got %d", expectedSize, len(data))
	}

	proof := &ipa.IPAProof{
		L: make([]banderwagon.Element, numRounds),
		R: make([]banderwagon.Element, numRounds),
	}

	offset := 4
	for i := 0; i < numRounds; i++ {
		if err := proof.L[i].SetBytes(data[offset : offset+32]); err != nil {
			return nil, 0, fmt.Errorf("failed to parse L[%d]: %w", i, err)
		}
		offset += 32
	}
	for i := 0; i < numRounds; i++ {
		if err := proof.R[i].SetBytes(data[offset : offset+32]); err != nil {
			return nil, 0, fmt.Errorf("failed to parse R[%d]: %w", i, err)
		}
		offset += 32
	}

	proof.A_scalar.SetBytesLE(data[offset : offset+32])
	offset += 32

	// Extract index
	index := uint64(binary.BigEndian.Uint32(data[offset : offset+4]))

	return proof, index, nil
}

func cidToFieldElement(c cid.Cid) fr.Element {
	var result fr.Element
	bytes := c.Bytes()
	h := sha256.Sum256(bytes)
	result.SetBytes(h[:])
	return result
}

func (s *Scheme) commitValues(paths []string, values []cid.Cid) (cid.Cid, error) {
	if len(values) > MaxValues {
		return cid.Cid{}, fmt.Errorf("too many values: %d > %d", len(values), MaxValues)
	}

	vector := make([]fr.Element, MaxValues)
	zero := fr.Element{}
	zero.SetZero()
	for i := range vector {
		vector[i] = zero
	}
	for i, value := range values {
		vector[i] = cidToFieldElement(value)
	}

	comm := s.ipaConfig.Commit(vector)
	commBytes := comm.Bytes()
	commStr := string(commBytes[:])

	clonedValues := append([]cid.Cid(nil), values...)
	clonedPaths := append([]string(nil), paths...)

	s.mu.Lock()
	s.auxCache[commStr] = &auxData{vector: vector}
	s.mu.Unlock()

	s.BaseScheme.Cache.Set(commStr, &commitment.CacheEntry{
		Paths:  clonedPaths,
		Values: clonedValues,
	})

	return codec.NewIPACid(commBytes[:])
}

func (s *Scheme) ensureState(comm cid.Cid, paths []string, values []cid.Cid) (*commitment.CacheEntry, *auxData, error) {
	commBytes, err := codec.ExtractCommitment(comm)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to extract commitment: %w", err)
	}
	commStr := string(commBytes)

	entry, ok := s.BaseScheme.Cache.Get(commStr)
	if !ok {
		if values == nil {
			return nil, nil, fmt.Errorf("commitment not found in cache")
		}
		rebuilt, err := s.commitValues(paths, values)
		if err != nil {
			return nil, nil, err
		}
		if !rebuilt.Equals(comm) {
			return nil, nil, fmt.Errorf("reconstructed commitment does not match expected root")
		}
		entry, ok = s.BaseScheme.Cache.Get(commStr)
		if !ok {
			return nil, nil, fmt.Errorf("commitment not found in cache")
		}
	}

	s.mu.RLock()
	aux, ok := s.auxCache[commStr]
	s.mu.RUnlock()
	if !ok {
		return nil, nil, fmt.Errorf("auxiliary data not found in cache")
	}
	return entry, aux, nil
}

// Ensure Scheme implements commitment.Scheme.
var _ commitment.Scheme = (*Scheme)(nil)
var _ commitment.ListBackend = (*Scheme)(nil)
