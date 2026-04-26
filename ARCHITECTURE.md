# MALT Architecture

## Overview

MALT is an authenticated mutable structure layer over immutable
content-addressed storage.

The architectural center is the list/map semantic layer over immutable CAS
payloads. A MALT graph is not a Go runtime object that merely holds
dependencies. It is the authenticated structure induced by list/map semantics,
ArcTable arcset persistence, and stateless commitment proofs.

```text
Read(root, query) -> result + proof
VerifyRead(root, query, result, proof) -> valid / invalid

Write(root, mutation) -> newRoot + receipt
VerifyWrite(root, mutation, newRoot, receipt) -> valid / invalid
```

List and map are semantic abstractions:

- list semantic: complex graph nodes with stable-indexed or range-addressed
  child references
- map semantic: authenticated keyed/path-like relations among graph nodes

ArcTable is Bucket-based arcset persistence/materialization. Commitment
backends are stateless primitives used to authenticate semantic-layer
representations. Resolver, writer, and current graph packages are runtime or
compatibility adapters around those semantics.

This document is implementation-oriented. For the shorter system overview, see
[`README.md`](./README.md).

## Target Core Model

The target model has four distinct layers:

| Layer | Responsibility |
| --- | --- |
| Semantic layer | Abstract list/map semantics |
| ArcTable | Bucket-based arcset persistence/materialization |
| Commitment backend | Stateless proof and verification over semantic-layer representations |
| Application layout | Product data model built above list/map/CAS blobs |

The public structure layer should expose list/map semantics, not storage
machinery.

### Semantic Layer

The semantic layer defines the shape of authenticated operations. It should be
generic enough for list, map, and future semantics, but narrow enough that it
does not erase their differences.

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
documented direction is to make list/map the first semantic abstractions and to
move runtime adapters around them.

### List Semantic

The list semantic authenticates stable-indexed or ranged child references inside
complex graph nodes.

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

### Map Semantic

The map semantic authenticates key-to-CID or key-to-node relations.

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
map reads. It can implement longest-prefix path policy, but the map semantic
owns exact key proof generation and verification.

### ArcTable

List/map semantics need materialized arcset state in order to prove and update
without reconstructing full payload objects.

In the current implementation this role is served by Bucket-based ArcTable:

- point lookup of materialized structure entries
- batch lookup for implementation nodes
- snapshots or iteration when needed
- overwrite current-state mode
- versioned MVCC-style retrieval

ArcTable is not the trust root. Incorrect materialized state is rejected by
proof verification or root recomputation.

### Commitment Backend

Primitive commitment backends authenticate already-positioned values selected
by the semantic layer.

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
  - target public map semantic abstraction and shared types
- `core/structure/mapping/radix`
  - primary map implementation
- `core/structure/list`
  - target public list semantic abstraction and shared types
- `core/structure/list/tree`
  - primary list implementation
- `core/arctable`
  - Bucket-based arcset persistence/materialization
- `core/commitment`
  - stateless primitive commitment backends
- `core/layout/unixfs`
  - current pure MALT UnixFS-style layout prototype built directly over
    `mapping.Semantics`, `list.Semantics`, and CAS
- `core/resolver`
  - current runtime read loop and compatibility adapters
- `core/writer`
  - current concrete map/arcs write adapter, not the target abstract writer
- `core/graph`
  - current graph metadata and runtime composition, not the target semantic
    abstraction
- `core/lineage`
  - auxiliary version-history metadata pending integration with versioned
    ArcTable
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
  - commits structure through list/map semantics
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
confused with the core MALT semantic layer.

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
|   |-- arctable/     # Bucket-based arcset persistence/materialization
|   |-- bucketpath/   # current bucket path boundary helper
|   |-- cas/          # CAS clients and adapters
|   |-- codec/        # MALT CID codecs and CID utilities
|   |-- commitment/   # primitive commitment backends
|   |-- graph/        # current metadata/runtime composition
|   |-- kvstore/      # KV backends
|   |-- layout/
|   |   `-- unixfs/    # current map/list-based UnixFS layout prototype
|   |-- lineage/      # auxiliary version-history metadata
|   |-- manifest/     # current directory-manifest helper
|   |-- resolver/     # current read compatibility adapters
|   |-- structure/    # list/map semantic abstractions and implementations
|   |-- types/        # arc sets, evidence, proof-related types
|   `-- writer/       # current concrete write adapter
`-- integration/
```

## Map Semantic Implementation

`core/structure/mapping` defines the public keyed-map interface.

The current interface exposes:

- `Commit`
- `Prove`
- `Verify`
- `Update`

This already approximates the target map semantic shape:

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
orchestrate calls into map semantic operations.

## List Semantic Implementation

`core/structure/list` defines the public stable-indexed list interface.

The current interface exposes:

- `Commit`
- `Prove`
- `Verify`
- `Replace`
- `Append`
- `Truncate`

This already approximates the target list semantic shape:

- `Prove` is the list read path for index queries
- first-class `Range` proof support remains a TODO for file range workloads
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

- resolver adapters translate application or compatibility reads into semantic
  reads
- writer adapters translate application mutations into semantic writes
- neither adapter owns the semantic definition of map or list

The explicit resolver is a map compatibility adapter:

1. apply path policy, such as longest-prefix matching
2. call map semantic proof generation for the selected exact key
3. wrap the map proof as resolver evidence

The current concrete `writer.Writer` should be treated as transitional. It
combines map mutation, `@payload` policy, ArcTable delta handling, and lineage
recording. Those responsibilities should be separated when the semantic layer is
implemented directly.

## Flattened UnixFS-Style Layout

The current code includes a first pure MALT structure UnixFS-like layout in
`core/layout/unixfs`.

Current implementation:

- directories and files are committed as map roots
- directory entries are map bindings from one path segment to a child root
- file `@payload` points to a raw CAS CID for small files
- large-file `@payload` points to a list root of chunk CIDs
- payload blocks and chunks remain ordinary CAS CIDs
- path lookup composes exact map reads
- range load translates byte ranges to list index reads

This layout is not the definition of MALT. It is an application model that
demonstrates that list/map semantics can express practical file-system
semantics.

Current boundary:

- The package is a library/prototype and is not yet wired into `malt add`,
  `malt cat`, `malt get`, daemon routes, or managed buckets.
- It directly injects `mapping.Semantics`, `list.Semantics`, and `cas.Client`.
- It intentionally bypasses current `core/graph`, `core/writer`, and
  `core/resolver` while the graph-node and resolver boundaries remain open.
- The unresolved pieces are tracked as TODO items, not as a change to the core
  semantic-layer direction.

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

Open TODOs for the next discussion:

- define graph node and arc terminology precisely enough to map onto current
  map/list semantics
- decide whether `@payload` belongs only to resolver/runtime policy or also to
  graph-node invariants
- decide how `core/layout/unixfs` should expose proof transcripts for path
  lookup and terminal payload materialization
- decide whether UnixFS reads should call current `core/resolver` or keep a
  layout-local resolver
- decide how UnixFS writes should integrate with managed bucket heads and daemon
  APIs
- decide whether list needs a first-class range proof API or whether composed
  index proofs are sufficient for the first benchmark

## ArcTable and Versioning

ArcTable currently provides materialization and lookup support.

Current implementations:

- `overwrite`
  - current-view storage
- `versioned`
  - delta-per-version storage linked by `@previous`

The `@previous` chain and MVCC behavior should be treated as versioning and
optimization concerns. They are not part of the minimal semantic read/write
interface.

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
deployment policies unless the system explicitly extends the semantic layer.

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
MALT semantic abstraction.
