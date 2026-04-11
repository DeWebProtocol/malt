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
)

// Scheme implements commitment.Scheme using Inner Product Arguments.
type Scheme struct {
	ipaConfig *ipa.IPAConfig

	mu       sync.RWMutex
	auxStore map[string]*auxData
}

type auxData struct {
	vector      []fr.Element
	paths       []string
	values      []cid.Cid
	pathToIndex map[string]int
}

// NewScheme creates a new IPA commitment scheme.
func NewScheme() (*Scheme, error) {
	ipaConfig, err := ipa.NewIPASettings()
	if err != nil {
		return nil, fmt.Errorf("failed to create IPA settings: %w", err)
	}

	return &Scheme{
		ipaConfig: ipaConfig,
		auxStore:  make(map[string]*auxData),
	}, nil
}

// Commit generates an IPA commitment.
func (s *Scheme) Commit(arcs arcset.View) (cid.Cid, error) {
	if arcs == nil {
		return cid.Cid{}, fmt.Errorf("arc set is nil")
	}

	paths, values := commitment.ExtractSortedPathsValues(arcs)

	if len(paths) > MaxValues {
		return cid.Cid{}, fmt.Errorf("too many values: %d > %d", len(paths), MaxValues)
	}

	// Create vector (padded to 256 elements)
	vector := make([]fr.Element, MaxValues)
	zero := fr.Element{}
	zero.SetZero()
	for j := range vector {
		vector[j] = zero
	}

	pathToIndex := make(map[string]int, len(paths))
	for idx, path := range paths {
		vector[idx] = cidToFieldElement(values[idx])
		pathToIndex[path] = idx
	}

	// Compute IPA commitment
	comm := s.ipaConfig.Commit(vector)

	commBytes := comm.Bytes()
	s.mu.Lock()
	s.auxStore[string(commBytes[:])] = &auxData{
		vector:      vector,
		paths:       paths,
		values:      values,
		pathToIndex: pathToIndex,
	}
	s.mu.Unlock()

	// Create MALT CID from commitment bytes
	return codec.NewIPACid(commBytes[:])
}

// Prove generates an IPA proof.
func (s *Scheme) Prove(comm cid.Cid, arcs arcset.View, path string) (cid.Cid, []byte, error) {
	// Extract commitment bytes from MALT CID
	commBytes, err := codec.ExtractCommitment(comm)
	if err != nil {
		return cid.Cid{}, nil, fmt.Errorf("failed to extract commitment: %w", err)
	}

	s.mu.RLock()
	aux, ok := s.auxStore[string(commBytes)]
	s.mu.RUnlock()

	if !ok {
		return cid.Cid{}, nil, fmt.Errorf("commitment not found in auxiliary store")
	}

	proveIndex, ok := aux.pathToIndex[path]
	if !ok {
		return cid.Cid{}, nil, fmt.Errorf("path %s not found", path)
	}

	transcript := common.NewTranscript("malt-ipa")

	var c banderwagon.Element
	if err := c.SetBytes(commBytes); err != nil {
		return cid.Cid{}, nil, fmt.Errorf("failed to reconstruct commitment: %w", err)
	}

	var evalPoint fr.Element
	evalPoint.SetUint64(uint64(proveIndex))

	proof, err := ipa.CreateIPAProof(transcript, s.ipaConfig, c, aux.vector, evalPoint)
	if err != nil {
		return cid.Cid{}, nil, fmt.Errorf("failed to create IPA proof: %w", err)
	}

	proofBytes, err := s.serializeProof(&proof)
	if err != nil {
		return cid.Cid{}, nil, fmt.Errorf("failed to serialize proof: %w", err)
	}

	return aux.values[proveIndex], proofBytes, nil
}

