# MALT

MALT is an authenticated mutable structure layer over immutable
content-addressed storage.

Payload remains ordinary CAS content identified by CID. MALT defines how
mutable structure above those payload CIDs is expressed, persisted,
authenticated, read, written, and verified.

## Core Idea

The conceptual center is a list/map semantic layer over immutable CAS payloads:

```text
Read(root, query) -> result + proof
VerifyRead(root, query, result, proof) -> valid / invalid

Write(root, mutation) -> newRoot + receipt
VerifyWrite(root, mutation, newRoot, receipt) -> valid / invalid
```

A structure root exposes authenticated read/write semantics, but those
semantics are owned by `list` and `map`, not by the current runtime `graph`
object.

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

Both semantics use ArcTable for Bucket-based arcset persistence/materialization
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
- `malt bucket`
  - manages daemon-side buckets and the client-side default bucket
- `malt add`
  - uploads payload directly to CAS
  - commits file and directory structure through MALT list/map semantics
- `malt cat`, `malt get`
  - product file and directory read commands over bucket paths
- `malt resolve`, `malt prove`, `malt update`, `malt verify`, `malt lineage`
  - lower-level thin HTTP clients against the local daemon
- `malt cas`
  - direct convenience commands against the configured CAS endpoint
- daemon API
  - fixed HTTP/JSON surface at `/api/v1`
- embedded mock CAS
  - optional same-process second-port service
  - fixed Kubo-compatible API at `/api/v0`

The bucket/file commands are the current product layout built above MALT. They
are not the definition of the MALT semantic layer.

## Data Model

MALT separates:

- payload content
- authenticated mutable structure

Payload CIDs identify immutable CAS blocks.
Structure roots identify committed list/map semantic state.

In the current bucket-first prototype:

- a managed bucket head is a directory-shaped `map` root
- large files are represented by `list` roots referenced from map bindings
- every MALT-native `map` root carries a reserved `@payload` binding
- empty payloads should use a defined empty-block CID rather than omitting
  `@payload`

These bucket rules belong to the current file-system layout. The core MALT
layer only requires list/map semantics to expose authenticated read/write
semantics over roots.

## Terminology and Layers

| Layer | Role | Examples |
| --- | --- | --- |
| Semantic Layer | Abstract list/map semantics | `list`, `map` |
| ArcTable | Bucket-based arcset persistence/materialization | overwrite, versioned |
| Commitment Backend | Stateless primitive authentication | KZG, IPA |
| Application Layout | Product data model built above semantic layer | flattened UnixFS-style bucket layout |

Under this terminology:

- `list` and `map` are semantic abstractions.
- ArcTable is materialization and lookup state used by those semantics.
- Commitment backends authenticate semantic-layer arcset/cell/node representations.
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
UnixFS-like file and directory semantics in `core/layout/unixfs`.

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

- `core/layout/unixfs` is a library/prototype layer, not the daemon or CLI path.
- It depends directly on `mapping.Semantics`, `list.Semantics`, and `cas.Client`.
- It does not route through current `core/graph`, `core/writer`, or
  `core/resolver`.
- Graph-level node/arc modeling, resolver integration, and runtime exposure are
  explicit TODO items rather than settled implementation details.

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
2. it returns a result plus proof or receipt
3. the client verifies against the relevant root
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
  - Bucket-based arcset persistence/materialization
- `core/layout/unixfs`
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
- `core/bucketpath` and `core/manifest`
  - current bucket/file layout helpers; candidates to move under a dedicated
    UnixFS or bucket layout package

## Config

Current operator flow:

1. run `malt init`
2. create `~/.malt/malt.json`
3. choose a local state root
4. run `malt daemon`
5. optionally set `client.default_bucket_id` or use `malt bucket default`

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
- `client.default_bucket_id`

Current defaults:

- daemon listen: `127.0.0.1:4317`
- embedded mock CAS listen: `127.0.0.1:4318`
- structure backend: `kzg`
- ArcTable type: `versioned`

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
|   |-- arctable/     # Bucket-based arcset persistence/materialization
|   |-- bucketpath/   # current bucket path boundary helper
|   |-- cas/          # CAS clients and adapters
|   |-- codec/        # MALT CID codecs and CID utilities
|   |-- commitment/   # primitive commitment backends
|   |-- graph/        # current runtime metadata/composition
|   |-- kvstore/      # KV backends
|   |-- lineage/      # auxiliary version-history metadata
|   |-- manifest/     # current directory-manifest helper
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
