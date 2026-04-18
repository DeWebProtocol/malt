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

## Terminology and Layers

The preferred abstraction is to expose **structural semantics** publicly and keep
layout/backend choices internal to each semantic implementation.

| Layer | Question | Examples |
| --- | --- | --- |
| Structural Semantics | What logical structure does the application expose? | `list`, `map` |
| Semantic Implementation | How is that semantic contract realized internally? | flat indexed list, linked/chunked list, segment-radix map, hashed-path radix map |
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

For engineering convenience, the current codebase still exposes many of these
choices through one `commitment.Scheme` interface under `core/sce`.
That is a legacy flattening of concerns, not the preferred conceptual layering.
The cleaner direction is to introduce a separate `core/structure` layer with
public `list` and `map` semantics, each with their own internal implementations.

In the current prototype, hot proof/index state is typically organized in deployment-specific namespaces for performance.
The current code often maps one namespace to one graph, but that state placement is an implementation choice, not a semantic requirement of the abstraction.

## Native Resolution

Native MALT resolution works over explicit arcs:

1. look up the relevant arc in `EAT`
2. obtain a root-scoped arc view
3. generate a proof using `SCE`
4. return a transcript that the client can verify locally

The explicit path is the primary path.

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
  - some older code paths still route through `sce`/`commitment`
- `EAT`
  - explicit arc materialization and lookup state
- `SCE`
  - stateless commitment/proof engine over caller-supplied arc views
- `Writer`
  - advances structure roots through localized arc updates
- `Resolver`
  - resolves from a structure root and returns a verifiable transcript
- `Lineage`
  - optional version metadata for ancestry and history operations

## Repo Layout

```text
malt/
├── cmd/
│   ├── gateway/main.go
│   └── malt/
├── config/
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
│   ├── sce/          # structure commitment engine
│   ├── structure/    # public structural semantics (`list`, `map`)
│   ├── types/        # arc sets, evidence, proof-related types
│   └── writer/       # write-side structure update flow
├── eval/
├── gateway/
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

## Preferred Refactor Direction

The long-term public abstraction should look like:

- `core/structure/list`
  - public stable-indexed list semantic
  - implementations may include flat indexed layouts backed by `IPA` or `KZG`
- `core/structure/map`
  - public keyed map semantic
  - implementations may include segment-radix or hashed-path radix layouts
- commitment backends
  - internal dependencies of structure implementations rather than the primary API surface
  - they authenticate positioned slots or nodes; they do not define public `list` / `map` semantics

## More Detail

For implementation structure and code-level control flow, see [`ARCHITECTURE.md`](./ARCHITECTURE.md).