// Verify verifies an IPA proof.
func (s *Scheme) Verify(comm cid.Cid, path string, value cid.Cid, proof []byte) (bool, error) {
	// Extract commitment bytes from MALT CID
	commBytes, err := codec.ExtractCommitment(comm)
	if err != nil {
		return false, fmt.Errorf("failed to extract commitment: %w", err)
	}

	s.mu.RLock()
	aux, ok := s.auxStore[string(commBytes)]
	s.mu.RUnlock()

	if !ok {
		return false, fmt.Errorf("commitment not found in auxiliary store")
	}

	index, ok := aux.pathToIndex[path]
	if !ok {
		return false, nil
	}

	ipaProof, err := s.deserializeProof(proof)
	if err != nil {
		return false, fmt.Errorf("failed to deserialize proof: %w", err)
	}

	transcript := common.NewTranscript("malt-ipa")

	var c banderwagon.Element
	if err := c.SetBytes(commBytes); err != nil {
		return false, fmt.Errorf("failed to reconstruct commitment: %w", err)
	}

	var evalPoint fr.Element
	evalPoint.SetUint64(uint64(index))

	output := cidToFieldElement(value)

	ok, err = ipa.CheckIPAProof(transcript, s.ipaConfig, c, *ipaProof, evalPoint, output)
	if err != nil {
		return false, fmt.Errorf("failed to check IPA proof: %w", err)
	}

	return ok, nil
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

	aux, ok := s.auxStore[string(commBytes)]
	if !ok {
		return cid.Cid{}, fmt.Errorf("commitment not found in auxiliary store")
	}

	updateIndex, ok := aux.pathToIndex[path]
	if !ok {
		return cid.Cid{}, fmt.Errorf("path %s not found", path)
	}

	newElement := cidToFieldElement(newValue)
	var diff fr.Element
	diff.Sub(&newElement, &aux.vector[updateIndex])

	var c banderwagon.Element
	if err := c.SetBytes(commBytes); err != nil {
		return cid.Cid{}, fmt.Errorf("failed to reconstruct commitment: %w", err)
	}

	var update banderwagon.Element
	update.ScalarMul(&s.ipaConfig.SRS[updateIndex], &diff)

	var newComm banderwagon.Element
	newComm.Add(&c, &update)

	result := newComm.Bytes()

	aux.vector[updateIndex] = newElement
	aux.values[updateIndex] = newValue
	s.auxStore[string(result[:])] = aux

	// Create MALT CID from new commitment bytes
	return codec.NewIPACid(result[:])
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

	aux, ok := s.auxStore[string(commBytes)]
	if !ok {
		return cid.Cid{}, fmt.Errorf("commitment not found in auxiliary store")
	}

	for path, update := range updates {
		index, ok := aux.pathToIndex[path]
		if !ok {
			return cid.Cid{}, fmt.Errorf("path %s not found", path)
		}

		newElement := cidToFieldElement(update.New)
		var diff fr.Element
		diff.Sub(&newElement, &aux.vector[index])

		var c banderwagon.Element
		if err := c.SetBytes(commBytes); err != nil {
			return cid.Cid{}, fmt.Errorf("failed to reconstruct commitment: %w", err)
		}

		var updateElem banderwagon.Element
		updateElem.ScalarMul(&s.ipaConfig.SRS[index], &diff)

		var newComm banderwagon.Element
		newComm.Add(&c, &updateElem)

		result := newComm.Bytes()
		commBytes = result[:]

		aux.vector[index] = newElement
		aux.values[index] = update.New
	}

	s.auxStore[string(commBytes)] = aux

	// Create MALT CID from new commitment bytes
	return codec.NewIPACid(commBytes)
}

