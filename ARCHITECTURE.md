# MALT Architecture

## Overview

MALT is an authenticated structure layer over immutable content-addressed storage.

Its implementation should be read around one main separation:

- payload remains ordinary content-addressed data
- structure is made explicit as arcs
- structure is exposed through semantic abstractions such as `list` and `map`
- semantic implementations compile those abstractions into internal arcs and authenticated positions
- primitive commitment backends authenticate those positions independently from payload

MALT roots are encoded as CID-compatible identifiers, so MALT-native structures and ordinary IPLD/CAS objects can reference each other. That interoperability matters, but it is not the conceptual center of the system.

This document is implementation-oriented. For the shorter system overview, see [`README.md`](./README.md).

In the current prototype, hot proving/index state is colocated and organized in deployment-specific namespaces for performance. The current code often maps one namespace to one graph, but that placement is an implementation choice, not the semantic definition of MALT.

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
  - client-side orchestration for file and directory ingestion
  - uploads payload blocks to CAS
  - then attaches the resulting bindings into a bucket through the daemon API
- `malt cat ...` and `malt get ...`
  - product read paths for bucket-local files and directories
- `malt ...`
  - lower-level thin client commands for inspection, mutation, proof, verification, and lineage workflows
- `malt cas ...`
  - convenience commands for CAS-oriented workflows

Current code status:

- `cmd/malt` now provides `malt init`, `malt daemon`, bucket/add/cat/get product commands, lower-level resolve/prove/update/verify/lineage commands, and `malt cas`
- `server/` provides the daemon HTTP server
- `client/` provides the thin daemon HTTP client
- `httpapi/` holds the shared `/api/v1` request/response model
- embedded mock CAS runs on a second local port and exposes a Kubo-compatible `/api/v0`

Current runtime invariants:

- a managed bucket head must be a `map` root because bucket heads represent directory-like structure
- a `list` root represents file-content structure and is not a valid managed bucket head
- every MALT-native `map` root carries the reserved `@payload` binding
- materializing a bare `map` root means resolving `@payload` first
- `list` roots are terminal typed keys and do not auto-redirect through `@payload`
- bucket-path misses are reported as `not found`, not as a fallback to the current root

## Architectural Center

The codebase should be read around these first-class concepts:

- `Semantic Layer`
  - the architectural center of MALT
  - exposes `list` and `map` as the public structure semantics
  - compiles those semantics into internal arcs and authenticated positions
- `Commitment Backend`
  - primitive authentication backend over already-positioned slots or nodes
  - current in-tree backend: `KZG`
- `ArcTable`
  - explicit arc materialization and lookup state
- `Resolver`
  - resolves structure from a root and returns a transcript
- `Writer`
  - advances structure roots through localized arc updates
- `Graph`
  - the main runtime composition unit
  - provides the main read and write entry points
  - does not redefine the authentication boundary, which remains the structure root
- `Lineage`
  - optional version metadata for ancestry and history operations

Everything else in the repository should be interpreted relative to that core.

## Code Layout

```text
malt/
├── client/          # thin daemon HTTP client
├── cmd/
│   └── malt/
├── config/
├── httpapi/         # shared daemon API payloads
├── server/          # daemon HTTP server
├── core/
│   ├── api/          # Node: top-level component wiring
│   ├── cas/          # CAS clients and adapters
│   ├── codec/        # MALT CID codecs and CID utilities
│   ├── commitment/   # Primitive commitment backends
│   ├── arctable/          # Explicit Arc Table implementations
│   ├── graph/        # Graph metadata and runtime composition
│   ├── kvstore/      # KV backends
│   ├── lineage/      # Version lineage metadata
│   ├── resolver/     # Resolution loop and step executors
│   ├── structure/    # Public structural semantics (`list`, `map`)
│   ├── types/        # Arc sets, evidence, proof-related types
│   └── writer/       # Write-side structure update flow
└── integration/
```

## Semantic Layer

