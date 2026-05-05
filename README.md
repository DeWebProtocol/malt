# MALT

MALT is an authenticated mutable structure layer over immutable
content-addressed storage.

Payload remains ordinary CAS content identified by CID. MALT defines how
mutable structure above those payload CIDs is expressed, persisted,
authenticated, read, written, and verified.

## Core Idea

MALT core consists of ArcTable, stateless commitment backends, and the list/map
semantic layer over immutable CAS payloads. ArcTable stores and materializes
arcsets for fast lookup, but does not provide correctness. Commitment backends
are stateless vector-commitment proof primitives. The list/map layer is the
semantic abstraction exposed above them.

Gateway reads use the standard shape:

```text
Read(root, query) -> result + ProofList
VerifyRead(root, query, result, ProofList) -> valid / invalid
```

A structure root exposes authenticated read/write semantics, but those
semantics are owned by `list` and `map`, not by the current runtime `graph`
object. Root-centric gateway materialization accepts semantic mutations
produced by layouts and returns an operational receipt. Root publication,
freshness, and multi-writer arbitration are application or deployment policy,
not the gateway correctness interface. In the HTTP deployment, blob reads may carry
`ProofList` in response metadata/header; large-file range reads return selected
bytes plus the corresponding `ProofList`.

Current core semantics are:

- `list`
  - describes complex graph nodes with ordered/indexed/ranged child references
  - query: index or range
  - mutation: append, replace, or truncate
  - use case: chunk sequences for large or mutable files
- `map`
  - describes authenticated relations among graph nodes
  - query: key or path-like key
  - mutation: insert, replace, or delete a binding
  - use case: directory-like metadata, object fields, path indexes
  - every map semantic object carries the reserved `@payload` binding

Both semantics use ArcTable for namespace-scoped arcset persistence/materialization
and stateless commitment backends for proofs.

## What MALT Changes

Traditional Merkle-DAG traversal commits structure implicitly in parent
content. A local structural change can force rootward object rewrite.

MALT changes the boundary:

- payload is still immutable CAS data
- structure is authenticated by independent structure roots
- list/map semantic layer defines typed read and write semantics
- local structure changes advance structure roots without rewriting unrelated
  payload blocks

The claim is not that MALT makes updates free. The claim is that it replaces
implicit ancestor-rewrite costs with explicit, verifiable structure maintenance.

## Runtime Shape

The current prototype is packaged as a single binary named `malt`.

Current runtime shape:

- `malt daemon`
  - long-running local process
  - owns hot proving/index state
- `malt add`
  - uploads payload directly to CAS
  - commits file and directory structure through MALT list/map semantics
  - writes under `--root` when extending an existing root, or creates a new
    root when omitted
- `malt cat`, `malt get`
  - product file and directory read commands over explicit roots
- `malt semantic-mutation`
  - materializes root-centric semantic mutation requests
- `malt resolve`, `malt prove`, `malt prooflist`, `malt update`,
  `malt verify`, `malt lineage`, `malt metrics`, `malt eval-read`
  - lower-level thin HTTP clients against the local daemon
- `malt cas`
  - direct convenience commands against the configured CAS endpoint
- daemon API
  - root-centric HTTP/JSON surface rooted at `/`
- embedded mock CAS
  - optional same-process second-port service
  - fixed Kubo-compatible API at `/api/v0`

The file commands are the current product layout built above MALT. They are
not the definition of the MALT semantic layer.

## Data Model

MALT separates:

- payload content
- authenticated mutable structure

Payload CIDs identify immutable CAS blocks.
Structure roots identify committed list/map semantic state.

In the current root-centric prototype:

- a directory root is a directory-shaped `map` root
- large files are represented by `list` roots referenced from map bindings
- every MALT-native `map` root carries a reserved `@payload` binding as a map
  semantic invariant
- empty payloads should use a defined empty-block CID rather than omitting
  `@payload`

