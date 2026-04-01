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
	"github.com/dewebprotocol/malt/arcset"
	"github.com/dewebprotocol/malt/internal/sce/commitment"
	"github.com/dewebprotocol/malt/key"
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
	values      []key.Key
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
func (s *Scheme) Commit(arcs arcset.View) (key.Key, error) {
	paths, values := extractSortedPathsValues(arcs)

	if len(paths) > MaxValues {
		return nil, fmt.Errorf("too many values: %d > %d", len(paths), MaxValues)
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
		vector[idx] = keyToFieldElement(values[idx])
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

	return key.NewStructureRoot(commBytes[:]), nil
}

// Prove generates an IPA proof.
func (s *Scheme) Prove(comm key.Key, arcs arcset.View, path string) (key.Key, arcset.Proof, error) {
	s.mu.RLock()
	aux, ok := s.auxStore[string(comm.Bytes())]
	s.mu.RUnlock()

	if !ok {
		return nil, nil, fmt.Errorf("commitment not found in auxiliary store")
	}

	proveIndex, ok := aux.pathToIndex[path]
	if !ok {
		return nil, nil, fmt.Errorf("path %s not found", path)
	}

	transcript := common.NewTranscript("malt-ipa")

	var c banderwagon.Element
	if err := c.SetBytes(comm.Bytes()); err != nil {
		return nil, nil, fmt.Errorf("failed to reconstruct commitment: %w", err)
	}

	var evalPoint fr.Element
	evalPoint.SetUint64(uint64(proveIndex))

	proof, err := ipa.CreateIPAProof(transcript, s.ipaConfig, c, aux.vector, evalPoint)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create IPA proof: %w", err)
	}

	proofBytes, err := s.serializeProof(&proof)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to serialize proof: %w", err)
	}

	return aux.values[proveIndex], arcset.Proof(proofBytes), nil
}

// Verify verifies an IPA proof.
func (s *Scheme) Verify(comm key.Key, path string, value key.Key, proof arcset.Proof) (bool, error) {
	s.mu.RLock()
	aux, ok := s.auxStore[string(comm.Bytes())]
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
	if err := c.SetBytes(comm.Bytes()); err != nil {
		return false, fmt.Errorf("failed to reconstruct commitment: %w", err)
	}

	var evalPoint fr.Element
	evalPoint.SetUint64(uint64(index))

	output := keyToFieldElement(value)

	ok, err = ipa.CheckIPAProof(transcript, s.ipaConfig, c, *ipaProof, evalPoint, output)
	if err != nil {
		return false, fmt.Errorf("failed to check IPA proof: %w", err)
	}

	return ok, nil
}

// Update updates a value in the commitment.
func (s *Scheme) Update(comm key.Key, arcs arcset.View, path string, oldValue, newValue key.Key) (key.Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	aux, ok := s.auxStore[string(comm.Bytes())]
	if !ok {
		return nil, fmt.Errorf("commitment not found in auxiliary store")
	}

	updateIndex, ok := aux.pathToIndex[path]
	if !ok {
		return nil, fmt.Errorf("path %s not found", path)
	}

	newElement := keyToFieldElement(newValue)
	var diff fr.Element
	diff.Sub(&newElement, &aux.vector[updateIndex])

	var c banderwagon.Element
	if err := c.SetBytes(comm.Bytes()); err != nil {
		return nil, fmt.Errorf("failed to reconstruct commitment: %w", err)
	}

	var update banderwagon.Element
	update.ScalarMul(&s.ipaConfig.SRS[updateIndex], &diff)

	var newComm banderwagon.Element
	newComm.Add(&c, &update)

	result := newComm.Bytes()

	aux.vector[updateIndex] = newElement
	aux.values[updateIndex] = newValue
	s.auxStore[string(result[:])] = aux

	return key.NewStructureRoot(result[:]), nil
}

