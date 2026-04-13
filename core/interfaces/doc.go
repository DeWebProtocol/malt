// Package interfaces defines core MALT interfaces.
//
// # MALT Architecture Overview
//
// MALT (Mutable Abstraction Layer on Trie) provides a compatibility-preserving
// structure layer for content-addressed storage. The architecture is organized
// into three layers:
//
// ## 1. Storage Layer
//
// Storage backends are injectable interfaces:
//
//   - ContentStore: content-addressable storage (CID → data)
//   - ArcStore: explicit arc storage (root, path → CID)
//   - (KVStore is the underlying interface, defined in kvstore package)
//
// ## 2. Commitment Layer
//
// CommitmentBackend provides cryptographic operations:
//
//   - Commit: generate commitment from arc set
//   - Prove/Verify: single-path proofs
//   - BatchProve/BatchVerify: multi-path proofs
//   - AggregateProve/AggregateVerify: aggregated proofs
//
// ## 3. Graph Layer
//
// Graph is the stateless core abstraction:
//
//   - Resolve(root, path) → (target, proof)
//   - BatchResolve(root, paths) → (targets, aggregatedProof)
//   - Update(root, arcs) → (newRoot, delta)
//   - Snapshot(root) → arc set view
//   - Commit(view) → root CID
//
// ## Deployment
//
// Deployment is the composition factory:
//
//   deployment := NewMemoryDeployment(kvStore)
//   graph := deployment.CreateGraph()
//   root := deployment.InitializeGraph(ctx)
//
// ## Design Principles
//
// 1. Stateless Graph: root always passed as parameter
// 2. Storage injection: backends injected by Deployment
// 3. Rewrite amplification = 1.0: localized updates
// 4. Batch operations: efficient bulk operations
package interfaces