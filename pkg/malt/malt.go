// Package malt provides the main MALT API.
// MALT (Mutable structure LAyer on Top) is a system-level abstraction
// that provides verifiable, evolvable structures on top of content-addressed storage.
//
// Key concepts:
// - Objects are stored in CAS (e.g., IPFS/IPLD), not in MALT
// - MALT stores explicit arcs with cryptographic commitments
// - MALT provides resolution and proof services for structural relationships
// - MALT complements CAS, not replaces it
package malt

import (
	"fmt"
	"sync"

	"github.com/dewebprotocol/malt/pkg/commitment"
	"github.com/dewebprotocol/malt/pkg/commitment/mock"
	"github.com/dewebprotocol/malt/pkg/resolver"
	"github.com/dewebprotocol/malt/pkg/storage"
	"github.com/dewebprotocol/malt/pkg/storage/memory"
	"github.com/dewebprotocol/malt/pkg/types"
)

// MALT is the main API facade for the MALT system.
// It provides a high-level interface for managing structural relationships
// (explicit arcs) with cryptographic commitments.
type MALT struct {
	// ear is the Explicit Arc Resolver
	ear *resolver.EAR

	// sce is the Structure Commitment Engine
	sce commitment.CommitmentScheme

	// eat is the Explicit Arc Table storage
	eat storage.LineageStore

	// config holds the MALT configuration
	config *Config

	// structures stores structure data indexed by commitment
	// Key: commitment string, Value: StructureData
	structures map[string]*StructureData

	// mu protects concurrent access
	mu sync.RWMutex
}

// StructureData stores structure-related data.
type StructureData struct {
	// Commitment is the structure commitment
	Commitment commitment.Commitment

	// ArcSet is the set of explicit arcs
	ArcSet *types.ArcSet

	// SourceCID is the source object's CID (optional, for reference)
	SourceCID types.CID
}

// Config holds MALT configuration.
type Config struct {
	// CommitmentType specifies the commitment scheme type
	CommitmentType commitment.SchemeType

	// StorageType specifies the storage backend type
	StorageType string

	// VectorSize is the maximum number of arcs per structure
	VectorSize int

	// EnableVersioning enables versioned resolution
	EnableVersioning bool
}

// DefaultConfig returns the default MALT configuration.
func DefaultConfig() *Config {
	return &Config{
		CommitmentType:   commitment.SchemeTypeMock,
		StorageType:      "memory",
		VectorSize:       256,
		EnableVersioning: true,
	}
}

// New creates a new MALT instance with default configuration.
func New() (*MALT, error) {
	return NewWithConfig(DefaultConfig())
}

// NewWithConfig creates a new MALT instance with the given configuration.
func NewWithConfig(cfg *Config) (*MALT, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	// Initialize storage
	var eat storage.LineageStore
	switch cfg.StorageType {
	case "memory":
		eat = memory.New()
	default:
		return nil, fmt.Errorf("unsupported storage type: %s", cfg.StorageType)
	}

	// Initialize commitment scheme
	var sce commitment.CommitmentScheme
	switch cfg.CommitmentType {
	case commitment.SchemeTypeMock:
		sce = mock.NewMockCommitment()
	case commitment.SchemeTypeIPA:
		return nil, fmt.Errorf("IPA commitment not yet implemented, use mock")
	default:
		return nil, fmt.Errorf("unsupported commitment type: %s", cfg.CommitmentType)
	}

	// Initialize resolver (CAS is optional for structure layer)
	ear, err := resolver.New(sce, eat, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create resolver: %w", err)
	}

	return &MALT{
		ear:       ear,
		sce:       sce,
		eat:       eat,
		config:    cfg,
		structures: make(map[string]*StructureData),
	}, nil
}

// NewWithComponents creates a new MALT instance with custom components.
// This allows for full customization of the underlying implementations.
func NewWithComponents(sce commitment.CommitmentScheme, eat storage.LineageStore) (*MALT, error) {
	ear, err := resolver.New(sce, eat, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create resolver: %w", err)
	}

	return &MALT{
		ear:        ear,
		sce:        sce,
		eat:        eat,
		config:     DefaultConfig(),
		structures: make(map[string]*StructureData),
	}, nil
}

