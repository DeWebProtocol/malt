// Package ipa provides an IPA (Inner Product Argument) commitment backend.
package ipa

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	multiproof "github.com/crate-crypto/go-ipa"
	"github.com/crate-crypto/go-ipa/bandersnatch/fr"
	"github.com/crate-crypto/go-ipa/banderwagon"
	"github.com/crate-crypto/go-ipa/common"
	ipa "github.com/crate-crypto/go-ipa/ipa"
	"github.com/dewebprotocol/malt/auth/commitment"
	"github.com/dewebprotocol/malt/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

const (
	// MaxValues is the maximum number of values per commitment.
	MaxValues = 256
	// ProofSize is the size of a primitive IPA index proof in bytes.
	// For 256 elements: numRounds=8, size=4 + 8*32(L) + 8*32(R) + 32(A_scalar) + 4(index) = 552
	ProofSize = 552
	// MaxCacheEntries is the maximum number of cached commitments.
	// When exceeded, the oldest entries are evicted.
	MaxCacheEntries = 1024
)

const (
	singleTranscriptLabel = "malt-ipa"
	batchTranscriptLabel  = "malt-ipa-batch"
)

// Scheme implements an IPA-based index commitment backend.
type Scheme struct {
	ipaConfig *ipa.IPAConfig
}

// NewScheme creates a new IPA commitment scheme.
func NewScheme() (*Scheme, error) {
	ipaConfig, err := ipa.NewIPASettings()
	if err != nil {
		return nil, fmt.Errorf("failed to create IPA settings: %w", err)
	}

	return &Scheme{
		ipaConfig: ipaConfig,
	}, nil
}

// MaxValues returns the maximum number of authenticated slots.
func (s *Scheme) MaxValues() int {
	return MaxValues
}

// Commit commits a stable indexed cell vector.
func (s *Scheme) Commit(values []commitment.Cell) (cid.Cid, error) {
	return s.commitValues(values)
}

// Prove proves the value at a stable index.
func (s *Scheme) Prove(values []commitment.Cell, index uint64) (cid.Cid, commitment.Cell, []byte, error) {
	comm, err := s.commitValues(values)
	if err != nil {
		return cid.Undef, nil, nil, err
	}
	if index >= uint64(len(values)) {
		return cid.Undef, nil, nil, fmt.Errorf("index %d out of range", index)
	}
	value, proof, err := s.proveValuesIndex(comm, values, index)
	return comm, value, proof, err
}

// BatchProve proves multiple stable indices with one batch proof payload.
func (s *Scheme) BatchProve(values []commitment.Cell, indices []uint64) (cid.Cid, []commitment.Cell, []byte, error) {
	comm, err := s.commitValues(values)
	if err != nil {
		return cid.Undef, nil, nil, err
	}
	if len(indices) == 0 {
		return cid.Undef, nil, nil, fmt.Errorf("indices must not be empty")
	}

	commBytes, err := maltcid.ExtractCommitment(comm)
	if err != nil {
		return cid.Undef, nil, nil, fmt.Errorf("failed to extract commitment: %w", err)
	}

	var c banderwagon.Element
	if err := c.SetBytes(commBytes); err != nil {
		return cid.Undef, nil, nil, fmt.Errorf("failed to reconstruct commitment: %w", err)
	}

	vector := valuesToVector(values)
	commitments := make([]banderwagon.Element, len(indices))
	cs := make([]*banderwagon.Element, len(indices))
	fs := make([][]fr.Element, len(indices))
	zs := make([]uint8, len(indices))
	proved := make([]commitment.Cell, len(indices))
	for i, index := range indices {
		if index >= uint64(len(values)) {
			return cid.Undef, nil, nil, fmt.Errorf("index %d out of range", index)
		}
		if index >= MaxValues {
			return cid.Undef, nil, nil, fmt.Errorf("index %d exceeds max %d", index, MaxValues-1)
		}

		commitments[i] = c
		cs[i] = &commitments[i]
		fs[i] = vector
		zs[i] = uint8(index)
		proved[i] = commitment.NewCell(values[int(index)])
	}

	transcript := common.NewTranscript(batchTranscriptLabel)
	proof, err := multiproof.CreateMultiProof(transcript, s.ipaConfig, cs, fs, zs)
	if err != nil {
		return cid.Undef, nil, nil, fmt.Errorf("failed to create IPA batch proof: %w", err)
	}

	proofBytes, err := serializeMultiProof(proof)
	if err != nil {
		return cid.Undef, nil, nil, fmt.Errorf("failed to serialize IPA batch proof: %w", err)
	}
	return comm, proved, proofBytes, nil
}

