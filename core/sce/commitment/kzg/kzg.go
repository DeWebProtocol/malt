// Package kzg provides a KZG polynomial commitment implementation.
package kzg

import (
	"crypto/sha256"
	"fmt"
	"math/big"
	"math/bits"
	"sync"

	blsfr "github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	gokzg4844 "github.com/crate-crypto/go-kzg-4844"
	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/sce/commitment"
	"github.com/dewebprotocol/malt/core/types/arcset"
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

// Scheme implements a primitive indexed commitment backend using KZG
// polynomial commitments. The path-oriented Scheme methods are retained as
// wrappers over the primitive indexed operations.
type Scheme struct {
	context      *gokzg4844.Context
	domainPoints []gokzg4844.Scalar

	mu    sync.RWMutex
	cache map[string]*cacheEntry
	order []string // tracks insertion order for LRU eviction
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

// CommitValues commits a stable indexed value vector.
func (s *Scheme) CommitValues(values []cid.Cid) (cid.Cid, error) {
	return s.commitValues(nil, values)
}

// Commit generates a KZG commitment for the given arc set.
func (s *Scheme) Commit(arcs arcset.ArcSet) (cid.Cid, error) {
	paths, values := commitment.ExtractSortedPathsValues(arcs)
	return s.commitValues(paths, values)
}

// Prove generates a KZG proof for a value at the given path.
func (s *Scheme) Prove(comm cid.Cid, arcs arcset.ArcSet, path string) (cid.Cid, []byte, error) {
	var (
		paths  []string
		values []cid.Cid
	)
	if arcs != nil {
		paths, values = commitment.ExtractSortedPathsValues(arcs)
	}
	entry, err := s.ensureState(comm, paths, values)
	if err != nil {
		return cid.Cid{}, nil, err
	}

	proveIndex, ok := commitment.FindPathIndex(entry.paths, path)
	if !ok {
		return cid.Cid{}, nil, fmt.Errorf("path %s not found", path)
	}
	target, proof, err := s.ProveIndex(comm, values, uint64(proveIndex))
	if err != nil {
		return cid.Cid{}, nil, err
	}
	return target, commitment.WrapPathProof(path, proof), nil
}

// Verify verifies a KZG proof.
func (s *Scheme) Verify(comm cid.Cid, path string, value cid.Cid, proof []byte) (bool, error) {
	primitiveProof, err := commitment.UnwrapPathProof(path, proof)
	if err != nil {
		return false, err
	}
	_, _, index, err := deserializeProof(primitiveProof)
	if err != nil {
		return false, err
	}
	return s.VerifyIndex(comm, index, value, primitiveProof)
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
	entry, err := s.ensureState(comm, paths, values)
	if err != nil {
		return cid.Cid{}, err
	}

	updateIndex, ok := commitment.FindPathIndex(entry.paths, path)
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
	entry, err := s.ensureState(comm, paths, values)
	if err != nil {
		return cid.Cid{}, err
	}
	nextValues := append([]cid.Cid(nil), entry.values...)
	for path, update := range updates {
		index, ok := commitment.FindPathIndex(entry.paths, path)
		if !ok {
			return cid.Cid{}, fmt.Errorf("path %s not found", path)
		}
		if !nextValues[index].Equals(update.Old) {
			return cid.Cid{}, fmt.Errorf("old value mismatch for path %s", path)
		}
		nextValues[index] = update.New
	}
	return s.commitValues(entry.paths, nextValues)
}

// BatchProve generates proofs for multiple paths.
func (s *Scheme) BatchProve(comm cid.Cid, arcs arcset.ArcSet, paths []string) (map[string]arcset.BatchProofEntry, error) {
	return commitment.BatchProve(paths, func(path string) (cid.Cid, []byte, error) {
		return s.Prove(comm, arcs, path)
	})
}

// BatchVerify verifies multiple proofs.
func (s *Scheme) BatchVerify(comm cid.Cid, proofs map[string]arcset.BatchProofEntry) (bool, error) {
	return commitment.BatchVerify(proofs, func(path string, value cid.Cid, proof []byte) (bool, error) {
		return s.Verify(comm, path, value, proof)
	})
}

// AggregateProve generates an aggregated proof.
func (s *Scheme) AggregateProve(comm cid.Cid, arcs arcset.ArcSet, paths []string) (*arcset.AggregatedProof, error) {
	return commitment.AggregateProve(paths, func(path string) (cid.Cid, []byte, error) {
		return s.Prove(comm, arcs, path)
	})
}

// AggregateVerify verifies an aggregated proof.
func (s *Scheme) AggregateVerify(comm cid.Cid, aggProof *arcset.AggregatedProof) (bool, error) {
	return commitment.AggregateVerify(aggProof, func(path string, value cid.Cid, proof []byte) (bool, error) {
		return s.Verify(comm, path, value, proof)
	})
}

// ProveIndex proves the value at a stable index.
func (s *Scheme) ProveIndex(comm cid.Cid, values []cid.Cid, index uint64) (cid.Cid, []byte, error) {
	entry, err := s.ensureState(comm, nil, values)
	if err != nil {
		return cid.Cid{}, nil, err
	}
	if index >= uint64(len(entry.values)) {
		return cid.Cid{}, nil, fmt.Errorf("index %d out of range", index)
	}

	inputPoint := s.domainPoint(index)
	proof, claimedValue, err := s.context.ComputeKZGProof(entry.blob, inputPoint, 1)
	if err != nil {
		return cid.Cid{}, nil, fmt.Errorf("failed to compute proof: %w", err)
	}
	return entry.values[index], serializeProof(proof, claimedValue, index), nil
}

// VerifyIndex verifies a proof for a stable index without cache state.
func (s *Scheme) VerifyIndex(comm cid.Cid, index uint64, value cid.Cid, proof []byte) (bool, error) {
	kzgProof, claimedValue, proofIndex, err := deserializeProof(proof)
	if err != nil {
		return false, err
	}
	if proofIndex != index {
		return false, nil
	}
	expected := cidToKZGScalar(value)
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

// ReplaceIndex performs an index-stable replacement.
func (s *Scheme) ReplaceIndex(comm cid.Cid, values []cid.Cid, index uint64, oldValue, newValue cid.Cid) (cid.Cid, error) {
	entry, err := s.ensureState(comm, nil, values)
	if err != nil {
		return cid.Cid{}, err
	}
	if index >= uint64(len(entry.values)) {
		return cid.Cid{}, fmt.Errorf("index %d out of range", index)
	}
	if !entry.values[index].Equals(oldValue) {
		return cid.Cid{}, fmt.Errorf("old value mismatch at index %d", index)
	}

	nextValues := append([]cid.Cid(nil), entry.values...)
	nextValues[index] = newValue
	return s.commitValues(entry.paths, nextValues)
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

// evictLocked removes the oldest half of the cache when capacity is exceeded.
// Must be called with s.mu held.
func (s *Scheme) evictLocked() {
	if len(s.cache) < MaxCacheEntries {
		return
	}
	// Evict oldest half
	evictCount := MaxCacheEntries / 2
	for i := 0; i < evictCount && len(s.order) > 0; i++ {
		key := s.order[0]
		s.order = s.order[1:]
		delete(s.cache, key)
	}
}

func (s *Scheme) commitValues(paths []string, values []cid.Cid) (cid.Cid, error) {
	if len(values) > MaxValues {
		return cid.Cid{}, fmt.Errorf("too many values: %d > %d", len(values), MaxValues)
	}

	blob := &gokzg4844.Blob{}
	for i, value := range values {
		scalar := cidToKZGScalar(value)
		offset := i * gokzg4844.SerializedScalarSize
		copy(blob[offset:offset+gokzg4844.SerializedScalarSize], scalar[:])
	}

	comm, err := s.context.BlobToKZGCommitment(blob, 1)
	if err != nil {
		return cid.Cid{}, fmt.Errorf("failed to commit: %w", err)
	}

	commBytes := comm[:]
	clonedValues := append([]cid.Cid(nil), values...)
	clonedPaths := append([]string(nil), paths...)
	blobCopy := *blob

	s.mu.Lock()
	s.evictLocked()
	s.cache[string(commBytes)] = &cacheEntry{
		blob:   &blobCopy,
		paths:  clonedPaths,
		values: clonedValues,
	}
	s.order = append(s.order, string(commBytes))
	s.mu.Unlock()

	return codec.NewKZGCid(commBytes)
}

func (s *Scheme) ensureState(comm cid.Cid, paths []string, values []cid.Cid) (*cacheEntry, error) {
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

	rebuilt, err := s.commitValues(paths, values)
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

// Ensure Scheme implements commitment.Scheme.
var _ commitment.ListBackend = (*Scheme)(nil)
var _ commitment.Scheme = (*Scheme)(nil)
