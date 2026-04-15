# MALT

MALT is a graph-scoped authenticated structure layer over immutable content-addressed storage.

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

## Native Resolution

Native MALT resolution works over explicit arcs:

1. look up the relevant arc in `EAT`
2. obtain a graph/root-scoped arc view
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

MALT uses transcript-based compositional verification:

1. an untrusted resolver executes the lookup
2. it returns per-step evidence
3. the client verifies each step locally
4. the lookup is valid only if every step verifies

This keeps correctness cryptographic even when lookup/index infrastructure is not trusted.

## Core Components

- `Graph`
  - scoped unit of structure and main read/write entry point
- `EAT`
  - graph-scoped explicit arc materialization and lookup state
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
│   ├── graph/        # graph metadata and per-graph composition
│   ├── kvstore/      # KV backends
│   ├── lineage/      # version lineage metadata
│   ├── replication/  # secondary snapshot/sync tooling
│   ├── resolver/     # resolution loop and step executors
│   ├── sce/          # structure commitment engine
│   ├── types/        # arc sets, evidence, proof-related types
│   └── writer/       # write-side structure update flow
├── eval/
├── gateway/
└── integration/
```

## What Is Secondary

These parts of the repo are useful, but they are not the conceptual center of MALT:

- gateway deployment
- mixed MALT/IPLD traversal machinery
- replication and snapshot tooling
- benchmark scaffolding
- helper deployment abstractions
- backend comparisons among KZG, Verkle, and IPA

## More Detail

For implementation structure and code-level control flow, see [`ARCHITECTURE.md`](./ARCHITECTURE.md).