The semantic layer is where MALT attaches meaning to authenticated structure.

The public semantic contracts are:

- `list`
  - ordered structure
  - stable index semantics
  - authenticated length/header state belongs to the committed structure state itself
  - native operations are index proof and index-stable replacement
  - insert/delete are higher-level rewrites above the primitive stable-index contract
- `map`
  - keyed structure
  - native contract is key-based lookup plus authentication of the key-to-target binding
  - localized maintenance should affect one logical key binding without exposing internal placement rules

Important implementation note:

- the current write-up discusses `list` first because it is the simpler semantic and the current implementation is farther along there
- this is only an exposition and implementation-order choice
- `map` remains part of MALT's semantic-layer model and public API surface

The semantic layer owns:

- translation from semantic objects into internal arcs
- mapping from semantic coordinates into authenticated positions
- calls into `ArcTable` for arc access
- calls into primitive commitment backends for commit/prove/verify/update

## Commitment Backends

Primitive commitment backends have a narrow role:

> Authenticate already-positioned slots or nodes chosen by a semantic implementation.

They are responsible for:

- `Commit`
- `Prove`
- `Verify`
- `Update`

They are not responsible for:

- path semantics
- `list` or `map` semantics
- longest-prefix lookup
- graph traversal policy

Current design direction:

- keep primitive backends under `core/commitment`
- expose a restart-safe semantic-neutral index commitment interface
- treat caches as performance optimizations only
- let semantic packages own layout decisions, metadata/header state, and future key-placement logic

Backend choice remains workload-sensitive and layout-sensitive, but the current
in-tree product path is KZG-only.

## ArcTable

`ArcTable` is the explicit arc materialization and lookup layer.

Its role is:

- point lookup of explicit arcs
- snapshots of the committed arc set for a root
- optional version-aware retrieval

Current implementations:

- `overwrite`
  - bucket-scoped current-view storage
- `versioned`
  - delta-per-version storage linked by `@previous`

Important framing:

- `ArcTable` is performance state, not the trust root
- incorrect lookup results are rejected by proof verification
- in the current implementation, graph-scoped state is realized as bucket-scoped state, with one bucket per graph

## CAS Boundary

The intended boundary with CAS is asymmetric.

Write path:

- the client can encode immutable payload itself
- the client can compute the payload CID itself
- the client can publish payload directly to CAS

Read path:

- MALT still needs CAS to fetch immutable blocks
- this includes bare-root `@payload` materialization
- this also includes compatibility traversal into legacy CID space

This means MALT should not be framed primarily as a payload-upload proxy.
Its core role is structure management, authenticated resolution, and proof generation.

`malt add ...` does not change that boundary. It is only a client-side convenience
workflow that performs:

1. local file traversal
2. direct payload upload to CAS
3. follow-up structure attachment into a bucket through the daemon

### Mock CAS Direction

The preferred integration direction is a Kubo/IPFS-compatible HTTP surface when practical.

Why:

- it keeps the client contract closer to real CAS deployments
- switching between mock and real CAS should mostly change endpoint configuration rather than semantics

Decision rule:

- if same-process same-port embedding is non-invasive, it is acceptable
- otherwise use the same process with a second local port

### `core/cas` Scope

`core/cas` should stay close to the MALT core boundary:

- core-facing CAS interfaces
- minimal immutable-block operations needed by resolver/runtime code

Transport-specific integrations such as Kubo/IPFS HTTP adapters are better
understood as adapters around that core boundary rather than as the conceptual
center of MALT itself.

## Resolution and Verification

`resolver.Resolver` runs the read loop and returns:

- the resolved target
- a transcript of step evidence

There are two step kinds:

- explicit step
  - uses `ArcTable` lookup plus semantic-layer proof generation over the selected binding
- implicit compatibility step
  - traverses ordinary IPLD/Merkle objects through CAS

The explicit step is the native MALT path.
The implicit step exists for compatibility when traversal crosses into legacy CID space.

Clients verify the returned transcript locally:

