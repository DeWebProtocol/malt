# MALT Architecture

## Overview

MALT is an authenticated structure layer over immutable content-addressed storage.

Its core design is:

- payload remains ordinary content-addressed data
- structure is made explicit as arcs
- a structure root commits those arcs independently from payload
- structural change advances the structure root without recursively rewriting unrelated payload

MALT roots are encoded as CID-compatible identifiers, so MALT-native structures and ordinary IPLD/CAS objects can reference each other. That interoperability matters, but it is not the conceptual center of the system.

This document is implementation-oriented. For the shorter system overview, see [`README.md`](./README.md).

In the current prototype, hot proving/index state is colocated and organized per graph for performance.
That placement is an implementation choice, not the core semantic definition of MALT.

## Architectural Center

The codebase should be read around six first-class concepts:

- `Graph`
  - the scoped unit of structure
  - provides the main read and write entry points
- `EAT`
  - explicit arc materialization and lookup state
- `SCE`
  - stateless commitment/proof engine over caller-supplied arc views
- `Writer`
  - advances structure roots through localized arc updates
- `Resolver`
  - resolves structure from a root and returns a transcript
- `Lineage`
  - optional version metadata for ancestry and history operations

Everything else in the repository should be interpreted relative to that core.

## Code Layout

```
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
│   ├── eat/          # Explicit Arc Table implementations
│   ├── graph/        # Graph metadata and per-graph composition
│   ├── kvstore/      # KV backends
│   ├── lineage/      # Version lineage metadata
│   ├── replication/  # Secondary snapshot/sync tooling
│   ├── resolver/     # Resolution loop and step executors
│   ├── sce/          # Structure commitment engine
│   ├── structure/    # Public structural semantics (`list`, `map`)
│   ├── types/        # Arc sets, evidence, proof-related types
│   └── writer/       # Write-side structure update flow
├── eval/
└── integration/
```

## Component Responsibilities

### Structure Roots and Payload

- Payload blocks remain ordinary CAS/IPLD objects.
- Structure is represented as explicit arcs of the form `path -> target`.
- `SCE` commits an arc set into a structure root.
- A structure root is CID-compatible, but semantically it denotes committed structure rather than raw payload.

### Operational Structure State

The main operational state of MALT lives in the explicit arc representation.

In the current implementation, this hot state is typically namespaced per graph.
That organization is chosen for locality and performance, not as a semantic requirement of the abstraction.

`EAT` provides:

- point lookup of explicit arcs
- snapshots of the committed arc set for a root
- optional version-aware retrieval

Current implementations:

- `overwrite`
  - bucket-scoped current-view storage
- `versioned`
  - delta-per-version storage linked by `@previous`

### Stateless Commitment Engine

`SCE` is the cryptographic engine of MALT.

It is best understood as stateless with respect to authoritative graph state:

- it computes `Commit`, `Prove`, `Verify`, and `Update`
- it operates over inputs supplied by the caller
- any internal cache is an optimization, not semantic system state

The preferred conceptual split is:

- structural semantics
  - the public contract exposed to applications
  - `list` and `map`
- semantic implementations
  - internal realizations of those contracts
  - examples: flat indexed list, linked/chunked list, segment-radix map, hashed-path radix map
- commitment backend
  - internal authentication primitive used by an implementation
  - examples: KZG, IPA, hash/Merkle commitments

In that model, `SCE` should not be the public semantic layer.
The cleaner long-term direction is a separate `core/structure` layer that
exposes `list` and `map`, while `SCE` and commitment backends remain internal.

Two semantic notes matter:

- `list`
  - stable indexed structure with committed length
  - native operations are index proof and index-stable replacement
  - insert/delete are derived, higher-cost rewrites built above the semantic contract
- `map`
  - keyed structure whose implementation chooses the internal key-placement rule

The current repository still exposes many of these concerns through a single
`commitment.Scheme` interface for engineering convenience.
That interface should be read as a temporary code-level flattening, not as the
preferred architecture.

### Localized Write Path

The write path is implemented by `writer.Writer`.

For an arc update, the code follows this shape:

1. read the current binding from `EAT`
2. obtain the arc-set view or snapshot
3. ask `SCE` to commit or update the structure root
4. write the resulting arc state back through `EAT`
5. optionally record lineage metadata

This is the main operational realization of MALT's locality claim.

### Resolution and Verification

`resolver.Resolver` runs the read loop and returns:

- the resolved target
- a transcript of step evidence

There are two step kinds:

- explicit step
  - uses `EAT` lookup and `SCE` proof generation
- implicit compatibility step
  - traverses ordinary IPLD/Merkle objects through CAS

The explicit step is the native MALT path.
The implicit step exists for compatibility when traversal crosses into legacy CID space.

Clients verify the returned transcript locally:

- explicit steps verify against the structure root
- implicit steps verify by block hash and in-block path mapping

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

`Graph` is the main scoped MALT unit.

Important property:

- the graph is stateless with respect to the current root
- the root is always passed as an argument to read and write operations

This keeps structure evolution explicit and avoids embedding mutable "current root" state inside the graph object.

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
4. `SCE` computes a new structure root from the supplied inputs
5. `EAT` is updated to reflect the resulting structure state
6. optional lineage metadata is recorded

This flow is the implementation-level realization of localized structural evolution.

### Resolve / Verify Flow

The main read-side code path is:

1. `resolver.Resolver` starts from a root and remaining path
2. if the current identifier is a MALT structure root, it uses the explicit step executor
3. if the current identifier is an ordinary CID, it uses the compatibility step executor
4. each step appends evidence to the transcript
5. the client verifies the transcript step by step

The explicit step is the native MALT mechanism.
The implicit step is the interoperability path.

## Interoperability

MALT supports mixed traversal because structure roots are CID-compatible and can coexist with ordinary IPLD/CAS objects.

This means:

- MALT roots can point to payload CIDs
- payload objects can in principle link onward through ordinary IPLD semantics
- the resolver can dispatch between native explicit traversal and compatibility traversal

This is an interoperability property of the runtime model, not the primary definition of MALT.

## Secondary Surfaces

The following parts of the repository are useful but should be treated as secondary to the core architecture:

- `gateway/`
  - HTTP adapter and operational entry point
- `core/replication/`
  - snapshot and synchronization tooling
- `eval/`
  - benchmark and measurement scaffolding

## Implemented Variants

### EAT

- `overwrite`
  - bucket-scoped current-view storage
- `versioned`
  - delta-per-version storage linked by `@previous`

### SCE Schemes (Code)

The current code still groups several concerns under one `Scheme` notion:

- map-style implementation
  - radix
- fixed-slot backends used by list-like implementations
  - KZG
  - IPA

The intended refactor direction is:

- public `core/structure/list`
- public `core/structure/map`
- implementation subpackages below each semantic
- internal commitment backends selected by the implementation

## Verification Model

MALT uses transcript-based stepwise verification.

1. An untrusted resolver performs resolution.
2. It returns a transcript containing evidence for each step.
3. The client verifies each step locally.
4. The overall resolution is valid only if every step verifies.

This decouples execution from trust:

- resolvers can be untrusted
- lookup/index state can be operational rather than authoritative
- correctness remains cryptographic

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
