# MALT

MALT is an authenticated structure layer over immutable content-addressed storage.

Its purpose is to separate structure from payload:

- payload remains ordinary content-addressed data
- structure is represented explicitly as arcs
- a structure root commits those arcs independently from payload
- structural change advances the structure root without recursively rewriting unrelated payload

## What MALT Changes

Traditional Merkle-DAG traversal commits structure implicitly in parent content.
That makes structural evolution ancestor-dependent: changing a relation often forces rootward rewrite.

MALT changes that model:

- structure is committed explicitly
- updates are expressed as changes to explicit arcs
- the system maintains a new structure root plus local proof/index maintenance
- unrelated payload blocks do not need to be rewritten

The claim is not that MALT is free. The claim is that it replaces propagation-heavy structural rewrite with localized, verifiable structure maintenance.

## Runtime Shape

The repository converges toward a single binary named `malt`.

Current runtime shape:

- `malt daemon`
  - long-running local process
  - owns hot proving/index state
- `malt bucket`
  - manages managed buckets and the client-side default bucket
- `malt add`
  - client-side workflow that uploads payload directly to CAS
  - then attaches resulting `path -> CID` bindings into a bucket through the daemon
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

`cmd/gateway` should therefore be read only as a debug/evaluation alias for the
same daemon server package, not as the primary product boundary.

## Data Model

MALT logically separates:

- payload content
- explicit outgoing structure

Payload stays in ordinary CAS/IPLD blocks.
Structure is committed independently as a structure root.

A structure root is CID-compatible, but it is not semantically the same thing as a payload CID:

- payload CID
  - identifies immutable content
- structure root
  - identifies a committed explicit arc set

This distinction is central to the design.

In the current bucket-first runtime, a managed bucket head is always a
directory-shaped `map` root. Large files are represented by `list` roots inside
that directory structure, but a `list` root is not itself a valid bucket head.
Every MALT-native `map` root must carry a reserved `@payload` binding; empty
payloads should still use a defined empty-block CID rather than omitting the
binding.

## Terminology and Layers

The preferred abstraction is to expose **structural semantics** publicly and keep
layout/backend choices internal to each semantic implementation.

| Layer | Question | Examples |
| --- | --- | --- |
| Structural Semantics | What logical structure does the application expose? | `list`, `map` |
| Semantic Implementation | How is that semantic contract realized internally? | tree-shaped indexed list, digest-keyed radix map, future variants such as segment-radix or hashed-path radix |
| Commitment Backend | What primitive authenticates already-positioned values or nodes? | KZG, IPA, hash/Merkle commitments |

Under this terminology:

- `list`
  - a stable indexed structure
  - native operations are index-based proof and index-stable replacement
  - length must be committed as part of the structure state
  - insert/delete are not primitive operations; they can be implemented above the semantic layer as expensive shift-and-rewrite sequences
- `map`
  - a keyed structure
  - implementations choose how keys are placed into authenticated positions and how conflicts are handled
- fixed-slot commitment primitive
  - a backend that only authenticates already-positioned values
  - `KZG` and `IPA` belong here
  - it does not define public structure semantics or a general `key -> position` rule

Primitive commitment backends now live under `core/commitment`, while public
structure semantics live under `core/structure`. Some call paths still share a
common commitment interface for engineering convenience, but the semantic
boundary is explicit in the current codebase: `list` and `map` are the public
structural contracts, and primitive backends remain internal dependencies of
their implementations.

The current write-up may discuss `list` first because it is the simpler semantic
and the current implementation is farther along there. That is only an
exposition choice. `map` remains part of the public semantic layer.

In the current prototype, hot proof/index state is typically organized in deployment-specific namespaces for performance.
The current code often maps one namespace to one graph, but that state placement is an implementation choice, not a semantic requirement of the abstraction.

## Native Resolution

Native MALT resolution works over explicit arcs:

1. look up the relevant arc in `EAT`
2. obtain a root-scoped arc view
3. generate a proof using the semantic layer and primitive commitment backend
4. return a transcript that the client can verify locally

The explicit path is the primary path.

Current terminal behavior is intentionally asymmetric:

- bare `map` roots materialize through the mandatory `@payload` binding
- `list` roots are terminal typed keys and do not auto-redirect to `@payload`
- bucket-path misses return `not found` rather than silently returning the
  current root

## CAS Boundary

MALT is not primarily a payload-upload proxy.

Recommended boundary:

