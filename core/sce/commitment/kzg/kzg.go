// Package kzg provides a KZG polynomial commitment backend.
package kzg

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/big"
	"math/bits"
	"sync"

	blsfr "github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	gokzg4844 "github.com/crate-crypto/go-kzg-4844"
	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/sce/commitment"
	cid "github.com/ipfs/go-cid"
)

// bls12381ScalarMod is the BLS12-381 scalar field modulus.
var bls12381ScalarMod, _ = new(big.Int).SetString("73eda753299d7d483339d80809a1d80553bda402fffe5bfeffffffff00000001", 16)

const (
	// MaxValues is the maximum number of values per commitment (KZG constraint).
	MaxValues = 4096
	// ProofSize is the size of a primitive KZG index proof in bytes.
	ProofSize = 84
	// MaxCacheEntries is the maximum number of cached commitments.
	// When exceeded, the oldest entries are evicted.
	MaxCacheEntries = 1024
)

// Scheme implements a KZG-based index commitment backend.
type Scheme struct {
	context      *gokzg4844.Context
	domainPoints []gokzg4844.Scalar

	mu    sync.RWMutex
	cache map[string]*cacheEntry
	order []string
}

type cacheEntry struct {
	blob   *gokzg4844.Blob
	values []commitment.Cell
}

// NewScheme creates a new KZG commitment scheme.
func NewScheme() (*Scheme, error) {
	context, err := gokzg4844.NewContext4096Secure()
	if err != nil {
		return nil, fmt.Errorf("failed to create KZG context: %w", err)
	}
	domainPoints, err := buildDomainPoints(MaxValues)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize KZG domain: %w", err)
	}

	return &Scheme{
		context:      context,
		domainPoints: domainPoints,
		cache:        make(map[string]*cacheEntry),
		order:        make([]string, 0, MaxCacheEntries),
	}, nil
}

// MaxValues returns the maximum number of authenticated slots.
func (s *Scheme) MaxValues() int {
	return MaxValues
}

// CommitValues commits a stable indexed cell vector.
func (s *Scheme) CommitValues(values []commitment.Cell) (cid.Cid, error) {
	return s.commitValues(values)
}

// ProveIndex proves the value at a stable index.
func (s *Scheme) ProveIndex(comm cid.Cid, values []commitment.Cell, index uint64) (commitment.Cell, []byte, error) {
	entry, err := s.ensureState(comm, values)
	if err != nil {
		return nil, nil, err
	}
	if index >= uint64(len(entry.values)) {
		return nil, nil, fmt.Errorf("index %d out of range", index)
	}
	return s.proveEntryIndex(entry, index)
}

func (s *Scheme) proveEntryIndex(entry *cacheEntry, index uint64) (commitment.Cell, []byte, error) {
	inputPoint := s.domainPoint(index)
	proof, claimedValue, err := s.context.ComputeKZGProof(entry.blob, inputPoint, 1)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to compute proof: %w", err)
	}
	return commitment.NewCell(entry.values[index]), serializeProof(proof, claimedValue, index), nil
}

// BatchProve currently concatenates single-index KZG proofs because the
// current go-kzg-4844 dependency does not expose batch opening generation.
// TODO: replace this looped encoding with a real KZG multiproof when the
// backend supports batch opening generation for our index-commitment setting.
func (s *Scheme) BatchProve(comm cid.Cid, values []commitment.Cell, indices []uint64) ([]commitment.Cell, []byte, error) {
	entry, err := s.ensureState(comm, values)
	if err != nil {
		return nil, nil, err
	}
	if len(indices) == 0 {
		return nil, nil, fmt.Errorf("indices must not be empty")
	}

	proved := make([]commitment.Cell, len(indices))
	proofs := make([][]byte, len(indices))
	for i, index := range indices {
		if index >= uint64(len(entry.values)) {
			return nil, nil, fmt.Errorf("index %d out of range", index)
		}

		value, proof, err := s.proveEntryIndex(entry, index)
		if err != nil {
			return nil, nil, err
		}
		proved[i] = value
		proofs[i] = proof
	}
	return proved, serializeBatchProof(proofs), nil
}

// VerifyIndex verifies a proof for a stable index without cache state.
func (s *Scheme) VerifyIndex(comm cid.Cid, index uint64, value commitment.Cell, proof []byte) (bool, error) {
	kzgProof, claimedValue, proofIndex, err := deserializeProof(proof)
	if err != nil {
		return false, err
	}
	if proofIndex != index {
		return false, nil
	}
	expected := cellToKZGScalar(value)
	if claimedValue != expected {
		return false, nil
	}

	commBytes, err := codec.ExtractCommitment(comm)
	if err != nil {
		return false, fmt.Errorf("failed to extract commitment: %w", err)
	}
	var kzgComm gokzg4844.KZGCommitment
	copy(kzgComm[:], commBytes)

	inputPoint := s.domainPoint(index)
	if err := s.context.VerifyKZGProof(kzgComm, inputPoint, claimedValue, kzgProof); err != nil {
		return false, fmt.Errorf("KZG proof verification failed: %w", err)
	}
	return true, nil
}

