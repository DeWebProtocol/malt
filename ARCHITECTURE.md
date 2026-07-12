# MALT Architecture

## Overview

MALT is a general graph data-authentication system. Its authentication
granularity is a typed arc rather than a storage block. Immutable payload bytes
remain in content-addressed storage (CAS), while vector-commitment (VC) backends
commit to and prove map/list relations under typed MALT roots.

The trusted MALT kernel consists of canonical arc and root rules, commitment
verification, semantic proof rules, and portable ProofList verification.
ArcTable, concrete semantic implementations used to generate proofs, caches,
storage adapters, daemons, and gateways form an untrusted execution engine.
They may accelerate reads and mutations, but they are not correctness
authorities. UnixFS is one application layout over this model.

This repository packages that core as a specification implementation with a
reference runtime and evaluation gateway. Production managed-gateway behavior
such as tenancy, identity, authorization, S3 backend orchestration, cache
policy, quota, billing, pinning, garbage collection, and operations belongs in
the separate `DeWebProtocol/gateway` service repository or private deployment
overlays.

```text
Read(ReadRequest{Root, Query}) -> ReadResult{Target, Segments, ProofList}
VerifyRead(request, result) -> valid / invalid

Apply(semantic mutation with base root) -> result root + write receipt
```

List and map are semantic abstractions:

- list semantic: authenticated structure with stable-indexed or range-addressed
  child references
- map semantic: authenticated keyed/path-like relations from semantic objects
  to target CIDs

The module-root `package malt` is the typed application-neutral facade.
`Engine.Read`, `Engine.Apply`, and `Engine.VerifyRead` compose execution-plane
implementations while hiding runtime scope from canonical requests. The engine
is still untrusted: `VerifyRead` binds a caller-supplied trusted root and typed
query to the returned target, optional range segments, and ProofList before the
portable `auth/verifier` checks the evidence.

`malt.SegmentPath` is the application-neutral composition coordinate. Clients
send segment arrays; the reference resolver may consume multiple leading
segments with one authenticated arc and currently prefers the longest prefix at
each root. The proof contract is existential: it authenticates the returned
complete derivation, not the uniqueness or maximality of the selected path.
Applications and layouts own overlap/conflict policy.

The unversioned `artifact` package projects resolve, primitive prove, and
verify operations into the explicit `malt.artifact/v0alpha2` envelope. This is
the cross-process boundary for gateway, daemon, and SDK integrations. Profile
versions belong in serialized artifacts and schemas, not Go package names.

ArcTable is namespace-scoped arcset persistence/materialization and provides no
correctness by itself. `graph` is a runtime composition boundary around
resolver and writer ports, not a semantic owner or graph-node hierarchy.
Layouts translate domain operations into typed arc queries and semantic
mutations. Server routes execute adapters over these contracts.

This document is implementation-oriented. For the shorter system overview, see
[`README.md`](./README.md).

## Core Model

The implementation separates the trusted authentication kernel from reusable
but untrusted execution and application layers:

| Layer | Responsibility |
| --- | --- |
| Portable auth kernel | Canonical arcs, typed roots, VC verification, semantic proof rules, and ProofList validation |
| Root `malt` facade | Typed primitive queries, immutable segment paths, and `Engine.Read`/`Apply`/`VerifyRead` |
| `artifact` contract | Profiled resolve/prove/verify request, result, ProofList binding, and schemas |
| Execution engine | Proof generation, mutation application, operational scope, ArcTable, indexes, and caches |
| Runtime adapters | Resolver/writer ports, reference daemon, HTTP transport, SDK, and storage wiring |
| Application layout | Domain model over typed arcs and CAS payloads; UnixFS is one layout |

The public core facade exposes typed semantic operations and MALT segment paths,
not storage machinery, JavaScript/filesystem syntax, or a UnixFS path API.

### Semantic Layer

The semantic layer defines the shape of authenticated operations. It should be
generic enough for list, map, and future semantics, but narrow enough that it
does not erase their differences.