- immutable payload publication is primarily a client-to-CAS operation
- MALT primarily owns structure operations, proof generation, and authenticated resolution
- MALT still depends on CAS on the read path, including bare-root `@payload` materialization and legacy CID traversal

`malt cas ...` commands are still useful convenience tooling, but they should
not be mistaken for the conceptual center of the system.

`malt add ...` is therefore best understood as a client-side orchestration convenience:

- payload publication still goes directly to CAS
- structure attachment still goes through the daemon API
- large files are chunked client-side and committed as `list` roots
- directories are materialized as bucket-local `map` roots whose bindings
  include `@payload`, direct children, and flattened descendants
- the command only hides that two-step interaction behind one local workflow

## Interoperability

MALT roots are encoded as CID-compatible identifiers.
That means MALT-native structures and ordinary IPLD/CAS objects can reference each other.

As a result, the resolver can cross between:

- explicit MALT structure traversal
- ordinary IPLD/Merkle traversal

This mixed traversal is an interoperability feature.
It is not the primary definition of MALT.

## Verification

MALT uses transcript-based stepwise verification:

1. an untrusted resolver executes the lookup
2. it returns per-step evidence
3. the client verifies each step locally
4. the lookup is valid only if every step verifies

This keeps correctness cryptographic even when lookup/index infrastructure is not trusted.

## Core Components

- `Graph`
  - runtime composition unit and main read/write entry point
  - the authentication boundary remains the structure root, not the graph object
- `Structure`
  - preferred semantic layer for public `list` and `map` contracts
  - now lives under `core/structure`
  - commitment backends now live under `core/commitment`
- `EAT`
  - explicit arc materialization and lookup state
- `Commitment Backend`
  - stateless primitive commitment/proof engine over caller-supplied indexed views
- `Writer`
  - advances structure roots through localized arc updates
- `Resolver`
  - resolves from a structure root and returns a verifiable transcript
- `Lineage`
  - optional version metadata for ancestry and history operations

## Config

Current operator flow:

1. run `malt init`
2. create `~/.malt/malt.json`
3. choose a local state root
4. run `malt daemon`
5. optionally set `client.default_bucket_id` or use `malt bucket default`

Important split:

- config location should be stable
- state-root placement should remain user-configurable

Current schema:

- `rpc.listen`
- `state.root_dir`
- `state.kvstore`
- `state.eat`
- `state.lineage`
- `structure.default_backend`
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
- EAT type: `versioned`

## Repo Layout

```text
malt/
├── client/          # thin daemon HTTP client
├── cmd/
│   ├── gateway/main.go
│   └── malt/
├── config/
├── httpapi/         # shared daemon request/response payload types
├── core/
│   ├── api/          # top-level wiring via Node
│   ├── cas/          # CAS clients and adapters
│   ├── codec/        # MALT CID codecs and CID utilities
│   ├── eat/          # explicit arc table implementations
│   ├── graph/        # graph metadata and runtime composition
│   ├── kvstore/      # KV backends
│   ├── lineage/      # version lineage metadata
│   ├── replication/  # secondary snapshot/sync tooling
│   ├── resolver/     # resolution loop and step executors
│   ├── commitment/   # primitive commitment backends
│   ├── structure/    # public structural semantics (`list`, `map`)
│   ├── types/        # arc sets, evidence, proof-related types
│   └── writer/       # write-side structure update flow
├── eval/
├── gateway/
├── server/          # daemon HTTP server
└── integration/
```

## What Is Secondary

These parts of the repo are useful, but they are not the conceptual center of MALT:

- gateway deployment
- compatibility traversal machinery in the resolver/gateway path
- replication and snapshot tooling
- benchmark scaffolding
- helper deployment abstractions
- layout/backend comparisons such as radix, KZG, and IPA

## Current Structure Layer

The current public structure layer should be read as:

- `core/structure/list`
  - public stable-indexed list semantic
  - primary implementation is a tree-shaped indexed layout backed by a fixed-slot primitive such as `IPA` or `KZG`
  - small lists are shallow instances of the same runtime rather than a separate public `indexed` semantic
- `core/structure/mapping`
  - public keyed semantic
  - current default implementation is a digest-keyed radix runtime
- commitment backends
  - internal dependencies of structure implementations rather than the primary API surface
  - they authenticate positioned slots or nodes; they do not define public `list` / `map` semantics

## More Detail

For implementation structure and code-level control flow, see [`ARCHITECTURE.md`](./ARCHITECTURE.md).