// CreateStructure creates a new structure with the given arc set.
// Returns the commitment that can be used to resolve arcs.
// Note: Objects (targets) are stored separately in CAS.
func (m *MALT) CreateStructure(arcs *types.ArcSet) (commitment.Commitment, error) {
	if arcs == nil {
		arcs = types.NewArcSet()
	}

	// Create structure commitment
	comm, err := m.sce.Commit(arcs)
	if err != nil {
		return nil, fmt.Errorf("failed to create commitment: %w", err)
	}

	// Store arc entries in EAT
	for _, pair := range arcs.Pairs() {
		if err := m.eat.Put(comm, pair.Path, pair.Target); err != nil {
			return nil, fmt.Errorf("failed to store arc: %w", err)
		}
	}

	// Store structure data
	m.mu.Lock()
	m.structures[comm.String()] = &StructureData{
		Commitment: comm,
		ArcSet:     arcs.Clone(),
	}
	m.mu.Unlock()

	return comm, nil
}

// CreateStructureForSource creates a structure for a specific source object.
// This links the structure to a source CID for reference.
func (m *MALT) CreateStructureForSource(sourceCID types.CID, arcs *types.ArcSet) (commitment.Commitment, error) {
	comm, err := m.CreateStructure(arcs)
	if err != nil {
		return nil, err
	}

	// Update structure data with source CID
	m.mu.Lock()
	if data, ok := m.structures[comm.String()]; ok {
		data.SourceCID = sourceCID
	}
	m.mu.Unlock()

	return comm, nil
}

// Resolve resolves a path from a structure commitment.
// Returns the target CID and a proof that can be verified.
// This implements the resolution procedure from Section 4.3.
func (m *MALT) Resolve(comm commitment.Commitment, p types.Path) (types.CID, commitment.Proof, error) {
	m.mu.RLock()
	data, ok := m.structures[comm.String()]
	m.mu.RUnlock()

	if !ok {
		return types.CID{}, nil, fmt.Errorf("structure not found for commitment: %s", comm)
	}

	return m.ear.ResolveSimple(comm, p, data.ArcSet)
}

// ResolveWithArcSet resolves a path using an externally provided arc set.
// This is useful when the arc set is not stored in MALT.
func (m *MALT) ResolveWithArcSet(comm commitment.Commitment, p types.Path, arcs *types.ArcSet) (types.CID, commitment.Proof, error) {
	return m.ear.ResolveSimple(comm, p, arcs)
}

// Verify verifies a proof for a given arc.
// This can be done without accessing MALT's internal state.
func (m *MALT) Verify(comm commitment.Commitment, p types.Path, c types.CID, proof commitment.Proof) (bool, error) {
	return m.ear.Verify(comm, p, c, proof)
}

// UpdateArc updates an arc in a structure.
// Returns the new commitment after the update.
// This implements localized structural updates without ancestor propagation.
func (m *MALT) UpdateArc(comm commitment.Commitment, p types.Path, newCID types.CID) (commitment.Commitment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, ok := m.structures[comm.String()]
	if !ok {
		return nil, fmt.Errorf("structure not found for commitment: %s", comm)
	}

	// Perform update (this also updates data.ArcSet)
	result, err := m.ear.Update(comm, p, newCID, data.ArcSet)
	if err != nil {
		return nil, fmt.Errorf("update failed: %w", err)
	}

	// Store new structure data with updated arc set
	m.structures[result.NewCommitment.String()] = &StructureData{
		Commitment: result.NewCommitment,
		ArcSet:     data.ArcSet,
		SourceCID:  data.SourceCID,
	}

	return result.NewCommitment, nil
}

// AddArc adds a new arc to a structure.
// Returns the new commitment after the addition.
func (m *MALT) AddArc(comm commitment.Commitment, p types.Path, target types.CID) (commitment.Commitment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, ok := m.structures[comm.String()]
	if !ok {
		return nil, fmt.Errorf("structure not found for commitment: %s", comm)
	}

	newComm, err := m.ear.AddArc(comm, p, target, data.ArcSet)
	if err != nil {
		return nil, fmt.Errorf("failed to add arc: %w", err)
	}

	// Store new structure data
	m.structures[newComm.String()] = &StructureData{
		Commitment: newComm,
		ArcSet:     data.ArcSet,
		SourceCID:  data.SourceCID,
	}

	return newComm, nil
}

