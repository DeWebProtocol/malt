# MALT Architecture

## Overview

MALT is an authenticated mutable structure layer over immutable
content-addressed storage.

MALT core consists of ArcTable, stateless commitment backends, and the list/map
semantic layer over immutable CAS payloads. A MALT graph is not a Go runtime
object that merely holds dependencies. It is the authenticated structure induced
by list/map semantics, ArcTable arcset persistence, and stateless commitment
proofs.

```text
GraphRead(root, query) -> result + ProofList
VerifyRead(root, query, result, ProofList) -> valid / invalid

ApplyMutation(baseRoot, semantic mutation) -> newRoot + writeReceipt
```

List and map are semantic abstractions:

- list semantic: complex graph nodes with stable-indexed or range-addressed
  child references
- map semantic: authenticated keyed/path-like relations among graph nodes

ArcTable is namespace-scoped arcset persistence/materialization and does
not provide correctness by itself. Commitment backends are stateless
vector-commitment primitives used to authenticate semantic-layer
representations. `graph` is the abstraction boundary around resolver and
writer ports: resolver is the read/proof path, writer is the mutation path.
Layouts translate source-domain data into writer semantic mutations. Server
routes execute graph ports; client-side layout code composes layout mutations.

This document is implementation-oriented. For the shorter system overview, see
[`README.md`](./README.md).

## Target Core Model

The target model has four distinct layers:

| Layer | Responsibility |
| --- | --- |
| Semantic layer | Abstract list/map semantics |
| ArcTable | Namespace-scoped arcset persistence/materialization |
| Commitment backend | Stateless proof and verification over semantic-layer representations |
| Graph ports | Resolver and writer boundaries over graph state |
| Server API | Runtime surface exposing graph ports |
| Application layout | Product data model built above list/map/CAS blobs |

The public structure layer should expose list/map semantics, not storage
machinery.

### Semantic Layer

The semantic layer defines the shape of authenticated operations. It should be
generic enough for list, map, and future semantics, but narrow enough that it
does not erase their differences.

Conceptually, graph reads return `result + ProofList`, where the ProofList is
the vector-commitment proof chain from root to destination. Root-centric writer
mutation accepts semantic mutations that have already been produced by a layout
and returns an operational receipt without publishing a managed head. The HTTP
server returns default `GET /{root}/{path}` file and directory reads with
`ProofList` metadata in `X-Malt-ProofList` response
headers encoded as `base64url-json`. Clients can opt out of default proof
generation with `?proof=false` or `X-Malt-Proof: omit`; default GET responses
advertise that request-header variance with `Vary: X-Malt-Proof`. Large-file
byte-range reads include path/`@payload` proof plus one measured-list
`list_range` step. That step carries authenticated fixed chunk metadata, the
covered segment CIDs, and a proof payload composed from the metadata slot proof
and the required index proofs. Response-body range binding is still a
ProofList-schema TODO. File routes are product surfaces around the same
root-centric mutation namespace.

### List Semantic

The list semantic authenticates stable-indexed child references inside complex
graph nodes.

Read semantics:

- first-class index query
- optional measured range query over byte intervals when the implementation
  authenticates byte-layout metadata
- length-aware proof

Native writes:

- append
- replace existing index
- truncate

List does not have path-resolution semantics. Application layouts translate
byte ranges or file operations into list queries. The current fixed-width
measured list maps byte ranges to a minimum segment set and proves the
authenticated metadata plus the required index proofs.

The current public package is `auth/semantic/list`.
The primary implementation is `runtime/semantic/list/tree`.

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
- reserved `@payload` binding for every map semantic object

The current public package is `auth/semantic/mapping`.
The primary implementation is `runtime/semantic/mapping/radix`.
The older indexed map baseline lives under
`cmd/eval/internal/baseline/indexedmap` as an evaluation comparison
implementation. It is not a production map semantic wired by `graph`,
which constructs `mapping/radix`.

The current explicit resolver is best understood as a compatibility layer above
map reads. It can implement longest-prefix path policy, but the map semantic
owns exact key proof generation and verification.

`@payload` is not a UnixFS-only convention. It is the reserved terminal
materialization binding for map semantic objects. List semantic objects do not
implicitly redirect through `@payload`.

### ArcTable

List/map semantics need materialized arcset state in order to prove and update
without reconstructing full payload objects.

In the current implementation this role is served by namespace-scoped
ArcTable:

