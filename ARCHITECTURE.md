# MALT Architecture

## Overview

MALT is an authenticated mutable structure layer over immutable
content-addressed storage.

The architectural center is an abstract authenticated graph contract. A MALT
graph is not a Go runtime object that merely holds dependencies. It is a
semantic object that defines how a structure root is read, written, and
verified.

```text
Read(root, query) -> result + proof
VerifyRead(root, query, result, proof) -> valid / invalid

Write(root, mutation) -> newRoot + receipt
VerifyWrite(root, mutation, newRoot, receipt) -> valid / invalid
```

Map and list are concrete implementations of that contract:

- map graph: authenticated key-to-CID bindings
- list graph: authenticated stable-indexed or range-addressed sequences

ArcTable and commitment backends are internal mechanisms used by graph
implementations. Resolver, writer, and current graph packages are runtime or
compatibility adapters around those implementations.

This document is implementation-oriented. For the shorter system overview, see
[`README.md`](./README.md).

## Target Core Model

The target model has four distinct layers:

| Layer | Responsibility |
| --- | --- |
| Graph contract | Abstract authenticated read/write/verify semantics |
| Graph implementation | Concrete map/list semantics satisfying the contract |
| Structure storage | Materialized state needed by an implementation |
| Commitment backend | Primitive proof and verification over positioned cells |

The public structure layer should expose graph implementations, not storage
machinery.

### Graph Contract

The graph contract defines the shape of authenticated operations. It should be
generic enough for map, list, and future graph implementations, but narrow
enough that it does not erase their semantics.

Conceptually:

```go
type Graph[Query any, Result any, Mutation any, Receipt any] interface {
    Read(ctx context.Context, root cid.Cid, query Query) (Result, Proof, error)
    VerifyRead(root cid.Cid, query Query, result Result, proof Proof) (bool, error)

    Write(ctx context.Context, root cid.Cid, mutation Mutation) (cid.Cid, Receipt, error)
    VerifyWrite(oldRoot, newRoot cid.Cid, mutation Mutation, receipt Receipt) (bool, error)
}
```

The current code does not yet expose exactly this package-level interface. The
documented direction is to make map/list the first graph implementations and to
move runtime adapters around them.

### Map Graph

The map graph authenticates key-to-CID bindings.

Native reads:

- exact key lookup
- binding proof
- binding verification

Native writes:

- insert absent key
- replace existing key
- delete existing key

The current public package is `core/structure/mapping`.
The primary implementation is `core/structure/mapping/radix`.

The current explicit resolver is best understood as a compatibility layer above
map graph reads. It can implement longest-prefix path policy, but the map graph
owns exact key proof generation and verification.

### List Graph

The list graph authenticates stable-indexed sequences.

Native reads:

- index query
- range query
- length-aware proof

Native writes:

- append
- replace existing index
- truncate

List does not have path-resolution semantics. Application layouts translate
byte ranges or file operations into list index/range operations.

The current public package is `core/structure/list`.
The primary implementation is `core/structure/list/tree`.

### Structure Storage

Graph implementations need materialized state in order to prove and update
without reconstructing full payload objects.

In the current implementation this role is served by ArcTable:

- point lookup of materialized structure entries
- batch lookup for implementation nodes
- snapshots or iteration when needed
- optional version-aware retrieval

ArcTable is not the trust root. Incorrect materialized state is rejected by
proof verification or root recomputation.

### Commitment Backend

Primitive commitment backends authenticate already-positioned values selected
by a graph implementation.

They are responsible for:

- commit
- prove
- verify
- update when the backend supports efficient local updates

They are not responsible for:

- map key semantics
- list range semantics
- path resolution
- application layout
- graph lifecycle

Current in-tree backends:

- `commitment/kzg`
- `commitment/ipa` for experimental selectable runs

## Current Implementation Status

Some package names still reflect an older runtime-first design. Interpret them
as follows:

- `core/structure/mapping`
  - target public map graph contract and shared types