// BatchVerify currently replays single-index KZG verification because the
// current go-kzg-4844 dependency does not expose batch opening generation.
// TODO: replace this looped verification path once BatchProve emits a real
// KZG multiproof for our index-commitment setting.
func (s *Scheme) BatchVerify(comm cid.Cid, indices []uint64, values []commitment.Cell, proof []byte) (bool, error) {
	if len(indices) == 0 {
		return false, fmt.Errorf("indices must not be empty")
	}
	if len(indices) != len(values) {
		return false, fmt.Errorf("indices/value length mismatch: %d != %d", len(indices), len(values))
	}

	proofs, err := deserializeBatchProof(proof)
	if err != nil {
		return false, err
	}
	if len(proofs) != len(indices) {
		return false, fmt.Errorf("batch proof count mismatch: %d != %d", len(proofs), len(indices))
	}

	for i := range indices {
		ok, err := s.VerifyIndex(comm, indices[i], values[i], proofs[i])
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// VerifyProof verifies a proof carrying its own index metadata.
func (s *Scheme) VerifyProof(comm cid.Cid, value commitment.Cell, proof []byte) (bool, error) {
	_, _, index, err := deserializeProof(proof)
	if err != nil {
		return false, err
	}
	return s.VerifyIndex(comm, index, value, proof)
}

// ReplaceIndex performs an index-stable replacement.
func (s *Scheme) ReplaceIndex(comm cid.Cid, values []commitment.Cell, index uint64, oldValue, newValue commitment.Cell) (cid.Cid, error) {
	entry, err := s.ensureState(comm, values)
	if err != nil {
		return cid.Cid{}, err
	}
	if index >= uint64(len(entry.values)) {
		return cid.Cid{}, fmt.Errorf("index %d out of range", index)
	}
	if !entry.values[index].Equal(oldValue) {
		return cid.Cid{}, fmt.Errorf("old value mismatch at index %d", index)
	}

	nextValues := commitment.CloneCells(entry.values)
	nextValues[index] = commitment.NewCell(newValue)
	return s.commitValues(nextValues)
}

func cellToKZGScalar(value commitment.Cell) gokzg4844.Scalar {
	var scalar gokzg4844.Scalar
	hash := sha256.Sum256(value)

	fieldValue := new(big.Int).SetBytes(hash[:])
	fieldValue.Mod(fieldValue, bls12381ScalarMod)

	result := fieldValue.FillBytes(make([]byte, 32))
	copy(scalar[:], result)

	return scalar
}

// evictLocked removes the oldest half of the cache when capacity is exceeded.
// Must be called with s.mu held.
func (s *Scheme) evictLocked() {
	if len(s.cache) < MaxCacheEntries {
		return
	}
	evictCount := MaxCacheEntries / 2
	for i := 0; i < evictCount && len(s.order) > 0; i++ {
		key := s.order[0]
		s.order = s.order[1:]
		delete(s.cache, key)
	}
}

func (s *Scheme) commitValues(values []commitment.Cell) (cid.Cid, error) {
	if len(values) > MaxValues {
		return cid.Cid{}, fmt.Errorf("too many values: %d > %d", len(values), MaxValues)
	}

	blob := &gokzg4844.Blob{}
	for i, value := range values {
		scalar := cellToKZGScalar(value)
		offset := i * gokzg4844.SerializedScalarSize
		copy(blob[offset:offset+gokzg4844.SerializedScalarSize], scalar[:])
	}

	comm, err := s.context.BlobToKZGCommitment(blob, 1)
	if err != nil {
		return cid.Cid{}, fmt.Errorf("failed to commit: %w", err)
	}

	commBytes := comm[:]
	blobCopy := *blob

	s.mu.Lock()
	s.evictLocked()
	s.cache[string(commBytes)] = &cacheEntry{
		blob:   &blobCopy,
		values: commitment.CloneCells(values),
	}
	s.order = append(s.order, string(commBytes))
	s.mu.Unlock()

	return codec.NewKZGCid(commBytes)
}

func (s *Scheme) ensureState(comm cid.Cid, values []commitment.Cell) (*cacheEntry, error) {
	commBytes, err := codec.ExtractCommitment(comm)
	if err != nil {
		return nil, fmt.Errorf("failed to extract commitment: %w", err)
	}
	commStr := string(commBytes)

	s.mu.RLock()
	entry, ok := s.cache[commStr]
	s.mu.RUnlock()
	if ok {
		return entry, nil
	}
	if values == nil {
		return nil, fmt.Errorf("commitment not found in cache")
	}

	rebuilt, err := s.commitValues(values)
	if err != nil {
		return nil, err
	}
	if !rebuilt.Equals(comm) {
		return nil, fmt.Errorf("reconstructed commitment does not match expected root")
	}

	s.mu.RLock()
	entry, ok = s.cache[commStr]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("commitment not found in cache")
	}
	return entry, nil
}

func (s *Scheme) domainPoint(index uint64) gokzg4844.Scalar {
	return s.domainPoints[index]
}

func serializeProof(proof gokzg4844.KZGProof, claimedValue gokzg4844.Scalar, index uint64) []byte {
	proofBytes := make([]byte, 0, ProofSize)
	proofBytes = append(proofBytes, proof[:]...)
	proofBytes = append(proofBytes, claimedValue[:]...)
	proofBytes = append(proofBytes, byte(index>>24), byte(index>>16), byte(index>>8), byte(index))
	return proofBytes
}

func deserializeProof(data []byte) (gokzg4844.KZGProof, gokzg4844.Scalar, uint64, error) {
	if len(data) < ProofSize {
		return gokzg4844.KZGProof{}, gokzg4844.Scalar{}, 0, fmt.Errorf("proof too short: %d", len(data))
	}
	var proof gokzg4844.KZGProof
	var claimed gokzg4844.Scalar
	copy(proof[:], data[:48])
	copy(claimed[:], data[48:80])
	index := uint64(data[80])<<24 | uint64(data[81])<<16 | uint64(data[82])<<8 | uint64(data[83])
	return proof, claimed, index, nil
}

func serializeBatchProof(proofs [][]byte) []byte {
	buf := make([]byte, 4+len(proofs)*ProofSize)
	binary.BigEndian.PutUint32(buf[:4], uint32(len(proofs)))
	offset := 4
	for _, proof := range proofs {
		copy(buf[offset:offset+ProofSize], proof)
		offset += ProofSize
	}
	return buf
}

func deserializeBatchProof(data []byte) ([][]byte, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("batch proof too short: %d", len(data))
	}

	count := int(binary.BigEndian.Uint32(data[:4]))
	expectedSize := 4 + count*ProofSize
	if len(data) != expectedSize {
		return nil, fmt.Errorf("batch proof has wrong size: expected %d, got %d", expectedSize, len(data))
	}

	proofs := make([][]byte, count)
	offset := 4
	for i := 0; i < count; i++ {
		proofs[i] = append([]byte(nil), data[offset:offset+ProofSize]...)
		offset += ProofSize
	}
	return proofs, nil
}