Conceptually, graph reads return `result + ProofList`, where the ProofList is
the vector-commitment proof chain from root to destination. Root-centric writer
mutation accepts semantic mutations that have already been produced by a layout
and returns an operational receipt without publishing a managed head. The HTTP
server in this repository returns default `GET /{root}/{path}` file and directory reads with
`ProofList` metadata in `X-Malt-ProofList` response
headers encoded as `base64url-json`. Clients can opt out of default proof
generation with `?proof=false` or `X-Malt-Proof: omit`; default GET responses
advertise that request-header variance with `Vary: X-Malt-Proof`. Large-file
byte-range reads include path/`@payload` proof plus one measured-list
`list_range` step. That step carries authenticated fixed chunk metadata, the
covered segment CIDs, and a proof payload composed from the metadata slot proof
and the required index proofs. Portable ProofList verification authenticates
that metadata and those segment CIDs; UnixFS callers bind the returned HTTP
body with `layout/unixfs.VerifyRangeBody` or an equivalent segment-byte check.
File routes are reference/evaluation surfaces around the same root-centric
mutation namespace. They are not a production multi-tenant gateway contract.

### List Semantic

The list semantic authenticates stable-indexed child references inside
authenticated list structure.

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
- optional reserved `@payload` binding when a layout associates payload
  materialization with the semantic object

The current public package is `auth/semantic/mapping`.
The primary implementation is `runtime/semantic/mapping/radix`.
The older indexed map baseline lives under
`cmd/eval/internal/baseline/indexedmap` as an evaluation comparison
implementation. It is not a production map semantic wired by `graph`,
which constructs `mapping/radix`.

The current explicit resolver is best understood as a compatibility layer above
map reads. It can implement longest-prefix path policy, but the map semantic
owns exact key proof generation and verification.

`@payload` is the standard reserved terminal materialization coordinate. The
coordinate is available to every layout, but generic maps are valid without
it. UnixFS requires it for its file and directory objects. List semantic
objects do not implicitly redirect through `@payload`.

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
- `auth/verifier`
  - portable ProofList verification without runtime, ArcTable, CAS, layout,
    server, or daemon dependencies
- module-root `package malt`
  - typed `Query`, `ReadRequest`, `ReadResult`, and
    `Engine.Read`/`Apply`/`VerifyRead` facade
- `layout/unixfs`
  - current pure MALT UnixFS-style layout prototype built directly over
    `mapping.Semantics`, `list.Semantics`, and CAS-backed payload storage
- `graph/resolver`
  - resolver read port and explicit proof path
- `graph/writer`
  - writer mutation model and executor
- `graph`
  - resolver/writer port contracts around graph-shaped semantic state
- `runtime/graph`
  - concrete graph runtime composition around resolver and writer executors
- `graph/querypath`
  - current query-path canonicalization helper for root-relative paths
- `layout/unixfs/internal/manifest`
  - current UnixFS directory-manifest helper above the semantic layer

## Runtime Packaging

The repository shape is a small primary reference runtime binary named `malt`,
plus one evaluation binary named `malt-eval` with workload-specific
subcommands. The `malt` daemon and `server` package are retained as a
reference/evaluation gateway so end-to-end core behavior can be exercised
without pulling product tenancy and deployment policy into MALT core.

The managed service gateway is separate. It should depend on MALT core
contracts and return verifier-facing `result + ProofList` responses, but it
owns product-layer policy such as tenants, identity, authorization, backend
credentials, cache policy, root publication, quota, and operations.

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
- every UnixFS file or directory map carries the reserved `@payload` binding as
  a layout invariant; generic MALT maps may omit it
- the reference compatibility `Resolve` path materializes map roots through
  `@payload`; generic maps that omit it use `ResolveKey` or typed `Engine.Read`
- list roots are terminal typed keys and do not auto-redirect through
  `@payload`
- root-relative path misses are reported as `not found`

