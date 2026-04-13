# MALT Architecture

## Overview

MALT (Mutable Abstraction Layer over Trie) provides a compatibility-preserving structure layer for content-addressed storage (CAS). It replaces Merkle DAG implicit arcs with explicitly committed arcs, turning ancestor-dependent structural rewrite into localized, verifiable structure maintenance.

---

## Directory Structure

```
malt/
├── cmd/
│   ├── gateway/main.go          # HTTP Gateway entry point (REST API)
│   └── malt/                    # CLI tool (resolve, prove, verify, graph...)
├── config/                      # Config loading (Viper: file/env/flags)
├── logger/                      # Structured logging
├── core/                        # Core library
│   ├── api/                     # Application-level Node entry
│   ├── cas/                     # Content-addressable storage
│   ├── codec/                   # CID codec detection
│   ├── commitment/              # CommitmentBackend adapter
│   ├── deployment/              # Deployment factory (composition root)
│   ├── eat/                     # Explicit Arc Table
│   ├── graph/                   # Graph (resolver + writer composition)
│   ├── interfaces/              # Interface definitions
│   ├── kvstore/                 # KV storage (memory, badger, fs)
│   ├── lineage/                 # Version lineage tracking
│   ├── replication/             # Graph snapshot export/import/sync
│   ├── resolver/                # Hybrid resolver
│   ├── sce/                     # Structure Commitment Engine
│   ├── store/                   # ArcStore + ContentStore adapters
│   ├── types/                   # Type definitions (arcset, evidence)
│   └── writer/                  # Write-side API
├── gateway/                     # HTTP gateway implementation
└── integration/                 # Integration tests
```

---

## Core Components

### Layer 1: Interfaces (`core/interfaces/`)

Four core interfaces define the MALT architecture:

| Interface | Responsibility |
|-----------|---------------|
| `CommitmentBackend` | Cryptographic commitments (Commit/Prove/Verify/Update/BatchProve) |
| `ArcStore` | Arc persistence at KVStore level (path → target mapping) |
| `ContentStore` | Content-addressable storage (Get/Put/Has/Batch by CID) |
| `Graph` | Full graph = `GraphResolver` (read) + `GraphWriter` (write) |
| `Deployment` | Composition root — wires dependencies, creates Graph |

### Layer 2: Cryptographic Layer (`core/sce/` + `core/commitment/`)

```
Commitment.Scheme (pure cryptography)
├── kzg.NewScheme()       → Polynomial commitment (malt-kzg codec)
├── verkle.NewScheme()    → Verkle tree (malt-verkle codec)
└── ipa.NewScheme()       → Inner product argument (malt-ipa codec)

SCE.Engine (Structure Commitment Engine)
├── Manages session cache (commitment bytes → arc paths/values)
├── Wraps Scheme + arc set management
└── Provides Commit/Prove/Verify/Update/BatchUpdate/AggregateProve
```

### Layer 3: Data Layer (`core/eat/` + `core/kvstore/` + `core/cas/`)

```
EAT (Explicit Arc Table)
├── overwrite.EAT  → KVStore-backed, direct overwrite storage
└── versioned.EAT  → Versioned storage via @previous chain

KVStore
├── memory.KV    → In-memory (testing)
├── badger.KV    → BadgerDB (persistent)
└── fs.KV        → Filesystem (persistent)

CAS (Content-Addressable Storage)
├── mock.CAS     → KVStore-backed, simulates IPFS latency
│                 Defaults: Get=2.1s, Put=1.4s, Has=100ms (ProbeLab)
│                 Override via --mock-latency flag
└── ipfs.Client  → Local IPFS daemon API (POST /api/v0/block/*)
```

### Layer 4: Resolution Layer (`core/resolver/`)

```
step.Step (single-step resolution interface)
├── explicit.Resolver  → MALT explicit arcs, longest-prefix EAT match + SCE proof
├── implicit.Resolver  → Merkle-DAG implicit arcs via CAS + IPLD codecs
│                       Auto-detects: UnixFS / HAMT / Plain DAG
└── hamt.Resolver      → HAMT-specific resolution

resolver.Resolver (hybrid resolver)
├── Dispatches to explicitStep or implicitStep based on CID codec
├── Loop-consumes path segments until exhausted
├── Returns ResolveResult { Target, Transcript }
└── Transcript contains StepEvidence{Path, Target, Evidence} for each step
```

### Layer 5: Graph Layer (`core/graph/`)

```
graph.Graph = composition of:
    resolver: interfaces.GraphResolver  (ReadAdapter wrapping resolver.Resolver)
    writer:   interfaces.GraphWriter    (WriteAdapter wrapping SCE + EAT)

Manager  → Graph lifecycle (create/query/freeze/delete)
Store    → Graph metadata persistence (graph/meta/, graph/index/)
```

**Design decision**: Graph no longer holds SCE/EAT/CAS directly. It routes to resolver and writer, which own those dependencies.

### Layer 6: Write API (`core/writer/`)

```
writer.Writer
├── UpdateArc(ctx, bucketId, root, path, newTarget)
│    → Insert/replace/delete a single arc
├── BatchUpdateArcs(ctx, bucketId, root, updates)
└── CreateStructure(ctx, bucketId, arcs)
    → Generate new commitment + write to EAT + record lineage
```

---

## Entry Points

### A. HTTP Gateway Startup