- explicit steps verify against the structure root through the semantic layer and its commitment backend
- implicit steps verify by block hash and in-block path mapping

## Localized Write Path

The write path is implemented by `writer.Writer`.

For an arc update, the code follows this shape:

1. read the current binding from `ArcTable`
2. obtain the arc-set view or snapshot
3. ask the semantic layer to commit or update the structure root
4. let the semantic layer use the primitive commitment backend to authenticate the affected positions
5. write the resulting arc state back through `ArcTable`
6. optionally record lineage metadata

This is the main operational realization of MALT's locality claim.

## Main Runtime Objects

### `api.Node`

`api.Node` is the top-level wiring point for shared infrastructure:

- KV store
- ArcTable implementation
- CAS client
- graph manager
- optional lineage manager

It is not itself the graph abstraction. It constructs per-graph instances.

### `graph.Graph`

`Graph` is the main runtime MALT unit.

Important property:

- the graph is stateless with respect to the current root
- the root is always passed as an argument to read and write operations

This keeps structure evolution explicit and avoids embedding mutable "current root" state inside the graph object. The cryptographic boundary remains the structure root itself; `Graph` is a composition and deployment construct.

### `lineage`

`lineage` is an auxiliary metadata layer that tracks root ancestry.

It is useful for:

- history queries
- version navigation
- future version-management optimizations

It is not part of the cryptographic trust base.

## Code-Level Flow

### Commit / Update Flow

The main write-side code path is:

1. `graph.Graph` receives a root-scoped update request
2. `writer.Writer` reads the current arc binding from `ArcTable`
3. `writer.Writer` obtains a snapshot or view of the current arc set
4. the semantic layer computes a new structure root over the updated semantic state
5. the semantic layer uses the commitment backend to authenticate the updated positions
6. `ArcTable` is updated to reflect the resulting structure state
7. optional lineage metadata is recorded

### Resolve / Verify Flow

The main read-side code path is:

1. `resolver.Resolver` starts from a root and remaining path
2. if the current identifier is a MALT structure root, it uses the explicit step executor
3. if the current identifier is an ordinary CID, it uses the compatibility step executor
4. each step appends evidence to the transcript
5. the client verifies the transcript step by step

The explicit step is the native MALT mechanism.
The implicit step is the interoperability path.

## Deployment and Trust

Useful deployment framing:

- hot-path state: `ArcTable` records plus the materialized semantic/commitment state needed for low-latency proof generation
- cold-path state: snapshots, lineage metadata, and replicated copies used for recovery or availability

Correctness remains cryptographic because clients verify returned evidence locally.
Operational components affect latency and availability, not the semantic trust base.

The default product interpretation should therefore be sidecar/local-daemon
first.

## Implemented Semantics and Variants

### Semantic Layer

- `structure/mapping`
  - semantic contracts and shared keyed-map types
- `structure/mapping/radix`
  - primary keyed-map runtime semantic
  - digest-keyed radix placement with explicit collision handling
- `structure/mapping/indexed`
  - older canonical-order keyed baseline kept as a simpler comparison path
- `structure/list/tree`
  - primary stable-indexed list runtime semantic

### Commitment Backends

- `commitment/kzg`

These are primitive backends, not public semantic contracts.

## Configuration Direction

The legacy flat config and old CLI flag model have been replaced by the daemon-oriented runtime config.

The target operator flow should be:

1. run `malt init`
2. create `~/.malt/malt.json`
3. choose a local state root
4. start `malt daemon`

Important separation:

- config path is stable and discoverable
- state-root placement is user-configurable

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

Interpretation:

- the daemon has its own local HTTP endpoint and fixed `/api/v1` API surface
- mutable MALT state has an explicit root directory
- CAS can point either to an external Kubo-compatible endpoint or to an embedded mock
- the embedded mock CAS uses a separate local port and fixed `/api/v0` Kubo-compatible API

This config direction is a packaging/runtime decision and does not change the
core MALT abstraction.