The `@payload` binding is reserved by map semantic objects for terminal
materialization. List roots are terminal typed keys and do not auto-redirect
through `@payload`.

The daemon still uses an internal namespace for local state placement and
materialization. That namespace is not part of the core list/map semantics.
Applications decide which root becomes a published head, how freshness is
communicated, and whether concurrent roots are merged.

## Terminology and Layers

| Layer | Role | Examples |
| --- | --- | --- |
| Semantic Layer | Abstract list/map semantics | `list`, `map` |
| ArcTable | Namespace-scoped arcset persistence/materialization | overwrite, versioned |
| Commitment Backend | Stateless primitive authentication | KZG, IPA |
| Gateway | Deployment surface for semantic mutations and verifiable reads | daemon HTTP API |
| Application Layout | Product data model built above semantic layer | flattened UnixFS-style bucket layout |

Under this terminology:

- `list` and `map` are semantic abstractions.
- ArcTable is materialization and lookup state used by those semantics.
- Commitment backends authenticate semantic-layer arcset/cell/node representations.
- Layouts translate source-domain data into MALT semantic mutations.
- The gateway accepts those semantic mutations and returns `result + ProofList`
  on reads.
- Resolver and writer code paths are runtime adapters, not the core semantic
  definition of MALT.
- The current `core/graph` package is runtime metadata/composition code; it is
  not the target semantic abstraction.

## Map Semantic

The map semantic describes authenticated key-to-CID or key-to-node relations.

Native operations:

- read one key and return the target binding plus proof
- verify a key binding under a root
- insert, replace, or delete a binding to produce a new root
- reserve `@payload` as the terminal materialization binding for every map
  semantic object

The current default implementation is `core/structure/mapping/radix`, a
digest-keyed radix map over an index commitment backend. The older
`core/structure/mapping/indexed` package remains as a simpler comparison path.

The explicit path resolver should be understood as a compatibility layer above
map reads. It may implement longest-prefix path matching, but map itself
owns exact key lookup and proof generation.

## List Semantic

The list semantic describes authenticated stable-indexed sequences inside
complex graph nodes.

Native operations:

- read an index or range and return keys plus proof
- verify an index or range query under a root
- append a key
- replace an existing key
- truncate the sequence

List does not have path-resolution semantics. File reads use an application
layout that maps byte ranges to list index ranges, then calls list semantics.

The current implementation is `core/structure/list/tree`, a tree-shaped
stable-indexed layout over the same primitive commitment interface.

## Flattened UnixFS-Style Layout

The current prototype includes a first pure MALT structure version of
UnixFS-like file and directory semantics in `core/layout/malt/unixfs`.

- directories and files are committed as map roots
- directory entries are map bindings from path segment to child root
- file `@payload` bindings point to a raw CAS CID for small files
- large file `@payload` bindings point to a list root of chunk CIDs
- payload and chunks remain CAS CIDs
- path lookup composes map reads
- file range reads map byte ranges to list index reads

This layout gives a clean benchmark target:

- pure MALT structure UnixFS
- IPLD UnixFS/HAMT baseline

The comparison should measure path lookup, range read, chunk update, directory
mutation, proof size, and write amplification.

Current boundary:

- `core/layout/malt/unixfs` remains the direct map/list/CAS library layer for the
  UnixFS-style layout and translates source-domain file/directory data into
  MALT semantic mutations.
- The root-centric daemon exposes this layout through routes for UnixFS file
  and directory writes, path stat, and content reads, and the `malt add`,
  `malt cat`, and `malt get` commands use those root APIs.
- The layout still depends directly on `mapping.Semantics`, `list.Semantics`,
  and `cas.Client`; it does not make current `core/graph`, `core/writer`, or
  `core/resolver` the semantic owners.
- Graph-level node/arc terminology, gateway semantic-mutation schema,
  `ProofList`, write receipts, and benchmark-facing proof reporting remain
  explicit TODO items.

## CAS Boundary

MALT is not primarily a payload-upload proxy.

Recommended boundary:

