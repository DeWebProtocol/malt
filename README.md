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

Graph reads use the standard verifier-facing shape:

```text
Read(root, query) -> result + ProofList
VerifyRead(root, query, result, ProofList) -> valid / invalid
```

A structure root exposes authenticated read/write semantics, but those
semantics are owned by `list` and `map`. `graph` is the abstraction
boundary that combines the resolver read port and writer mutation port around a
namespace-scoped runtime. Layouts translate source-domain changes into
`writer.SemanticMutation` values; the server executes those mutations through
the writer port and returns an operational receipt. Root publication,
freshness, and multi-writer arbitration are application or deployment policy,
not the writer correctness interface. In the HTTP deployment, successful
default blob and directory `GET /{root}/{path}` reads carry `ProofList`
metadata in response headers; large-file range reads return selected bytes plus
path/`@payload` proof and a measured-list range `ProofList` step that composes
fixed chunk metadata proof with the required index proofs. Response-body range
binding is still a ProofList-schema TODO.

Current core semantics are:

- `list`
  - describes complex graph nodes with ordered/indexed child references
  - query: index, plus layout-level range reads over index intervals
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

The current prototype exposes a small primary runtime CLI named `malt`.
Evaluation-oriented workloads are grouped under one `malt-eval` binary with
subcommands.

Current runtime shape:

- `malt init`
  - creates the local configuration file and state-root directory
- `malt daemon`
  - long-running local process
  - owns hot proving/index state
- `malt add`
  - uploads payload directly to CAS
  - commits file and directory structure through MALT list/map semantics
  - writes under `--root` when extending an existing root, or creates a new
    root when omitted
- `malt resolve`
  - resolves a root-relative path and returns `target + ProofList` by default
- `malt verify`
  - verifies a ProofList, including the ProofList emitted by `malt resolve`
- `malt-eval read`
  - read benchmark driver for `maltflat`, IPLD UnixFS, and IPLD UnixFS+HAMT
    baselines
  - emits one JSONL record per measured operation; each line follows
    `cmd/eval/schemas/readbench-result.schema.json`
- `malt-eval write`
  - Git trace write-amplification replay driver
- `malt-eval run`
  - evaluation framework runner for JSON plans
  - writes raw suite envelopes, `manifest.json`, logs, and summary CSVs
- `malt-eval schema`
  - lists or prints embedded evaluator JSON schemas
- `malt-eval summarize`
  - regenerates figure CSVs from a framework run directory
- `malt-eval metrics`
  - daemon evaluation metrics client
- daemon API
  - root-centric HTTP/JSON surface rooted at `/`
- embedded mock CAS
  - optional same-process second-port service
  - fixed Kubo-compatible API at `/api/v0`

The CLI is intentionally root-centric. Lower-level compatibility and benchmark
modules may remain internally, but they are not exposed as primary `malt`
subcommands.

## HTTP Read Proof Metadata

Default successful `GET /{root}/{path}` responses include verifier-facing proof
metadata in these response headers:

```text
X-Malt-ProofList: <base64url(JSON ProofList)>
X-Malt-ProofList-Encoding: base64url-json
Vary: X-Malt-Proof
```

The proof header is generated for file bytes, directory JSON responses, and
byte-range reads. For list-backed file ranges, the current `ProofList` includes
path/`@payload` proof plus a measured-list range step carrying authenticated
fixed chunk metadata, the covered segment CID list, and the composed index
proofs. Clients that only need content bytes can opt out of default proof
generation with either
`?proof=false` or request header
`X-Malt-Proof: omit`; the `Vary` response header advertises the header-based
variance to shared HTTP caches.

`HEAD /{root}/{path}` remains a stat-only operation and returns `X-Malt-Kind`,
`X-Malt-Storage-Kind`, `X-Malt-Key`, optional `X-Malt-Payload`, and optional
`Content-Length` without generating proof headers.

Path resolution is a separate prefixed API: `GET /resolve/{root}/{path}` returns
a JSON `ResolveResponse` with `target` and, by default, `prooflist`; clients can
opt out with `?proof=false` or `X-Malt-Proof: omit`. The content route no longer
uses `format=resolve` or `format=proof` query modes.

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
| Graph Ports | Runtime read/write boundary over graph state | resolver, writer |
| Server API | Deployment surface for graph ports | daemon HTTP API |
| Application Layout | Product data model built above semantic layer | MALT UnixFS-style root layout |

Under this terminology:

- `list` and `map` are semantic abstractions.
- ArcTable is materialization and lookup state used by those semantics.
- Commitment backends authenticate semantic-layer arcset/cell/node representations.
- Layouts translate source-domain data into MALT semantic mutations.
- `graph` exposes resolver and writer ports. The resolver is the read
  path; the writer is the mutation path.
- Server routes execute resolver/writer ports. Client-side layout code composes
  source data into writer mutations.
- Resolver and writer code paths are ports over the semantic layer, not the
  semantic definition of MALT itself.
- Graph manager metadata tracks lifecycle and backend compatibility only; it
  does not store or publish a current root. The daemon currently creates an ad
  hoc default `Graph` and does not expose GraphManager lifecycle APIs as the
  public runtime path.

## Map Semantic

The map semantic describes authenticated key-to-CID or key-to-node relations.

Native operations:

- read one key and return the target binding plus proof
- verify a key binding under a root
- insert, replace, or delete a binding to produce a new root
- reserve `@payload` as the terminal materialization binding for every map
  semantic object

The current default implementation is `runtime/semantic/mapping/radix`, a
digest-keyed radix map over an index commitment backend. The older indexed map
baseline now lives under `cmd/eval/internal/baseline/indexedmap` for evaluation
comparison. It is not a production map semantic wired by `graph`.

