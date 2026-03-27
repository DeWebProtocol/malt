// Package mock provides a mock implementation of the commitment scheme
// for testing purposes. It uses simple hashing and is NOT cryptographically secure.
package mock

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/dewebprotocol/malt/pkg/commitment"
	"github.com/dewebprotocol/malt/pkg/types"
)

// MockCommitment is a mock implementation of CommitmentScheme for testing.
// It uses SHA-256 hashing and is NOT suitable for production use.
// This implementation is designed to:
// 1. Be deterministic for reproducible tests
// 2. Support local updates (by storing the arc set internally)
// 3. Generate verifiable proofs
type MockCommitment struct {
	// store maps commitments to their arc sets for proof generation
	store map[string]*types.ArcSet
}

// NewMockCommitment creates a new mock commitment scheme.
func NewMockCommitment() *MockCommitment {
	return &MockCommitment{
		store: make(map[string]*types.ArcSet),
	}
}

// Commit generates a mock commitment for an arc set.
// The commitment is computed as SHA-256(sorted(pairs)).
func (m *MockCommitment) Commit(arcs *types.ArcSet) (commitment.Commitment, error) {
	if arcs == nil {
		return nil, fmt.Errorf("arc set is nil")
	}

	// Compute commitment as hash of sorted pairs
	data := m.serializeArcSet(arcs)
	hash := sha256.Sum256(data)
	comm := commitment.Commitment(hash[:])

	// Store the arc set for later proof generation
	m.store[comm.String()] = arcs.Clone()

	return comm, nil
}

// Prove generates a mock proof for an arc.
// The proof contains the path and target, verifiable against the commitment.
func (m *MockCommitment) Prove(comm commitment.Commitment, arcs *types.ArcSet, p types.Path) (types.CID, commitment.Proof, error) {
	if arcs == nil {
		return types.CID{}, nil, fmt.Errorf("arc set is nil")
	}

	// Look up the target for the path
	target, ok := arcs.Get(p)
	if !ok {
		return types.CID{}, nil, fmt.Errorf("path %s not found in arc set", p)
	}

	// Generate proof (serialize path and target)
	proofData, err := json.Marshal(struct {
		Path   string
		Target string
	}{
		Path:   string(p),
		Target: target.String(),
	})
	if err != nil {
		return types.CID{}, nil, fmt.Errorf("failed to marshal proof: %w", err)
	}

	return target, commitment.Proof(proofData), nil
}

// Verify verifies a mock proof.
func (m *MockCommitment) Verify(comm commitment.Commitment, p types.Path, c types.CID, proof commitment.Proof) (bool, error) {
	// Parse the proof
	var proofData struct {
		Path   string
		Target string
	}
	if err := json.Unmarshal(proof, &proofData); err != nil {
		return false, fmt.Errorf("failed to unmarshal proof: %w", err)
	}

	// Check if the proof matches the claimed path and target
	if proofData.Path != string(p) {
		return false, nil
	}

	// Parse the target from proof
	targetCID, err := types.ParseCID(proofData.Target)
	if err != nil {
		return false, fmt.Errorf("invalid target CID in proof: %w", err)
	}

	// Check if target matches
	if !targetCID.Equals(c) {
		return false, nil
	}

	// Look up the stored arc set
	storedArcs, ok := m.store[comm.String()]
	if !ok {
		return false, fmt.Errorf("commitment not found in store")
	}

	// Verify the arc is in the stored set
	storedTarget, ok := storedArcs.Get(p)
	if !ok {
		return false, nil
	}

	return storedTarget.Equals(c), nil
}

// Update updates a mock commitment.
// For the mock, we store the updated arc set under a new commitment.
func (m *MockCommitment) Update(comm commitment.Commitment, p types.Path, oldCID, newCID types.CID) (commitment.Commitment, error) {
	// Get the stored arc set
	storedArcs, ok := m.store[comm.String()]
	if !ok {
		return nil, fmt.Errorf("commitment not found in store")
	}

	// Verify the old CID matches
	currentCID, ok := storedArcs.Get(p)
	if !ok {
		return nil, fmt.Errorf("path %s not found in arc set", p)
	}
	if !currentCID.Equals(oldCID) {
		return nil, fmt.Errorf("old CID mismatch: expected %s, got %s", oldCID, currentCID)
	}

	// Create updated arc set
	newArcs := storedArcs.Clone()
	newArcs.Add(p, newCID)

	// Compute new commitment
	return m.Commit(newArcs)
}

