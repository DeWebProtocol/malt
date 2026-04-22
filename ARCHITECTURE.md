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

## Architectural Center

The codebase should be read around these first-class concepts:

- `Semantic Layer`
  - the architectural center of MALT
  - exposes `list` and `map` as the public structure semantics
  - compiles those semantics into internal arcs and authenticated positions
- `Commitment Backend`
  - primitive authentication backend over already-positioned slots or nodes
  - examples: `KZG`, `IPA`
- `EAT`
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
├── cmd/
│   ├── gateway/main.go
│   └── malt/
├── config/
├── gateway/
├── core/
│   ├── api/          # Node: top-level component wiring
│   ├── cas/          # CAS clients and adapters
│   ├── codec/        # MALT CID codecs and CID utilities
│   ├── commitment/   # Primitive commitment backends
│   ├── eat/          # Explicit Arc Table implementations
│   ├── graph/        # Graph metadata and runtime composition
│   ├── kvstore/      # KV backends
│   ├── lineage/      # Version lineage metadata
│   ├── replication/  # Secondary snapshot/sync tooling
│   ├── resolver/     # Resolution loop and step executors
│   ├── structure/    # Public structural semantics (`list`, `map`)
│   ├── types/        # Arc sets, evidence, proof-related types
│   └── writer/       # Write-side structure update flow
├── eval/
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
- calls into `EAT` for arc access
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

Backend choice is workload-sensitive and layout-sensitive:

- vector-like or indexed list layouts may prefer `IPA`
- sparse or layer-composed structures may prefer `KZG`

## EAT

`EAT` is the explicit arc materialization and lookup layer.

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

- `EAT` is performance state, not the trust root
- incorrect lookup results are rejected by proof verification
- in the current implementation, graph-scoped state is realized as bucket-scoped state, with one bucket per graph

## Resolution and Verification

`resolver.Resolver` runs the read loop and returns:

- the resolved target
- a transcript of step evidence

There are two step kinds:

- explicit step
  - uses `EAT` lookup plus semantic-layer proof generation over the selected binding
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

1. read the current binding from `EAT`
2. obtain the arc-set view or snapshot
3. ask the semantic layer to commit or update the structure root
4. let the semantic layer use the primitive commitment backend to authenticate the affected positions
5. write the resulting arc state back through `EAT`
6. optionally record lineage metadata

This is the main operational realization of MALT's locality claim.

## Main Runtime Objects

### `api.Node`

`api.Node` is the top-level wiring point for shared infrastructure:

- KV store
- EAT implementation
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
2. `writer.Writer` reads the current arc binding from `EAT`
3. `writer.Writer` obtains a snapshot or view of the current arc set
4. the semantic layer computes a new structure root over the updated semantic state
5. the semantic layer uses the commitment backend to authenticate the updated positions
6. `EAT` is updated to reflect the resulting structure state
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

- hot-path state: `EAT` records plus the materialized semantic/commitment state needed for low-latency proof generation
- cold-path state: snapshots, lineage metadata, and replicated copies used for recovery or availability

Correctness remains cryptographic because clients verify returned evidence locally.
Operational components affect latency and availability, not the semantic trust base.

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
- `commitment/ipa`

These are primitive backends, not public semantic contracts.

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