func buildDomainPoints(size int) ([]gokzg4844.Scalar, error) {
	if bits.OnesCount(uint(size)) != 1 {
		return nil, fmt.Errorf("domain size %d is not a power of two", size)
	}

	var rootOfUnity blsfr.Element
	if _, err := rootOfUnity.SetString("10238227357739495823651030575849232062558860180284477541189508159991286009131"); err != nil {
		return nil, err
	}

	const maxOrderRoot = 32
	logx := bits.TrailingZeros(uint(size))
	if logx > maxOrderRoot {
		return nil, fmt.Errorf("domain size %d exceeds supported root order", size)
	}

	var generator blsfr.Element
	expo := uint64(1 << (maxOrderRoot - logx))
	generator.Exp(rootOfUnity, big.NewInt(int64(expo)))

	roots := make([]blsfr.Element, size)
	current := blsfr.One()
	for i := 0; i < size; i++ {
		roots[i] = current
		current.Mul(&current, &generator)
	}
	bitReverseRoots(roots)

	points := make([]gokzg4844.Scalar, size)
	for i := range roots {
		points[i] = gokzg4844.SerializeScalar(roots[i])
	}
	return points, nil
}

func bitReverseRoots(roots []blsfr.Element) {
	n := len(roots)
	bitLen := bits.Len(uint(n)) - 1
	for i := 0; i < n; i++ {
		j := int(bits.Reverse(uint(i)) >> (bits.UintSize - bitLen))
		if j > i {
			roots[i], roots[j] = roots[j], roots[i]
		}
	}
}

// Ensure Scheme implements commitment.IndexCommitment.
var _ commitment.IndexCommitment = (*Scheme)(nil)