Path-miss and payload-materialization behavior are file-layout and
product-runtime invariants. Generic map semantics only reserve the `@payload`
coordinate; they do not require it.

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
|-- server/          # reference daemon and eval-gateway HTTP server
|-- auth/             # data-authentication core
|   |-- arcset/       # canonical arcs and coordinates
|   |-- commitment/   # primitive commitment backends
|   |-- proof/        # evidence and ProofList artifacts
|   `-- semantic/     # list/map semantic interfaces
|-- graph/            # thin resolver/writer port surface
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

- `Commitment`
- `Commit`
- `Prove`
- `Verify`
- `Update`

This already approximates the target map semantic shape:

- `Prove` is the map read path for exact keys
- `Verify` validates read proof
- `Update` is the map write path
- `Commit` bootstraps a root from a materialized view
- `Commitment` exposes stateless single-step commitment primitives for
  storage-free proof and verification

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

- `Commitment`
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
- `Commitment` exposes stateless single-step commitment primitives for
  storage-free proof and verification

The primary implementation, `list/tree`, uses a tree-shaped fixed-slot layout.
It owns:

- committed length/header state
- index-to-slot layout
- node materialization through ArcTable
- commitment proof construction
- root update computation

List should not be forced through path-based resolution.

## Runtime Graph, Resolver, Writer, and Layouts

The old architecture treated `graph` as a broad contract layer. The current
interpretation is narrower: list/map semantics own authenticated operations,
`graph/resolver` owns read traversal and `ProofList` assembly, `graph/writer`
owns semantic mutation application, and `runtime/graph` only composes those
ports with concrete semantic implementations.

- layouts translate source-domain data into MALT semantic mutations
- the writer port applies converted semantic mutations through list/map
  semantic operations and returns a write receipt
- resolver reads return `result + ProofList`
- resolver adapters translate application or compatibility reads into semantic
  reads and proof chains
- neither adapter owns the semantic definition of map or list
- the root `graph` package should remain a small port/composition surface, not
  a second list/map node abstraction

The explicit resolver is a map compatibility adapter:

1. apply path policy, such as longest-prefix matching
2. call map semantic proof generation for the selected exact key
3. wrap the map proof as resolver evidence

The current concrete `writer.Writer` is the mutation boundary. It accepts
layout-produced semantic mutations, applies map/list deltas, and returns an
operational receipt. It does not publish an authoritative head; applications
decide which resulting root becomes current.

`ProofList` is the standard verifier-facing read artifact. It covers map
step proofs, terminal `@payload` proofs, list index proofs, measured-list
`list_range` evidence for range reads, and blob target binding proofs from the
queried root to the destination. Current `list_range` steps carry fixed chunk
metadata, covered segment CIDs, and metadata/index proof payload. The portable
`auth/verifier` checks proof structure and evidence without runtime lookup;
`layout/unixfs.VerifyRangeBody` separately binds authenticated range segments
to returned body bytes.

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

- The package remains the direct UnixFS-style layout library over map/list
  semantics and CAS-backed payload storage, translating source-domain
  file/directory data into MALT semantic mutations.
- `POST /{root}/_mutate` is the writer mutation route. Root-centric daemon
  routes also expose `POST /{root}/{path}` as a UnixFS layout convenience: it
  stages a file or directory operation, converts the resulting layout state into
  a semantic mutation, and then uses the same writer mutation path as
  `_mutate`.
- The public CLI exposes ingestion through `malt add`; reads are available
  through the daemon API and proof-bearing resolve/content endpoints.
- The package injects `mapping.Semantics`, `list.Semantics`, and narrow CAS
  reader/writer capabilities. `NewReader` grants no payload write capability;
  `NewWriter` receives read and write capabilities explicitly.
- Current `graph`, `graph/writer`, and `graph/resolver` remain runtime
  composition and compatibility adapters rather than semantic owners.
- The current writer receipt, artifact, and `ProofList` reference docs live
  under `docs/spec/`. Proposal-stage MIPs in `docs/mips/` track acceptance,
  remaining open decisions, and follow-up implementation planning rather than
  serving as the only schema copy.
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

- decide whether writer receipts in `docs/spec/writer-receipts.md` become a
  stable API and evaluation accounting contract
- expand `malt.artifact/v0alpha2` with cross-language map/list/range
  conformance vectors while keeping incompatible profiles explicit
- keep gateway, daemon, and SDK projections aligned with the checked-in
  resolve/prove/verify schemas
- decide whether list needs a future variable-size or compact range-proof API;
  the current prototype uses path/`@payload` proof plus one measured-list
  `list_range` step carrying metadata, segment CIDs, and metadata/index proofs
- stabilize benchmark-facing proof, receipt, and metrics reporting for paper
  figures

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
    "cors_allowed_origins": []
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
  routes, `POST /verify`, and UnixFS browser write routes such as
  `POST /_unixfs?path=...` and `POST /{root}/{path...}`. Empty or omitted
  disables browser CORS, which is the default written by `malt init`. Exact
  origins are matched literally. Port wildcards are only supported for loopback
  origins such as `http://localhost:*`, `http://127.0.0.1:*`, and
  `http://[::1]:*`; the daemon still replies with the concrete request origin.
  Admin and semantic-mutation routes are not exposed through the browser CORS
  surface. The daemon reads this policy at startup. Change the file and restart
  the managed daemon to apply browser-access changes.

This config is a packaging and runtime decision. It does not define the core
MALT semantic abstraction.