- point lookup of materialized structure entries
- batch lookup for implementation nodes
- snapshots or iteration when needed
- overwrite current-state mode
- versioned MVCC-style retrieval

ArcTable is not the trust root. Incorrect materialized state is rejected by
proof verification or root recomputation.

The namespace identifier is an operational collection boundary for state
placement, replication, migration, GC, and bloom/index configuration. It is not
part of the mathematical list/map semantics, and it does not decide which root
should become an application's current head.

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

Current package roles are:

- `auth/semantic/mapping`
  - target public map semantic abstraction and shared types
- `runtime/semantic/mapping/radix`
  - primary map implementation
- `auth/semantic/list`
  - target public list semantic abstraction and shared types
- `runtime/semantic/list/tree`
  - primary list implementation
- `runtime/arctable`
  - namespace-scoped arcset persistence/materialization
- `runtime/arctable/bloom`
  - optional negative-lookup optimization hook behind ArcTable implementations,
    disabled unless the ArcTable is constructed with a BloomCache
- `auth/commitment`
  - stateless primitive commitment backends
- `layout/unixfs`
  - current pure MALT UnixFS-style layout prototype built directly over
    `mapping.Semantics`, `list.Semantics`, and CAS
- `graph/resolver`
  - resolver read port and explicit proof path
- `graph/writer`
  - writer mutation model and executor
- `graph`
  - graph abstraction contracts
- `runtime/graph`
  - concrete graph runtime composition around resolver and writer executors
- `graph/querypath`
  - current query-path canonicalization helper for root-relative paths
- `layout/unixfs/internal/manifest`
  - current UnixFS directory-manifest helper above the semantic layer

## Runtime Packaging

The product shape is a small primary runtime binary named `malt`, plus one
evaluation binary named `malt-eval` with workload-specific subcommands.

Current command model:

- `malt init`
  - creates the local configuration file and state-root directory
- `malt start/status/stop/restart`
  - manage the local daemon as a background process
  - owns hot structure state
  - serves local HTTP/JSON API requests
- `malt add ...`
  - client-side file and directory ingestion
  - uploads payload blocks to CAS
  - commits structure through list/map semantics
  - writes under `--root` when extending an existing root, or creates a new
    root when omitted
- `malt resolve ...`
  - returns `target + ProofList` by default for a root-relative read
- `malt verify ...`
  - verifies a ProofList
- `malt-eval read`
  - read benchmark driver for `maltflat`, IPLD UnixFS, and IPLD UnixFS+HAMT
    baselines
- `malt-eval write`
  - Git trace write-amplification replay driver
- `malt-eval run`
  - evaluation framework runner for JSON plans and structured run directories
- `malt-eval schema`
  - lists or prints embedded evaluator JSON schemas
- `malt-eval summarize`
  - regenerates figure CSVs from framework raw envelopes
- `malt-eval metrics`
  - daemon evaluation metrics client

Current runtime invariants:

- a directory root is a directory-shaped map root
- a list root represents file-content structure and is not a directory root
- every MALT-native map root carries the reserved `@payload` binding as a map
  semantic invariant
- materializing a bare map root means resolving `@payload` first
- list roots are terminal typed keys and do not auto-redirect through
  `@payload`
- root-relative path misses are reported as `not found`

Path-miss behavior is a file-layout and product-runtime invariant. The reserved
`@payload` binding belongs to map semantic objects.

## Code Layout