- `core/structure/mapping/radix`
  - primary map graph implementation
- `core/structure/list`
  - target public list graph contract and shared types
- `core/structure/list/tree`
  - primary list graph implementation
- `core/arctable`
  - storage/materialization support for graph implementations
- `core/commitment`
  - primitive commitment backends
- `core/resolver`
  - current runtime read loop and compatibility adapters
- `core/writer`
  - current concrete map/arcs write adapter, not the target abstract writer
- `core/graph`
  - current graph metadata and runtime composition, not the target graph
    contract
- `core/lineage`
  - auxiliary version-history metadata, pending redesign with MVCC and
    versioned ArcTable
- `core/bucketpath` and `core/manifest`
  - current file/bucket layout helpers, candidates to move under a dedicated
    UnixFS-style layout package

## Runtime Packaging

The product shape is a single binary named `malt`.

Current command model:

- `malt daemon`
  - long-running local process
  - owns hot structure state
  - serves local HTTP/JSON API requests
- `malt bucket ...`
  - manages daemon-side buckets and the client-side default bucket
- `malt add ...`
  - client-side file and directory ingestion
  - uploads payload blocks to CAS
  - commits structure through map/list graph implementations
- `malt cat ...` and `malt get ...`
  - product read paths for bucket-local files and directories
- lower-level commands
  - resolve, prove, update, verify, and lineage inspection
- `malt cas ...`
  - convenience commands for CAS-oriented workflows

Current runtime invariants:

- a managed bucket head is a directory-shaped map root
- a list root represents file-content structure and is not a valid managed
  bucket head
- every MALT-native map root carries the reserved `@payload` binding
- materializing a bare map root means resolving `@payload` first
- list roots are terminal typed keys and do not auto-redirect through
  `@payload`
- bucket-path misses are reported as `not found`

These are file-layout and product-runtime invariants. They should not be
confused with the core MALT graph contract.

## Code Layout

