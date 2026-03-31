// Package ipa provides an IPA (Inner Product Argument) commitment implementation.
package ipa

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"sync"

	"github.com/crate-crypto/go-ipa/bandersnatch/fr"
	"github.com/crate-crypto/go-ipa/banderwagon"
	"github.com/crate-crypto/go-ipa/common"
	ipa "github.com/crate-crypto/go-ipa/ipa"
	"github.com/dewebprotocol/malt/internal/sce"
	"github.com/dewebprotocol/malt/key"
)

const (
	// MaxVectorSize is the maximum number of arcs per structure (IPA constraint).
	MaxVectorSize = 256
)

// Commitment implements sce.CommitmentScheme using Inner Product Arguments.
type Commitment struct {
	config *Config

	// auxStore stores auxiliary data for local updates
	mu       sync.RWMutex
	auxStore map[string]*auxData
}

// Config holds configuration for IPA commitment.
type Config struct {
	// VectorSize is the maximum size of the vector (must be <= 256)
	VectorSize int

	// ipaConfig is the underlying IPA configuration
	ipaConfig *ipa.IPAConfig
}

// NewConfig creates a new IPA configuration.
func NewConfig(vectorSize int) (*Config, error) {
	if vectorSize <= 0 || vectorSize > MaxVectorSize {
		return nil, fmt.Errorf("vector size must be between 1 and %d, got %d", MaxVectorSize, vectorSize)
	}

	// Initialize IPA settings (generates SRS)
	ipaConfig, err := ipa.NewIPASettings()
	if err != nil {
		return nil, fmt.Errorf("failed to create IPA settings: %w", err)
	}

	return &Config{
		VectorSize: vectorSize,
		ipaConfig:  ipaConfig,
	}, nil
}

// auxData holds auxiliary data for a commitment.
type auxData struct {
	vector      []fr.Element
	pathToIndex map[string]int
	indexToPath map[int]string
}

// NewCommitment creates a new IPA commitment scheme.
func NewCommitment(cfg *Config) (*Commitment, error) {
	if cfg == nil {
		var err error
		cfg, err = NewConfig(MaxVectorSize)
		if err != nil {
			return nil, err
		}
	}

	return &Commitment{
		config:    cfg,
		auxStore:  make(map[string]*auxData),
	}, nil
}

// Commit generates an IPA commitment for an arc set.
func (i *Commitment) Commit(arcs sce.ArcSetView) (key.Key, error) {
	if arcs == nil {
		return nil, fmt.Errorf("arc set is nil")
	}

	// Convert arc set to vector
	vector, pathToIndex, indexToPath, err := i.arcSetToVector(arcs)
	if err != nil {
		return nil, fmt.Errorf("failed to convert arc set to vector: %w", err)
	}

	// Compute IPA commitment
	comm := i.config.ipaConfig.Commit(vector)

	// Store auxiliary data for later updates
	commBytes := comm.Bytes()
	i.mu.Lock()
	i.auxStore[string(commBytes[:])] = &auxData{
		vector:      vector,
		pathToIndex: pathToIndex,
		indexToPath: indexToPath,
	}
	i.mu.Unlock()

	return key.NewStructureRoot(commBytes[:]), nil
}

// Prove generates an IPA proof for an arc.
func (i *Commitment) Prove(root key.Key, arcs sce.ArcSetView, path string) (key.Key, sce.Proof, error) {
	if root.Kind() != key.KeyKindStructureRoot {
		return nil, nil, fmt.Errorf("expected StructureRoot, got %v", root.Kind())
	}

	// Get auxiliary data
	i.mu.RLock()
	aux, ok := i.auxStore[string(root.Bytes())]
	i.mu.RUnlock()

	if !ok {
		return nil, nil, fmt.Errorf("commitment not found in auxiliary store")
	}

	// Get the index for the path
	index, ok := aux.pathToIndex[path]
	if !ok {
		return nil, nil, fmt.Errorf("path %s not found in path index", path)
	}

	// Get target key
	target, ok := arcs.Get(path)
	if !ok {
		return nil, nil, fmt.Errorf("path %s not found in arc set", path)
	}

	// Create transcript
	transcript := common.NewTranscript("malt-ipa")

	// Reconstruct commitment from bytes
	var comm banderwagon.Element
	commBytes := root.Bytes()
	if err := comm.SetBytes(commBytes); err != nil {
		return nil, nil, fmt.Errorf("failed to reconstruct commitment: %w", err)
	}

	// Evaluation point (index in the domain)
	var evalPoint fr.Element
	evalPoint.SetUint64(uint64(index))

	// Create IPA proof
	proof, err := ipa.CreateIPAProof(transcript, i.config.ipaConfig, comm, aux.vector, evalPoint)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create IPA proof: %w", err)
	}

	// Serialize proof
	proofBytes, err := i.serializeProof(&proof)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to serialize proof: %w", err)
	}

	return target, sce.Proof(proofBytes), nil
}