```text
malt/
|-- cmd/
|   |-- internal/
|   |   `-- merkledagimport/ # command-local Merkle-DAG UnixFS import helpers
|   |-- eval/
|   |   |-- command/
|   |   |-- helper/
|   |   |-- schemas/
|   |   `-- malt-eval/
|   `-- malt/
|-- config/
|-- daemon/
|-- api/http/         # shared daemon API payloads
|-- server/          # daemon HTTP server
|-- auth/             # data-authentication core
|   |-- arcset/       # canonical arcs and coordinates
|   |-- commitment/   # primitive commitment backends
|   |-- proof/        # evidence and ProofList artifacts
|   `-- semantic/     # list/map semantic interfaces
|-- graph/            # resolver/writer graph contracts
|   |-- querypath/    # root-relative query path canonicalization
|   |-- resolver/     # resolver read port and explicit proof path
|   `-- writer/       # writer mutation model and executor
|-- layout/
|   `-- unixfs/       # current map/list-based UnixFS layout prototype
|-- runtime/          # node, graph composition, ArcTable, metrics, and semantic implementations
|-- sdk/
|   `-- client/       # Go daemon client facade
|-- storage/          # CAS and KV storage libraries
|-- wire/
|   `-- maltcid/      # MALT map/list root CID codecs
`-- logger/
```

## Map Semantic Implementation

`auth/semantic/mapping` defines the public keyed-map interface.

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

`cmd/eval/internal/baseline/indexedmap` is a baseline comparison
implementation for the same public map interface. It is eval-local and should
not be treated as the production map semantic used by `graph`, resolver
adapters, or the current UnixFS-style layout path.

No external writer should redefine map semantics. A layout or write adapter may
only produce semantic mutations and orchestrate calls into map semantic
operations.

## List Semantic Implementation

`auth/semantic/list` defines the public stable-indexed list interface.

The current interface exposes:

- `Commit`
- `Prove`
- `Verify`
- `Replace`
- `Append`
- `Truncate`

This already approximates the target list semantic shape:

- `Prove` is the list read path for index queries
- optional `MeasuredSemantics` exposes `ProveRange` and `VerifyRange` for
  byte-addressable fixed-chunk lists
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

## Graph Ports, Layout, and Adapters

The old architecture treated resolver and writer as central runtime layers. The
current interpretation is narrower and makes `graph` the boundary around
read and write ports:

- layouts translate source-domain data into MALT semantic mutations
- the writer port applies converted semantic mutations through list/map
  semantic operations and returns a write receipt
- resolver reads return `result + ProofList`
- resolver adapters translate application or compatibility reads into semantic
  reads and proof chains
- neither adapter owns the semantic definition of map or list

The explicit resolver is a map compatibility adapter:

1. apply path policy, such as longest-prefix matching
2. call map semantic proof generation for the selected exact key
3. wrap the map proof as resolver evidence

The current concrete `writer.Writer` is the mutation boundary. It accepts
layout-produced semantic mutations, applies map/list deltas, and returns an
operational receipt. It does not publish an authoritative head; applications
decide which resulting root becomes current.

`ProofList` is the standard verifier-facing read artifact. It should cover map
step proofs, terminal `@payload` proofs, list index proofs, measured-list
`list_range` evidence for range reads, and blob target binding proofs from the
queried root to the destination. Current `list_range` steps carry fixed chunk
metadata, covered segment CIDs, and metadata/index proof payload. Response-body
range binding remains a schema TODO.

The current daemon has two HTTP proof-bearing read surfaces:

- default `GET /{root}/{path}` returns content or directory JSON and places the
  verifier-facing `ProofList` in `X-Malt-ProofList` with
  `X-Malt-ProofList-Encoding: base64url-json`
- `GET /resolve/{root}/{path}` returns `target` plus `prooflist` by
  default. Clients can opt out with `?proof=false` or `X-Malt-Proof: omit`

`HEAD /{root}/{path}` is intentionally stat-only and returns stat headers
without generating proof metadata.

## MALT UnixFS-Style Layout

The current code includes a first pure MALT structure UnixFS-like layout in
`layout/unixfs`.

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

`malt add --target malt --model unixfs` currently accepts `--layout flat` and
`--layout hierarchical`, with `flat` as the default. Both names currently route
through the same staged hybrid materialization path: ordinary directories are
materialized as map roots, and directory/root maps also include descendant
full-path bindings for longest-prefix reads. A future implementation split can
evaluate pure root-map `flat` behavior separately from pure per-directory
`hierarchical` behavior.

Current boundary:

- The package remains the direct map/list/CAS library layer for the
  UnixFS-style layout and translates source-domain file/directory data into
  MALT semantic mutations.
- `POST /{root}/_mutate` is the writer mutation route. Root-centric daemon
  routes also expose `POST /{root}/{path}` as a UnixFS layout convenience: it
  stages a file or directory operation, converts the resulting layout state into
  a semantic mutation, and then uses the same writer mutation path as
  `_mutate`.
- The public CLI exposes ingestion through `malt add`; reads are available
  through the daemon API and proof-bearing resolve/content endpoints.
- The package still directly injects `mapping.Semantics`, `list.Semantics`,
  and `cas.Client`; current `graph`, `graph/writer`, and `graph/resolver`
  remain runtime and compatibility adapters rather than semantic owners.
- The current writer semantic-mutation and `ProofList` schemas are
  implemented. Paper-facing formalization, write metadata semantics,
  graph-node terminology, and benchmark-facing proof reporting remain tracked
  as proposal-stage MIPs in the sibling documents repository.
- Graph manager metadata is limited to lifecycle and runtime profile
  compatibility. It does not store an authoritative current root or publish
  freshness. The current daemon path creates an ad hoc default `Graph` through
  `Node.NewGraph("default")` and does not expose the managed graph lifecycle as
  a public API.

It also gives the benchmark target:

- pure MALT structure UnixFS
- IPLD UnixFS/HAMT baseline

Metrics:

- path lookup latency
- range read latency
- evidence item count (`ProofList` steps for MALT, CAS block fetches for IPLD
  baselines)
- chunk update cost
- directory mutation cost
- proof size
- write amplification

Open proposal-stage MIPs for the next discussion:

- define graph node and arc terminology precisely enough to map onto current
  map/list semantics
- formalize the current writer semantic-mutation schema
- formalize how `layout/unixfs` exposes semantic mutations for writer
  application and how much of the current UnixFS convenience route
  remains public API versus test/demo scaffolding
- formalize the current `ProofList` schema and verification semantics for path
  lookup, terminal `@payload`, blob bindings, measured-list `list_range`
  evidence for range reads, and response-body range binding
- decide how UnixFS reads should map onto resolver read queries and `ProofList`
- define the final UnixFS write receipt and application-level concurrency
  contract for the already-wired root APIs
- decide after the first benchmark whether list needs a future compact
  range-proof API; the current prototype uses path/`@payload` proof plus one
  measured-list `list_range` step carrying metadata, segment CIDs, and
  metadata/index proofs

## ArcTable and Versioning

ArcTable currently provides materialization and lookup support.

Current implementations:

- `overwrite`
  - current-view storage
- `versioned`
  - delta-per-version storage linked by `@previous`

The `@previous` chain and MVCC behavior should be treated as versioning and
optimization concerns. They are not part of the minimal semantic read/write
interface. Versioned ArcTable can preserve concurrent roots without overwriting
each other, but choosing which root becomes a published head is an application
or deployment policy.

The earlier separate lineage index duplicated part of this conceptual space and
has been removed from the runtime. Version traversal should be derived from the
versioned ArcTable or from application-level root publication metadata when a
concrete use case requires it.

`runtime/arctable/bloom` is retained as the optional negative-lookup optimization
behind ArcTable implementations. The default root-centric runtime does not make
it part of read/write semantics or trust, so it is dormant optimization
machinery rather than deletion-ready dead code.

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
- daemon, resolver adapters, ArcTable, and caches are untrusted execution state
- bad state can affect latency or availability, but not accepted correctness

Freshness, root publication, and multi-writer arbitration remain application or
deployment policies unless the system explicitly extends the semantic layer.

## Configuration Direction

The target operator flow is:

1. run `malt init`
2. create `~/.malt/malt.json`
3. choose a local state root
4. run `malt start` for a managed background daemon

Current config shape:

```json
{
  "rpc": {
    "listen": "127.0.0.1:4317",
    "cors_allowed_origins": [
      "https://dewebprotocol.dev",
      "https://dewebprotocol.github.io",
      "http://localhost:*",
      "http://127.0.0.1:*",
      "http://[::1]:*"
    ]
  },
  "state": {
    "root_dir": "D:/malt-state",
    "kvstore": {
      "type": "badger",
      "path": "kv"
    },
    "arctable": {
      "type": "versioned"
    }
  },
  "structure": {
    "default_backend": "kzg"
  },
  "cas": {
    "mode": "external",
    "base_url": "http://127.0.0.1:4318",
    "timeout": "30s"
  }
}
```

Allowed runtime values:

- `state.kvstore.type`: `badger`, `memory`, or `fs`
- `structure.default_backend`: `kzg` or `ipa`
- `rpc.cors_allowed_origins`: browser origins allowed to call local read/proof
  routes and `POST /verify`. Empty or omitted disables browser CORS. Exact
  origins are matched literally. Port wildcards are only supported for loopback
  origins such as `http://localhost:*`, `http://127.0.0.1:*`, and
  `http://[::1]:*`; the daemon still replies with the concrete request origin.
  The daemon reads this policy at startup. Change the file and restart the
  managed daemon to apply browser-access changes.

This config is a packaging and runtime decision. It does not define the core
MALT semantic abstraction.