func (s *Scheme) proveValuesIndex(comm cid.Cid, values []commitment.Cell, index uint64) (commitment.Cell, []byte, error) {
	vector := valuesToVector(values)
	commBytes, err := maltcid.ExtractCommitment(comm)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to extract commitment: %w", err)
	}

	transcript := common.NewTranscript(singleTranscriptLabel)

	var c banderwagon.Element
	if err := c.SetBytes(commBytes); err != nil {
		return nil, nil, fmt.Errorf("failed to reconstruct commitment: %w", err)
	}

	var evalPoint fr.Element
	evalPoint.SetUint64(index)

	proof, err := ipa.CreateIPAProof(transcript, s.ipaConfig, c, vector, evalPoint)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create IPA proof: %w", err)
	}

	proofBytes, err := s.serializeProof(&proof, int(index))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to serialize proof: %w", err)
	}

	valueIndex := int(index)
	if valueIndex < 0 || valueIndex >= len(values) {
		return nil, nil, fmt.Errorf("index %d out of range", index)
	}
	return commitment.NewCell(values[valueIndex]), proofBytes, nil
}

// VerifyIndex verifies a proof for a stable index without requiring cache state.
func (s *Scheme) VerifyIndex(comm cid.Cid, index uint64, value commitment.Cell, proof []byte) (bool, error) {
	commBytes, err := maltcid.ExtractCommitment(comm)
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

	transcript := common.NewTranscript(singleTranscriptLabel)

	var c banderwagon.Element
	if err := c.SetBytes(commBytes); err != nil {
		return false, fmt.Errorf("failed to reconstruct commitment: %w", err)
	}

	var evalPointFr fr.Element
	evalPointFr.SetUint64(index)

	output := cellToFieldElement(value)
	ok, err := ipa.CheckIPAProof(transcript, s.ipaConfig, c, *ipaProof, evalPointFr, output)
	if err != nil {
		return false, fmt.Errorf("failed to check IPA proof: %w", err)
	}
	return ok, nil
}

// BatchVerify verifies a batch proof for an ordered index list.
func (s *Scheme) BatchVerify(comm cid.Cid, indices []uint64, values []commitment.Cell, proof []byte) (bool, error) {
	if len(indices) == 0 {
		return false, fmt.Errorf("indices must not be empty")
	}
	if len(indices) != len(values) {
		return false, fmt.Errorf("indices/value length mismatch: %d != %d", len(indices), len(values))
	}

	commBytes, err := maltcid.ExtractCommitment(comm)
	if err != nil {
		return false, fmt.Errorf("failed to extract commitment: %w", err)
	}

	var c banderwagon.Element
	if err := c.SetBytes(commBytes); err != nil {
		return false, fmt.Errorf("failed to reconstruct commitment: %w", err)
	}

	mp, err := deserializeMultiProof(proof)
	if err != nil {
		return false, fmt.Errorf("failed to deserialize IPA batch proof: %w", err)
	}

	commitments := make([]banderwagon.Element, len(indices))
	cs := make([]*banderwagon.Element, len(indices))
	outputs := make([]fr.Element, len(indices))
	ys := make([]*fr.Element, len(indices))
	zs := make([]uint8, len(indices))
	for i, index := range indices {
		if index >= MaxValues {
			return false, fmt.Errorf("index %d exceeds max %d", index, MaxValues-1)
		}

		commitments[i] = c
		cs[i] = &commitments[i]
		outputs[i] = cellToFieldElement(values[i])
		ys[i] = &outputs[i]
		zs[i] = uint8(index)
	}

	transcript := common.NewTranscript(batchTranscriptLabel)
	ok, err := multiproof.CheckMultiProof(transcript, s.ipaConfig, mp, cs, ys, zs)
	if err != nil {
		return false, fmt.Errorf("failed to check IPA batch proof: %w", err)
	}
	return ok, nil
}