// RemoveArc removes an arc from a structure.
// Returns the new commitment after the removal.
func (m *MALT) RemoveArc(comm commitment.Commitment, p types.Path) (commitment.Commitment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, ok := m.structures[comm.String()]
	if !ok {
		return nil, fmt.Errorf("structure not found for commitment: %s", comm)
	}

	newComm, err := m.ear.RemoveArc(comm, p, data.ArcSet)
	if err != nil {
		return nil, fmt.Errorf("failed to remove arc: %w", err)
	}

	// Store new structure data
	m.structures[newComm.String()] = &StructureData{
		Commitment: newComm,
		ArcSet:     data.ArcSet,
		SourceCID:  data.SourceCID,
	}

	return newComm, nil
}

// GetStructure retrieves the structure data for a commitment.
func (m *MALT) GetStructure(comm commitment.Commitment) (*StructureData, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	data, ok := m.structures[comm.String()]
	if !ok {
		return nil, fmt.Errorf("structure not found for commitment: %s", comm)
	}

	return data, nil
}

// GetArcSet retrieves the arc set for a commitment.
func (m *MALT) GetArcSet(comm commitment.Commitment) (*types.ArcSet, error) {
	data, err := m.GetStructure(comm)
	if err != nil {
		return nil, err
	}
	return data.ArcSet, nil
}

// GetLineage returns the commitment lineage for a structure.
func (m *MALT) GetLineage(comm commitment.Commitment) ([]commitment.Commitment, error) {
	return m.ear.GetLineage(comm)
}

// GetLatestCommitment returns the latest commitment in a lineage.
func (m *MALT) GetLatestCommitment(rootComm commitment.Commitment) (commitment.Commitment, error) {
	return m.ear.GetLatestCommitment(rootComm)
}

// CommitDirectly creates a commitment for an arc set without storing it.
// This is useful for creating commitments for external verification.
func (m *MALT) CommitDirectly(arcs *types.ArcSet) (commitment.Commitment, error) {
	return m.sce.Commit(arcs)
}

// ProveDirectly generates a proof for an arc without using stored structures.
func (m *MALT) ProveDirectly(comm commitment.Commitment, arcs *types.ArcSet, p types.Path) (types.CID, commitment.Proof, error) {
	return m.sce.Prove(comm, arcs, p)
}

// VerifyDirectly verifies a proof directly using the commitment scheme.
func (m *MALT) VerifyDirectly(comm commitment.Commitment, p types.Path, c types.CID, proof commitment.Proof) (bool, error) {
	return m.sce.Verify(comm, p, c, proof)
}

// Close closes the MALT instance and releases resources.
func (m *MALT) Close() error {
	return m.eat.Close()
}

// Stats returns statistics about the MALT instance.
func (m *MALT) Stats() Stats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return Stats{
		StructureCount: len(m.structures),
	}
}

// Stats contains MALT statistics.
type Stats struct {
	StructureCount int
}

// Example demonstrates basic MALT usage.
func Example() {
	// Create MALT instance (structure layer)
	m, err := New()
	if err != nil {
		panic(err)
	}
	defer m.Close()

	// Create target CIDs (these would come from CAS/IPFS in practice)
	target1CID, _ := types.NewCID([]byte("target1"))
	target2CID, _ := types.NewCID([]byte("target2"))

	// Create a structure with explicit arcs
	arcs := types.NewArcSetFromPairs(
		types.NewArcPair("link1", target1CID),
		types.NewArcPair("link2", target2CID),
	)

	// Create structure and get commitment
	comm, err := m.CreateStructure(arcs)
	if err != nil {
		panic(err)
	}

	// Resolve an arc
	resolvedCID, proof, err := m.Resolve(comm, "link1")
	if err != nil {
		panic(err)
	}

	// Verify the proof
	valid, err := m.Verify(comm, "link1", resolvedCID, proof)
	if err != nil {
		panic(err)
	}

	// Update an arc (localized update)
	newTargetCID, _ := types.NewCID([]byte("new_target"))
	newComm, err := m.UpdateArc(comm, "link1", newTargetCID)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Initial commitment: %s\n", comm)
	fmt.Printf("Resolved: %s\n", resolvedCID)
	fmt.Printf("Proof valid: %v\n", valid)
	fmt.Printf("New commitment after update: %s\n", newComm)
}