The explicit path resolver should be understood as a compatibility layer above
map reads. It may implement longest-prefix path matching, but map itself
owns exact key lookup and proof generation.

## List Semantic

The list semantic describes authenticated stable-indexed sequences inside
complex graph nodes.

Native operations:

- read an index and return keys plus proof
- verify an index query under a root
- optionally prove a byte range for measured fixed-width lists by returning
  authenticated metadata plus the minimum covered segment CID list
- append a key
- replace an existing key
- truncate the sequence

List does not have path-resolution semantics. File reads use an application
layout that maps byte ranges to list index ranges, then calls list semantics.

The current implementation is `runtime/semantic/list/tree`, a tree-shaped
stable-indexed layout over the same primitive commitment interface.

## MALT UnixFS-Style Layout

The current prototype includes a first pure MALT structure version of
UnixFS-like file and directory semantics in `layout/unixfs`.

- directories and files are committed as map roots
- directory entries are map bindings from path segment to child root
- file `@payload` bindings point to a raw CAS CID for small files
- large file `@payload` bindings point to a list root of chunk CIDs
- payload and chunks remain CAS CIDs
- path lookup composes map reads
- file range reads map byte ranges to list index reads

`malt add --target malt --model unixfs` currently accepts `--layout flat` and
`--layout hierarchical`, with `flat` as the default. Both names currently use
the same staged hybrid materialization path: ordinary directories are
materialized as map roots, and directory/root maps also include descendant
full-path bindings for longest-prefix reads. A future implementation split can
separate pure root-map `flat` behavior from pure per-directory
`hierarchical` behavior for evaluation.

This layout gives a clean benchmark target:

- pure MALT structure UnixFS
- IPLD UnixFS/HAMT baseline

The comparison should measure path lookup, range read, chunk update, directory
mutation, proof size, and write amplification.

Current boundary:

- `layout/unixfs` remains the direct map/list/CAS library layer for the
  UnixFS-style layout and translates source-domain file/directory data into
  `writer.SemanticMutation` values.
- `POST /{root}/_mutate` is the writer mutation route. The root-centric daemon
  also exposes `POST /{root}/{path}` as a UnixFS layout convenience: it stages a
  file or directory operation, converts the resulting layout state into a
  semantic mutation, and then uses the same writer route semantics as `_mutate`.
- The public CLI currently exposes write ingestion through `malt add`; reads
  are available through the daemon API and proof-bearing resolve/content
  endpoints.
- The layout still depends directly on `mapping.Semantics`, `list.Semantics`,
  and CAS access; it does not make `graph`, `graph/writer`, or
  `graph/resolver` the semantic owners.
- Graph-level node/arc terminology, paper-facing formalization of writer
  semantic mutations and `ProofList` schemas, write receipt semantics, and
  benchmark-facing proof reporting remain proposal-stage MIPs in the sibling
  documents repository.

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
- attach resulting roots into the selected root layout

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

## Implementation Components

- `auth/semantic/mapping`
  - public map semantic abstraction and shared map types
- `runtime/semantic/mapping/radix`
  - primary map implementation
- `cmd/eval/internal/baseline/indexedmap`
  - baseline comparison map implementation for evaluator-only use
- `auth/semantic/list`
  - public list semantic abstraction and shared list types
- `runtime/semantic/list/tree`
  - primary list implementation
- `auth/commitment`
  - stateless primitive commitment backends
- `runtime/arctable`
  - namespace-scoped arcset persistence/materialization
- `runtime/arctable/bloom`
  - optional ArcTable negative-lookup optimization hook, disabled unless an
    ArcTable is constructed with a BloomCache
- `layout/unixfs`
  - current pure MALT UnixFS-style layout prototype over map/list semantics
- `graph/resolver`
  - resolver read port and explicit proof path
- `graph/writer`
  - writer mutation port and executor
- `graph`
  - graph abstraction contracts
- `runtime/graph`
  - concrete graph runtime composition around resolver and writer executors
- `graph/querypath`
  - current query-path canonicalization helper for root-relative paths
- `layout/unixfs/manifest`
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
|-- cmd/
|   |-- internal/
|   |   `-- merkledagimport/ # command-local Merkle-DAG UnixFS import helpers
|   |-- eval/
|   |   |-- command/
|   |   |-- helper/
|   |   |-- schemas/
|   |   |-- malt-eval/
|   |   `-- ...
|   `-- malt/
|-- config/
|-- daemon/
|-- api/http/         # shared daemon request/response payload types
|-- auth/             # data-authentication core
|   |-- arcset/       # canonical arcs and coordinates
|   |-- commitment/   # primitive commitment backends
|   |-- proof/        # evidence and ProofList artifacts
|   `-- semantic/     # list/map semantic interfaces
|-- graph/            # resolver/writer graph contracts
|   |-- querypath/    # root-relative query path canonicalization
|   |-- resolver/     # read/proof port and explicit proof path
|   `-- writer/       # mutation port and executor
|-- layout/
|   `-- unixfs/       # map/list-based UnixFS layout prototype
|-- runtime/          # node, graph composition, ArcTable, metrics, and semantic implementations
|-- sdk/
|   `-- client/       # Go daemon client facade
|-- server/          # daemon HTTP server
|-- storage/          # CAS and KV storage libraries
|-- wire/
|   `-- maltcid/      # MALT map/list root CID codecs
`-- logger/
```

## More Detail

For implementation structure and code-level control flow, see
[`ARCHITECTURE.md`](./ARCHITECTURE.md).