// VerifyProof verifies a proof carrying its own index metadata.
func (s *Scheme) VerifyProof(comm cid.Cid, value commitment.Cell, proof []byte) (bool, error) {
	commBytes, err := maltcid.ExtractCommitment(comm)
	if err != nil {
		return false, fmt.Errorf("failed to extract commitment: %w", err)
	}

	ipaProof, index, err := s.deserializeProof(proof)
	if err != nil {
		return false, fmt.Errorf("failed to deserialize proof: %w", err)
	}

	transcript := common.NewTranscript(singleTranscriptLabel)

	var c banderwagon.Element
	if err := c.SetBytes(commBytes); err != nil {
		return false, fmt.Errorf("failed to reconstruct commitment: %w", err)
	}

	var evalPointFr fr.Element
	evalPointFr.SetUint64(index)

	output := cellToFieldElement(value)
	ok, err := ipa.CheckIPAProof(transcript, s.ipaConfig, c, *ipaProof, evalPointFr, output)
	if err != nil {
		return false, fmt.Errorf("failed to check IPA proof: %w", err)
	}
	return ok, nil
}

// Replace performs an index-stable replacement.
func (s *Scheme) Replace(values []commitment.Cell, index uint64, oldValue, newValue commitment.Cell) (cid.Cid, error) {
	if index >= uint64(len(values)) {
		return cid.Cid{}, fmt.Errorf("index %d out of range", index)
	}
	if !values[index].Equal(oldValue) {
		return cid.Cid{}, fmt.Errorf("old value mismatch at index %d", index)
	}

	nextValues := commitment.CloneCells(values)
	nextValues[index] = commitment.NewCell(newValue)
	return s.commitValues(nextValues)
}

// serializeProof serializes an IPA proof with index information.
func (s *Scheme) serializeProof(proof *ipa.IPAProof, index int) ([]byte, error) {
	numRounds := len(proof.L)
	totalSize := 4 + (numRounds*2+1)*32 + 4

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

	binary.BigEndian.PutUint32(result[offset:offset+4], uint32(index))

	return result, nil
}

// deserializeProof deserializes an IPA proof and returns the proof and index.
func (s *Scheme) deserializeProof(data []byte) (*ipa.IPAProof, uint64, error) {
	if len(data) < 40 {
		return nil, 0, fmt.Errorf("proof data too short")
	}

	numRounds := int(binary.BigEndian.Uint32(data[0:4]))
	expectedSize := 4 + (numRounds*2+1)*32 + 4
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

	index := uint64(binary.BigEndian.Uint32(data[offset : offset+4]))

	return proof, index, nil
}

func serializeMultiProof(proof *multiproof.MultiProof) ([]byte, error) {
	var buf bytes.Buffer
	if err := proof.Write(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func deserializeMultiProof(data []byte) (*multiproof.MultiProof, error) {
	var proof multiproof.MultiProof
	if err := proof.Read(bytes.NewReader(data)); err != nil {
		return nil, err
	}
	return &proof, nil
}

func cellToFieldElement(cell commitment.Cell) fr.Element {
	var result fr.Element
	h := sha256.Sum256(cell)
	result.SetBytes(h[:])
	return result
}

func (s *Scheme) commitValues(values []commitment.Cell) (cid.Cid, error) {
	if len(values) > MaxValues {
		return cid.Cid{}, fmt.Errorf("too many values: %d > %d", len(values), MaxValues)
	}

	vector := valuesToVector(values)

	comm := s.ipaConfig.Commit(vector)
	commBytes := comm.Bytes()
	return maltcid.NewIPACid(commBytes[:])
}

func valuesToVector(values []commitment.Cell) []fr.Element {
	vector := make([]fr.Element, MaxValues)
	zero := fr.Element{}
	zero.SetZero()
	for i := range vector {
		vector[i] = zero
	}
	for i, value := range values {
		vector[i] = cellToFieldElement(value)
	}
	return vector
}

// Ensure Scheme implements commitment.IndexCommitment.
var _ commitment.IndexCommitment = (*Scheme)(nil)
var _ commitment.IndexVerifier = (*Scheme)(nil)
var _ commitment.IndexProver = (*Scheme)(nil)