```text
malt/
|-- client/          # thin daemon HTTP client
|-- cmd/
|   `-- malt/
|-- config/
|-- httpapi/         # shared daemon API payloads
|-- server/          # daemon HTTP server
|-- core/
|   |-- api/          # Node: top-level component wiring
|   |-- arctable/     # graph-state materialization and lookup support
|   |-- bucketpath/   # current bucket path boundary helper
|   |-- cas/          # CAS clients and adapters
|   |-- codec/        # MALT CID codecs and CID utilities
|   |-- commitment/   # primitive commitment backends
|   |-- graph/        # current metadata/runtime composition
|   |-- kvstore/      # KV backends
|   |-- lineage/      # auxiliary version-history metadata
|   |-- manifest/     # current directory-manifest helper
|   |-- resolver/     # current read compatibility adapters
|   |-- structure/    # map/list graph contracts and implementations
|   |-- types/        # arc sets, evidence, proof-related types
|   `-- writer/       # current concrete write adapter
`-- integration/
```

## Map Graph Implementation

`core/structure/mapping` defines the public keyed-map interface.

The current interface exposes:

- `Commit`
- `Prove`
- `Verify`
- `Update`

This already approximates the target graph shape:

- `Prove` is the map read path for exact keys
- `Verify` validates read proof
- `Update` is the map write path
- `Commit` bootstraps a root from a materialized view

The primary implementation, `mapping/radix`, uses a digest-keyed radix layout.
It owns:

- key hashing and placement
- collision handling
- node materialization through ArcTable
- commitment proof construction
- root update computation

No external writer should redefine map semantics. A writer adapter may only
orchestrate calls into map graph operations.

## List Graph Implementation

`core/structure/list` defines the public stable-indexed list interface.

The current interface exposes:

- `Commit`
- `Prove`
- `Verify`
- `Replace`
- `Append`
- `Truncate`

This already approximates the target graph shape:

- `Prove` is the list read path for index queries
- future `Range` should be added for file range workloads
- `Verify` validates read proof
- `Append`, `Replace`, and `Truncate` are list write operations

The primary implementation, `list/tree`, uses a tree-shaped fixed-slot layout.
It owns:

- committed length/header state
- index-to-slot layout
- node materialization through ArcTable
- commitment proof construction
- root update computation

List should not be forced through path-based resolution.

## Resolver and Writer Adapters

The old architecture treated resolver and writer as central runtime layers.
The new interpretation is narrower:

- resolver adapters translate application or compatibility reads into graph
  reads
- writer adapters translate application mutations into graph writes
- neither adapter owns the semantic definition of map or list

The explicit resolver is a map compatibility adapter:

1. apply path policy, such as longest-prefix matching
2. call map graph proof generation for the selected exact key
3. wrap the map proof as resolver evidence

The current concrete `writer.Writer` should be treated as transitional. It
combines map mutation, `@payload` policy, ArcTable delta handling, and lineage
recording. Those responsibilities should be separated when the graph contract is
implemented directly.

## Flattened UnixFS-Style Layout

The first target application layout should be a pure MALT structure UnixFS-like
layout.

Suggested interpretation:

- directory nodes are map graph roots
- file content is represented by list graph roots
- payload blocks and chunks remain ordinary CAS CIDs
- path lookup composes map graph reads
- range load translates byte ranges to list ranges
- file and directory mutation composes map/list graph writes

This layout is not the definition of MALT. It is an application model that
demonstrates that the graph contract can express practical file-system
semantics.

It also gives the benchmark target:

- pure MALT structure UnixFS
- IPLD UnixFS/HAMT baseline

Metrics:

- path lookup latency
- range read latency
- chunk update cost
- directory mutation cost
- proof size
- write amplification

## ArcTable and Versioning

ArcTable currently provides materialization and lookup support.

Current implementations:

- `overwrite`
  - current-view storage
- `versioned`
  - delta-per-version storage linked by `@previous`

The `@previous` chain and MVCC behavior should be treated as versioning and
optimization concerns. They are not part of the minimal graph read/write
contract.

The separate `lineage` package duplicates part of this conceptual space. Until
the MVCC and bucket-based ArcTable design is settled, lineage should be treated
as auxiliary and removable from the core narrative.

## CAS Boundary

The intended boundary with CAS is asymmetric.

Write path:

- the client can encode immutable payload itself
- the client can compute the payload CID itself
- the client can publish payload directly to CAS

Read path:

- MALT may need CAS to materialize payloads
- application layouts may fetch chunks or directory manifests
- compatibility traversal may read ordinary IPLD blocks

MALT should not be framed primarily as a payload-upload proxy. Its core role is
authenticated structure management and proof generation.

## Trust Model

Correctness is cryptographic:

- clients verify proofs or receipts against roots
- daemon, resolver adapters, ArcTable, caches, and lineage metadata are
  untrusted execution state
- bad state can affect latency or availability, but not accepted correctness

Freshness, root publication, and multi-writer arbitration remain application or
deployment policies unless the system explicitly extends the graph contract.

## Configuration Direction

The target operator flow is:

1. run `malt init`
2. create `~/.malt/malt.json`
3. choose a local state root
4. start `malt daemon`

Current config shape:

```json
{
  "rpc": {
    "listen": "127.0.0.1:4317"
  },
  "state": {
    "root_dir": "D:/malt-state",
    "kvstore": {
      "type": "badger",
      "path": "kv"
    },
    "arctable": {
      "type": "versioned"
    },
    "lineage": {
      "enabled": true
    }
  },
  "structure": {
    "default_backend": "kzg"
  },
  "cas": {
    "mode": "external",
    "base_url": "http://127.0.0.1:5001",
    "timeout": "30s",
    "embedded_mock": {
      "enabled": false,
      "listen": "127.0.0.1:4318"
    }
  }
}
```

Allowed runtime values:

- `state.kvstore.type`: `badger`, `memory`, or `fs`
- `structure.default_backend`: `kzg` or `ipa`

This config is a packaging and runtime decision. It does not define the core
MALT graph abstraction.