// BatchUpdate updates multiple arcs in a single operation.
func (m *MockCommitment) BatchUpdate(comm commitment.Commitment, updates map[types.Path]struct {
	Old types.CID
	New types.CID
}) (commitment.Commitment, error) {
	// Get the stored arc set
	storedArcs, ok := m.store[comm.String()]
	if !ok {
		return nil, fmt.Errorf("commitment not found in store")
	}

	// Create updated arc set
	newArcs := storedArcs.Clone()

	// Apply all updates
	for p, update := range updates {
		currentCID, ok := newArcs.Get(p)
		if !ok {
			return nil, fmt.Errorf("path %s not found in arc set", p)
		}
		if !currentCID.Equals(update.Old) {
			return nil, fmt.Errorf("old CID mismatch for path %s: expected %s, got %s", p, update.Old, currentCID)
		}
		newArcs.Add(p, update.New)
	}

	// Compute new commitment
	return m.Commit(newArcs)
}

// serializeArcSet serializes an arc set deterministically.
func (m *MockCommitment) serializeArcSet(arcs *types.ArcSet) []byte {
	pairs := arcs.Pairs()

	// Sort pairs by path (already sorted by Pairs())
	var data []byte
	for _, pair := range pairs {
		// Add path length and path
		pathBytes := []byte(pair.Path)
		data = append(data, encodeUint32(uint32(len(pathBytes)))...)
		data = append(data, pathBytes...)

		// Add target CID
		targetBytes := pair.Target.Bytes()
		data = append(data, encodeUint32(uint32(len(targetBytes)))...)
		data = append(data, targetBytes...)
	}

	return data
}

// encodeUint32 encodes a uint32 to 4 bytes (big endian).
func encodeUint32(n uint32) []byte {
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, n)
	return buf
}

// GetArcSet retrieves the arc set for a commitment (for testing).
func (m *MockCommitment) GetArcSet(comm commitment.Commitment) (*types.ArcSet, bool) {
	arcs, ok := m.store[comm.String()]
	if !ok {
		return nil, false
	}
	return arcs.Clone(), true
}

// Clear clears all stored commitments (for testing).
func (m *MockCommitment) Clear() {
	m.store = make(map[string]*types.ArcSet)
}

// Size returns the number of stored commitments (for testing).
func (m *MockCommitment) Size() int {
	return len(m.store)
}

// Ensure MockCommitment implements CommitmentScheme
var _ commitment.CommitmentScheme = (*MockCommitment)(nil)

// ProveWithCommitment generates a proof using the stored arc set.
// This is useful when you have the commitment but not the arc set.
func (m *MockCommitment) ProveWithCommitment(comm commitment.Commitment, p types.Path) (types.CID, commitment.Proof, error) {
	storedArcs, ok := m.store[comm.String()]
	if !ok {
		return types.CID{}, nil, fmt.Errorf("commitment not found in store")
	}
	return m.Prove(comm, storedArcs, p)
}

// CommitWithProof generates a commitment and a proof for a specific path in one operation.
func (m *MockCommitment) CommitWithProof(arcs *types.ArcSet, p types.Path) (commitment.Commitment, types.CID, commitment.Proof, error) {
	comm, err := m.Commit(arcs)
	if err != nil {
		return nil, types.CID{}, nil, err
	}

	target, proof, err := m.Prove(comm, arcs, p)
	if err != nil {
		return nil, types.CID{}, nil, err
	}

	return comm, target, proof, nil
}

// PrintStore prints all stored commitments and their arc sets (for debugging).
func (m *MockCommitment) PrintStore() {
	// Sort commitments for deterministic output
	var commitments []string
	for comm := range m.store {
		commitments = append(commitments, comm)
	}
	sort.Strings(commitments)

	for _, comm := range commitments {
		arcs := m.store[comm]
		fmt.Printf("Commitment: %s\n", comm)
		for _, pair := range arcs.Pairs() {
			fmt.Printf("  %s -> %s\n", pair.Path, pair.Target)
		}
	}
}