```
cmd/gateway/main.go
  → config.Init() + flag parsing (--ipfs-api, --mock-latency, --listen)
  → api.NewNode(opts...)
      ├── initKVStore()             # memory / badger
      ├── initCommitmentScheme()    # kzg / verkle / ipa
      ├── initEAT()                 # overwrite / versioned
      ├── initCAS()                 # mock / ipfs (with mock-latency)
      └── Build explicitStep, implicitStep, resolver
  → gateway.NewNodeAdapter(node)
  → gateway.NewServer(adapter, ":8080")
  → srv.Start()
```

**REST Endpoints:**

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/graph` | Create a new graph |
| `GET` | `/graph/{id}` | Get graph by ID |
| `DELETE` | `/graph/{id}` | Soft-delete graph |
| `GET` | `/graphs` | List all graphs |
| `GET` | `/resolve/{root}/{path}` | Hybrid path resolution |
| `POST` | `/resolve` | Resolve with POST body |
| `GET` | `/proof/{root}/{path}` | Generate proof for path |
| `GET` | `/arc/{root}/{path}` | Query arc target |
| `GET` | `/snapshot/{root}` | Get arc set snapshot |
| `GET` | `/content/{cid}` | Fetch raw block from CAS |
| `POST` | `/update/{root}/{path}` | Update single arc |
| `POST` | `/update/batch/{root}` | Batch update arcs |
| `POST` | `/structure` | Create new structure |
| `POST` | `/verify` | Verify a resolution transcript |
| `GET` | `/health` | Health check |

### B. Path Resolution (Read)

```
GET /resolve/{root}/{path}
  → NodeAdapter.HybridResolve(root, path)
  → Node.HybridResolver().Resolve(rootCid, path)
  → resolver.Resolver.Resolve (loop):
      ├── codec.IsMaltCid? → explicit.Resolver.Resolve
      │     ├── EAT.Get(bucketId, root, candidatePath)  # longest prefix
      │     ├── EAT.Snapshot + SCE.Prove                # crypto proof
      │     └── Returns ExplicitEvidence
      │
      └── → implicit.Resolver.Resolve
            ├── CAS.Get(root)                           # fetch block
            ├── Detect codec (dag-pb / dag-cbor / dag-json / raw)
            ├── If dag-pb → detect HAMT → hamt.Resolver
            │     Otherwise → codec.ResolveLink (IPLD decode)
            └── Returns ImplicitEvidence
  → Returns { Target, Transcript }
```

### C. Arc Update (Write)

```
POST /update/{root}/{path}
  → WriteAdapter.UpdateArc(ctx, bucketId, root, path, target)
  → writer.Writer.UpdateArc
      ├── EAT.Get(bucketId, root, path)           # get old value
      ├── EAT.Snapshot(bucketId, root)            # arc set snapshot
      ├── SCE.Update(root, snapshot, path, old, new)  # update commitment
      ├── EAT.Update(bucketId, newRoot, oldRoot, {path: new})  # update EAT
      └── LineageRecorder.Record(ctx, newRoot, oldRoot)        # record lineage
  → Returns { OldRoot, NewRoot, Op: insert|replace|delete }
```

---

## Deployment Architecture

### MemoryDeployment (`core/deployment/`)

```
MemoryDeployment (for testing and dev)
├── memory.KV
├── overwrite.EAT
├── KZG Scheme → SCE
└── Graph(ReadAdapter + WriteAdapter)
```

### Node (api)

`api.Node` is the application-level entry point. It uses functional options to compose components:

```go
api.NewNode(
    api.WithKVStore(...),
    api.WithCommitment(...),
    api.WithEAT(...),
    api.WithCAS(...),
    api.WithConfigFile("malt.json"),
)
```

---

## Auxiliary Components

| Component | Location | Function |
|-----------|----------|----------|
| `lineage` | `core/lineage/` | Version lineage (root → parent chain), independent index |
| `replication` | `core/replication/` | Graph snapshot Export/Import/Sync for node replication |
| `codec` | `core/codec/` | CID codec detection (`IsMaltCid`, `GetMaltCodec`) |
| `evidence` | `core/types/` | Evidence types: `ExplicitEvidence`, `ImplicitEvidence`, `HAMTEvidence` |
| `arcset` | `core/types/` | Arc set abstractions: `View`, `Snapshot`, `Iterator`, `Map` |

---

## Verification Model

MALT uses **node-relative compositional verification**:

1. The resolver (untrusted) produces a `Transcript` recording each step's evidence
2. The client independently verifies each step using:
   - **Explicit steps**: SCE.Verify with the cryptographic proof
   - **Implicit steps**: Block hash verification + path mapping within the block
3. The full path is valid iff every step in the transcript verifies

This decouples resolution from verification, enabling:
- Untrusted resolvers (any node can perform resolution)
- Client-side verification without re-executing resolution
- Proof aggregation for batch verification

---

## CAS Configuration

| Flag | Default | Description |
|------|---------|-------------|
| `--ipfs-api` | (empty) | Connect to local IPFS daemon (full read+write CAS) |
| `--mock-latency` | ProbeLab-based | Override mock CAS latency (e.g., "100ms", "5s", "0s") |
| `--config, -c` | (none) | Load config from JSON file |
| `--listen, -l` | `:8080` | Listen address |

Mock CAS defaults are based on ProbeLab Kubo v0.39.0 measurements (Europe Frankfurt, DHT):

| Operation | Default | Source |
|-----------|---------|--------|
| Get | 2.1s | TTFB + provider discovery + broadcast |
| Put | 1.4s | Add Duration (merkle-izing + block storage) |
| Has | 100ms | Index lookup only |