// Verify verifies an IPA proof.
func (i *Commitment) Verify(root key.Key, path string, target key.Key, proof sce.Proof) (bool, error) {
	if root.Kind() != key.KeyKindStructureRoot {
		return false, fmt.Errorf("expected StructureRoot, got %v", root.Kind())
	}

	// Get auxiliary data for path index
	i.mu.RLock()
	aux, ok := i.auxStore[string(root.Bytes())]
	i.mu.RUnlock()

	if !ok {
		return false, fmt.Errorf("commitment not found in auxiliary store")
	}

	// Get the index for the path
	index, ok := aux.pathToIndex[path]
	if !ok {
		return false, nil
	}

	// Deserialize proof
	ipaProof, err := i.deserializeProof(proof)
	if err != nil {
		return false, fmt.Errorf("failed to deserialize proof: %w", err)
	}

	// Create transcript
	transcript := common.NewTranscript("malt-ipa")

	// Reconstruct commitment from bytes
	var comm banderwagon.Element
	commBytes := root.Bytes()
	if err := comm.SetBytes(commBytes); err != nil {
		return false, fmt.Errorf("failed to reconstruct commitment: %w", err)
	}

	// Evaluation point
	var evalPoint fr.Element
	evalPoint.SetUint64(uint64(index))

	// Expected output (target converted to field element)
	output := keyToFieldElement(target)

	// Verify IPA proof
	ok, err = ipa.CheckIPAProof(transcript, i.config.ipaConfig, comm, *ipaProof, evalPoint, output)
	if err != nil {
		return false, fmt.Errorf("failed to check IPA proof: %w", err)
	}

	return ok, nil
}

// Update updates the commitment for a changed arc.
func (i *Commitment) Update(root key.Key, arcs sce.ArcSetView, path string, oldKey, newKey key.Key) (key.Key, error) {
	if root.Kind() != key.KeyKindStructureRoot {
		return nil, fmt.Errorf("expected StructureRoot, got %v", root.Kind())
	}

	// Get auxiliary data
	i.mu.Lock()
	defer i.mu.Unlock()

	aux, ok := i.auxStore[string(root.Bytes())]
	if !ok {
		return nil, fmt.Errorf("commitment not found in auxiliary store")
	}

	// Get the index for the path
	index, ok := aux.pathToIndex[path]
	if !ok {
		return nil, fmt.Errorf("path %s not found in path index", path)
	}

	// Verify old key matches
	oldElement := keyToFieldElement(oldKey)
	if !aux.vector[index].Equal(&oldElement) {
		return nil, fmt.Errorf("old key mismatch")
	}

	// Compute new element
	newElement := keyToFieldElement(newKey)

	// Compute the difference
	var diff fr.Element
	diff.Sub(&newElement, &aux.vector[index])

	// Update the commitment: C' = C + diff * G[index]
	commBytes, err := i.updateCommitment(root.Bytes(), index, diff)
	if err != nil {
		return nil, fmt.Errorf("failed to update commitment: %w", err)
	}

	// Update auxiliary data
	aux.vector[index] = newElement

	// Store under new commitment
	i.auxStore[string(commBytes)] = aux

	return key.NewStructureRoot(commBytes), nil
}

