// Package interfaces defines the core interfaces for MALT's layered architecture.
//
// Architecture Overview:
//
// The MALT architecture is organized into three layers:
//
// 1. Storage Layer (injectable backends):
//   - KVStore: low-level key-value storage (already defined in kvstore package)
//   - ContentStore: content-addressable storage for data blocks (CID → data)
//   - ArcStore: explicit arc storage (root, path → CID)
//
// 2. Commitment Layer (cryptographic operations):
//   - CommitmentBackend: structure commitment schemes (KZG, Verkle, IPA, Merkle)
//
// 3. Graph Layer (stateless core):
//   - Graph: resolution, update, and verification operations
//   - Deployment: factory for composing all components
//
// Design Principles:
//
// - Stateless Graph: root is always passed as parameter, no internal state
// - Storage Injection: all storage backends are injected by Deployment
// - Batch Operations: BatchResolve returns AggregatedProof, BatchUpdate supports bulk changes
// - Proof with Resolve: Remove standalone Prove(), proof returned with Resolve
//
// Dependency Injection:
//
// Deployment acts as the composition root:
//
//   deployment := NewMemoryDeployment(kv, commitmentBackend)
//   graph := deployment.CreateGraph()
//
// Storage Hierarchy:
//
// KVStore (interface, injected by upper layer)
//   ↓
// ContentStore implementations:
//   - CASContentStore (wraps CAS)
//   - KVStoreContentStore (CID as key)
//
// ArcStore implementations:
//   - EATArcStore (refactored from EAT)
//
// Migration Notes:
//
// Phase 1 (Interface Definition): Define all interfaces in this package
// Phase 2 (Implementation Migration): Refactor existing code to implement interfaces
// Phase 3 (Graph Core): Implement new stateless Graph
// Phase 4 (Deployment): Create Deployment factory implementations
// Phase 5-7: Tests, CLI/Gateway update, cleanup
package interfaces