// BatchProve generates proofs for multiple paths.
func (s *Scheme) BatchProve(comm cid.Cid, arcs arcset.View, paths []string) (map[string]arcset.BatchProofEntry, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("paths is empty")
	}

	// Extract commitment bytes from MALT CID
	commBytes, err := codec.ExtractCommitment(comm)
	if err != nil {
		return nil, fmt.Errorf("failed to extract commitment: %w", err)
	}

	s.mu.RLock()
	aux, ok := s.auxStore[string(commBytes)]
	s.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("commitment not found in auxiliary store")
	}

	results := make(map[string]arcset.BatchProofEntry, len(paths))

	for _, path := range paths {
		index, ok := aux.pathToIndex[path]
		if !ok {
			return nil, fmt.Errorf("path %s not found", path)
		}

		transcript := common.NewTranscript("malt-ipa")

		var c banderwagon.Element
		if err := c.SetBytes(commBytes); err != nil {
			return nil, fmt.Errorf("failed to reconstruct commitment: %w", err)
		}

		var evalPoint fr.Element
		evalPoint.SetUint64(uint64(index))

		proof, err := ipa.CreateIPAProof(transcript, s.ipaConfig, c, aux.vector, evalPoint)
		if err != nil {
			return nil, fmt.Errorf("failed to create IPA proof for path %s: %w", path, err)
		}

		proofBytes, err := s.serializeProof(&proof)
		if err != nil {
			return nil, fmt.Errorf("failed to serialize proof for path %s: %w", path, err)
		}

		results[path] = arcset.BatchProofEntry{
			Target: aux.values[index],
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

	var c banderwagon.Element
	if err := c.SetBytes(commBytes); err != nil {
		return false, fmt.Errorf("failed to reconstruct commitment: %w", err)
	}

	s.mu.RLock()
	aux, ok := s.auxStore[string(commBytes)]
	s.mu.RUnlock()

	if !ok {
		return false, fmt.Errorf("commitment not found in auxiliary store")
	}

	for path, entry := range proofs {
		index, ok := aux.pathToIndex[path]
		if !ok {
			return false, nil
		}

		ipaProof, err := s.deserializeProof(entry.Proof)
		if err != nil {
			return false, fmt.Errorf("failed to deserialize proof for path %s: %w", path, err)
		}

		transcript := common.NewTranscript("malt-ipa")

		var evalPoint fr.Element
		evalPoint.SetUint64(uint64(index))

		output := cidToFieldElement(entry.Target)

		valid, err := ipa.CheckIPAProof(transcript, s.ipaConfig, c, *ipaProof, evalPoint, output)
		if err != nil || !valid {
			return false, err
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
	aux, ok := s.auxStore[string(commBytes)]
	s.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("commitment not found in auxiliary store")
	}

	targets := make([]cid.Cid, len(paths))
	aggregatedProofData := make([]byte, 0)

	var c banderwagon.Element
	if err := c.SetBytes(commBytes); err != nil {
		return nil, fmt.Errorf("failed to reconstruct commitment: %w", err)
	}

	for i, path := range paths {
		index, ok := aux.pathToIndex[path]
		if !ok {
			return nil, fmt.Errorf("path %s not found", path)
		}

		targets[i] = aux.values[index]

		transcript := common.NewTranscript("malt-ipa-aggregate")

		var evalPoint fr.Element
		evalPoint.SetUint64(uint64(index))

		proof, err := ipa.CreateIPAProof(transcript, s.ipaConfig, c, aux.vector, evalPoint)
		if err != nil {
			return nil, fmt.Errorf("failed to create IPA proof: %w", err)
		}

		proofBytes, err := s.serializeProof(&proof)
		if err != nil {
			return nil, fmt.Errorf("failed to serialize proof: %w", err)
		}

		lenBytes := make([]byte, 4)
		binary.BigEndian.PutUint32(lenBytes, uint32(len(proofBytes)))
		aggregatedProofData = append(aggregatedProofData, lenBytes...)
		aggregatedProofData = append(aggregatedProofData, proofBytes...)
	}

	return &arcset.AggregatedProof{
		Paths:     paths,
		Targets:   targets,
		ProofData: aggregatedProofData,
	}, nil
}

// AggregateVerify verifies an aggregated proof.
func (s *Scheme) AggregateVerify(comm cid.Cid, aggProof *arcset.AggregatedProof) (bool, error) {
	if aggProof == nil || len(aggProof.Paths) == 0 {
		return false, fmt.Errorf("invalid aggregated proof")
	}

	// Extract commitment bytes from MALT CID
	commBytes, err := codec.ExtractCommitment(comm)
	if err != nil {
		return false, fmt.Errorf("failed to extract commitment: %w", err)
	}

	var c banderwagon.Element
	if err := c.SetBytes(commBytes); err != nil {
		return false, fmt.Errorf("failed to reconstruct commitment: %w", err)
	}

	s.mu.RLock()
	aux, ok := s.auxStore[string(commBytes)]
	s.mu.RUnlock()

	if !ok {
		return false, fmt.Errorf("commitment not found in auxiliary store")
	}

	offset := 0
	for i, path := range aggProof.Paths {
		index, ok := aux.pathToIndex[path]
		if !ok {
			return false, nil
		}

		if offset+4 > len(aggProof.ProofData) {
			return false, fmt.Errorf("proof data too short")
		}

		proofLen := int(binary.BigEndian.Uint32(aggProof.ProofData[offset : offset+4]))
		offset += 4

		if offset+proofLen > len(aggProof.ProofData) {
			return false, fmt.Errorf("proof data too short for proof %d", i)
		}

		proofBytes := aggProof.ProofData[offset : offset+proofLen]
		offset += proofLen

		ipaProof, err := s.deserializeProof(proofBytes)
		if err != nil {
			return false, fmt.Errorf("failed to deserialize proof %d: %w", i, err)
		}

		transcript := common.NewTranscript("malt-ipa-aggregate")

		var evalPoint fr.Element
		evalPoint.SetUint64(uint64(index))

		output := cidToFieldElement(aggProof.Targets[i])

		valid, err := ipa.CheckIPAProof(transcript, s.ipaConfig, c, *ipaProof, evalPoint, output)
		if err != nil || !valid {
			return false, err
		}
	}

	return true, nil
}

func (s *Scheme) serializeProof(proof *ipa.IPAProof) ([]byte, error) {
	numRounds := len(proof.L)
	totalSize := 4 + (numRounds*2+1)*32

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

	return result, nil
}

func (s *Scheme) deserializeProof(data []byte) (*ipa.IPAProof, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("proof data too short")
	}

	numRounds := int(binary.BigEndian.Uint32(data[0:4]))
	expectedSize := 4 + (numRounds*2+1)*32
	if len(data) != expectedSize {
		return nil, fmt.Errorf("proof data has wrong size: expected %d, got %d", expectedSize, len(data))
	}

	proof := &ipa.IPAProof{
		L: make([]banderwagon.Element, numRounds),
		R: make([]banderwagon.Element, numRounds),
	}

	offset := 4
	for i := 0; i < numRounds; i++ {
		if err := proof.L[i].SetBytes(data[offset : offset+32]); err != nil {
			return nil, fmt.Errorf("failed to parse L[%d]: %w", i, err)
		}
		offset += 32
	}
	for i := 0; i < numRounds; i++ {
		if err := proof.R[i].SetBytes(data[offset : offset+32]); err != nil {
			return nil, fmt.Errorf("failed to parse R[%d]: %w", i, err)
		}
		offset += 32
	}

	proof.A_scalar.SetBytesLE(data[offset : offset+32])

	return proof, nil
}

func cidToFieldElement(c cid.Cid) fr.Element {
	var result fr.Element
	bytes := c.Bytes()
	h := sha256.Sum256(bytes)
	result.SetBytes(h[:])
	return result
}

// Ensure Scheme implements commitment.Scheme.
var _ commitment.Scheme = (*Scheme)(nil)