- immutable payload publication is primarily a client-to-CAS operation
- MALT owns authenticated structure operations and proof generation
- MALT may still fetch CAS blocks on read paths when an application layout needs
  payload materialization or legacy compatibility traversal

`malt add ...` is a client-side orchestration convenience:

- publish payload to CAS
- build map/list roots
- attach resulting roots into the selected bucket layout

## Interoperability

MALT roots are CID-compatible identifiers. MALT-native structures and ordinary
IPLD/CAS objects can reference each other.

Compatibility traversal through IPLD blocks is useful, but it is not the primary
definition of MALT. The primary definition is the list/map semantic layer over
CAS payloads.

## Verification

Verification is local to the client:

1. a service executes a graph read or write
2. reads return `result + ProofList`
3. the client verifies the ProofList against the relevant root
4. correctness does not depend on trusting the daemon, resolver, ArcTable, or
   cache state

## Core Components

- `core/structure/mapping`
  - public map semantic abstraction and shared map types
- `core/structure/mapping/radix`
  - primary map implementation
- `core/structure/list`
  - public list semantic abstraction and shared list types
- `core/structure/list/tree`
  - primary list implementation
- `core/commitment`
  - stateless primitive commitment backends
- `core/arctable`
  - namespace-scoped arcset persistence/materialization
- `core/layout/malt/unixfs`
  - current pure MALT UnixFS-style layout prototype over map/list semantics
- `core/resolver`
  - current runtime compatibility/read adapter code
- `core/writer`
  - current concrete map/arcs write adapter, not the target abstract writer
- `core/graph`
  - current runtime metadata/composition package, not the semantic abstraction
- `core/lineage`
  - auxiliary version-history metadata; pending redesign with MVCC and
    versioned ArcTable
- `core/querypath`
  - current query-path canonicalization helper for root-relative paths
- `core/manifest`
  - current UnixFS directory-manifest helper above the semantic layer

## Config

Current operator flow:

1. run `malt init`
2. create `~/.malt/malt.json`
3. choose a local state root
4. run `malt daemon`

Current schema:

- `rpc.listen`
- `state.root_dir`
- `state.kvstore`
- `state.kvstore.type` accepts `badger`, `memory`, or `fs`
- `state.arctable`
- `state.lineage`
- `structure.default_backend` accepts `kzg` or `ipa`
- `cas.mode`
- `cas.base_url`
- `cas.timeout`
- `cas.embedded_mock`
- `logging`
- `client`

Current defaults:

- daemon listen: `127.0.0.1:4317`
- embedded mock CAS listen: `127.0.0.1:4318`
- structure backend: `kzg`
- ArcTable type: `versioned`
- CAS mode: `embedded-mock`

## Repo Layout

```text
malt/
|-- client/          # thin daemon HTTP client
|-- cmd/
|   `-- malt/
|-- config/
|-- httpapi/         # shared daemon request/response payload types
|-- core/
|   |-- api/          # top-level wiring via Node
|   |-- arctable/     # namespace-scoped arcset persistence/materialization
|   |-- cas/          # CAS clients and adapters
|   |-- codec/        # MALT CID codecs and CID utilities
|   |-- commitment/   # primitive commitment backends
|   |-- graph/        # current runtime metadata/composition
|   |-- kvstore/      # KV backends
|   |-- metrics/      # node-local evaluation counters
|   |-- querypath/    # root-relative query path canonicalization
|   |-- lineage/      # auxiliary version-history metadata
|   |-- manifest/     # UnixFS directory-manifest helper
|   |-- resolver/     # current read compatibility adapters
|   |-- structure/    # list/map semantic abstractions and implementations
|   |-- types/        # arc sets, evidence, proof-related types
|   `-- writer/       # current concrete write adapter
|-- server/          # daemon HTTP server
`-- integration/
```

## More Detail

For implementation structure and code-level control flow, see
[`ARCHITECTURE.md`](./ARCHITECTURE.md).