// BatchUpdate updates multiple values.
func (s *Scheme) BatchUpdate(comm key.Key, arcs arcset.View, updates map[string]struct {
	Old key.Key
	New key.Key
}) (key.Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	aux, ok := s.auxStore[string(comm.Bytes())]
	if !ok {
		return nil, fmt.Errorf("commitment not found in auxiliary store")
	}

	commBytes := comm.Bytes()
	for path, update := range updates {
		index, ok := aux.pathToIndex[path]
		if !ok {
			return nil, fmt.Errorf("path %s not found", path)
		}

		newElement := keyToFieldElement(update.New)
		var diff fr.Element
		diff.Sub(&newElement, &aux.vector[index])

		var c banderwagon.Element
		if err := c.SetBytes(commBytes); err != nil {
			return nil, fmt.Errorf("failed to reconstruct commitment: %w", err)
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

	return key.NewStructureRoot(commBytes), nil
}

// ProveBatch generates proofs for multiple paths.
func (s *Scheme) ProveBatch(comm key.Key, arcs arcset.View, paths []string) (map[string]arcset.BatchProofEntry, error) {
	s.mu.RLock()
	aux, ok := s.auxStore[string(comm.Bytes())]
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
		if err := c.SetBytes(comm.Bytes()); err != nil {
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
			Proof:  arcset.Proof(proofBytes),
		}
	}

	return results, nil
}

// VerifyBatch verifies multiple proofs.
func (s *Scheme) VerifyBatch(comm key.Key, proofs map[string]arcset.BatchProofEntry) (bool, error) {
	var c banderwagon.Element
	if err := c.SetBytes(comm.Bytes()); err != nil {
		return false, fmt.Errorf("failed to reconstruct commitment: %w", err)
	}

	s.mu.RLock()
	aux, ok := s.auxStore[string(comm.Bytes())]
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

		output := keyToFieldElement(entry.Target)

		valid, err := ipa.CheckIPAProof(transcript, s.ipaConfig, c, *ipaProof, evalPoint, output)
		if err != nil || !valid {
			return false, err
		}
	}

	return true, nil
}

// ProveAggregate generates an aggregated proof.
func (s *Scheme) ProveAggregate(comm key.Key, arcs arcset.View, paths []string) (*arcset.AggregatedProof, error) {
	s.mu.RLock()
	aux, ok := s.auxStore[string(comm.Bytes())]
	s.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("commitment not found in auxiliary store")
	}

	targets := make([]key.Key, len(paths))
	aggregatedProofData := make([]byte, 0)

	var c banderwagon.Element
	if err := c.SetBytes(comm.Bytes()); err != nil {
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

// VerifyAggregate verifies an aggregated proof.
func (s *Scheme) VerifyAggregate(comm key.Key, aggProof *arcset.AggregatedProof) (bool, error) {
	if aggProof == nil || len(aggProof.Paths) == 0 {
		return false, fmt.Errorf("invalid aggregated proof")
	}

	var c banderwagon.Element
	if err := c.SetBytes(comm.Bytes()); err != nil {
		return false, fmt.Errorf("failed to reconstruct commitment: %w", err)
	}

	s.mu.RLock()
	aux, ok := s.auxStore[string(comm.Bytes())]
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

		ipaProof, err := s.deserializeProof(arcset.Proof(proofBytes))
		if err != nil {
			return false, fmt.Errorf("failed to deserialize proof %d: %w", i, err)
		}

		transcript := common.NewTranscript("malt-ipa-aggregate")

		var evalPoint fr.Element
		evalPoint.SetUint64(uint64(index))

		output := keyToFieldElement(aggProof.Targets[i])

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

func (s *Scheme) deserializeProof(data arcset.Proof) (*ipa.IPAProof, error) {
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

func keyToFieldElement(k key.Key) fr.Element {
	var result fr.Element
	bytes := k.Bytes()
	h := sha256.Sum256(bytes)
	result.SetBytes(h[:])
	return result
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