// BatchUpdate updates multiple arcs.
func (i *Commitment) BatchUpdate(root key.Key, arcs sce.ArcSetView, updates map[string]struct {
	Old key.Key
	New key.Key
}) (key.Key, error) {
	if root.Kind() != key.KeyKindStructureRoot {
		return nil, fmt.Errorf("expected StructureRoot, got %v", root.Kind())
	}

	i.mu.Lock()
	defer i.mu.Unlock()

	aux, ok := i.auxStore[string(root.Bytes())]
	if !ok {
		return nil, fmt.Errorf("commitment not found in auxiliary store")
	}

	// Apply all updates
	commBytes := root.Bytes()
	for path, update := range updates {
		index, ok := aux.pathToIndex[path]
		if !ok {
			return nil, fmt.Errorf("path %s not found in path index", path)
		}

		oldElement := keyToFieldElement(update.Old)
		if !aux.vector[index].Equal(&oldElement) {
			return nil, fmt.Errorf("old key mismatch for path %s", path)
		}

		newElement := keyToFieldElement(update.New)
		var diff fr.Element
		diff.Sub(&newElement, &aux.vector[index])

		var err error
		commBytes, err = i.updateCommitment(commBytes, index, diff)
		if err != nil {
			return nil, fmt.Errorf("failed to update commitment for path %s: %w", path, err)
		}

		aux.vector[index] = newElement
	}

	i.auxStore[string(commBytes[:])] = aux

	return key.NewStructureRoot(commBytes[:]), nil
}

// arcSetToVector converts an arc set to an IPA vector.
func (i *Commitment) arcSetToVector(arcs sce.ArcSetView) ([]fr.Element, map[string]int, map[int]string, error) {
	vector := make([]fr.Element, i.config.VectorSize)
	pathToIndex := make(map[string]int)
	indexToPath := make(map[int]string)

	// Initialize with zeros
	zero := fr.Element{}
	zero.SetZero()
	for j := range vector {
		vector[j] = zero
	}

	// Collect and sort paths for deterministic indexing
	var paths []string
	iter := arcs.Iterate()
	for {
		path, _, ok := iter.Next()
		if !ok {
			break
		}
		paths = append(paths, path)
	}
	if iter.Err() != nil {
		return nil, nil, nil, iter.Err()
	}
	sort.Strings(paths)

	// Map paths to indices
	for idx, path := range paths {
		if idx >= i.config.VectorSize {
			return nil, nil, nil, fmt.Errorf("arc set exceeds maximum vector size (%d)", i.config.VectorSize)
		}

		target, ok := arcs.Get(path)
		if !ok {
			continue
		}

		vector[idx] = keyToFieldElement(target)
		pathToIndex[path] = idx
		indexToPath[idx] = path
	}

	return vector, pathToIndex, indexToPath, nil
}

// updateCommitment performs O(1) commitment update.
func (i *Commitment) updateCommitment(commBytes []byte, index int, diff fr.Element) ([]byte, error) {
	// Reconstruct commitment
	var comm banderwagon.Element
	if err := comm.SetBytes(commBytes); err != nil {
		return nil, fmt.Errorf("failed to reconstruct commitment: %w", err)
	}

	// Compute diff * G[index]
	var update banderwagon.Element
	update.ScalarMul(&i.config.ipaConfig.SRS[index], &diff)

	// C' = C + update
	var newComm banderwagon.Element
	newComm.Add(&comm, &update)

	result := newComm.Bytes()
	return result[:], nil
}

// serializeProof serializes an IPA proof to bytes.
func (i *Commitment) serializeProof(proof *ipa.IPAProof) ([]byte, error) {
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

// deserializeProof deserializes bytes to an IPA proof.
func (i *Commitment) deserializeProof(data sce.Proof) (*ipa.IPAProof, error) {
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

// keyToFieldElement converts a Key to a field element.
func keyToFieldElement(k key.Key) fr.Element {
	var result fr.Element
	bytes := k.Bytes()

	// Hash the key bytes to get a deterministic field element
	h := sha256.Sum256(bytes)

	result.SetBytes(h[:])

	return result
}

// Ensure Commitment implements sce.CommitmentScheme.
var _ sce.CommitmentScheme = (*Commitment)(nil)