# Semantic Model

MALT's core semantic model is an authenticated list/map layer over immutable
CAS payload objects. This document is the implementation-bound reference for
the terms used by code, docs, and MIPs.

## Status

Experimental and implementation-bound. Public names may still change before a
stable release, but new docs should use this terminology.

## Core Terms

| Term | Meaning |
| --- | --- |
| Semantic object | A typed authenticated structure object, currently a map root or list root. |
| Graph root | A caller-supplied MALT structure root used as the verification handle for a read or mutation. |
| Payload | Immutable application bytes or metadata stored as ordinary CAS content and bound from MALT structure. |
| Outgoing arc | A canonical coordinate-to-CID binding represented in an arc set. |
| Map relation | A key/path-like authenticated binding from a map object to a target CID. |
| List child reference | An index-addressed authenticated binding from a list object to a child CID. |
| CAS blob | Immutable content-addressed bytes outside the MALT semantic layer. |

The implementation uses `auth/arcset` for canonical arc and coordinate
representations. Runtime materialization may use bucket or namespace keys
internally, but those storage keys are not semantic coordinates.

## Map Semantics

Map semantics authenticate key-to-CID relations. The current public abstraction
lives in `auth/semantic/mapping`, and the primary runtime implementation lives
under `runtime/semantic/mapping/radix`.

Native map operations are:

- exact key lookup with binding proof
- insert, replace, and delete of canonical keyed bindings
- commitment and verification of committed map state

Every MALT-native map object carries a reserved `@payload` binding. That
binding is the terminal materialization edge for file, directory, or other
layout payloads. It is not UnixFS-specific.

## List Semantics

List semantics authenticate ordered child references. The current public
abstraction lives in `auth/semantic/list`, and the primary runtime
implementation lives under `runtime/semantic/list/tree`.

Native list operations are:

- index lookup with proof
- append
- replace existing index
- truncate
- optional measured range proof when the implementation authenticates byte
  layout metadata

List objects do not auto-redirect through `@payload`, and list reads do not own
path-resolution semantics. Application layouts translate file ranges or domain
queries into list index or measured-range reads.

The current measured-list implementation is fixed-width. It authenticates child
count, total size, chunk size, index-to-CID bindings, and the selected byte
range's covered segment CIDs. A future variable-size measured list remains a
proposal-stage topic in [MIP-1006](../mips/mip-1006-variable-size-measured-list-evidence.md).

## Resolver And Writer Ports

The `graph` package is a small port/composition surface, not a separate
semantic owner.

- resolver is the read/proof port: `(root, query) -> result + ProofList`
- writer is the mutation port: `graph.MutationWriter.Apply(baseRoot, semantic mutation) -> newRoot + receipt`
- `graph.CompatWriter` contains reference-runtime helper methods and is not the gateway product API

Resolver traversal lives under `graph/resolver`. Mutation application lives
under `graph/writer`. Layouts translate source-domain data into semantic
queries and mutations; they do not redefine map or list semantics.

## ArcTable Boundary

ArcTable is namespace-scoped arcset persistence and materialization. It helps
the runtime prove and update structure efficiently, but it is not the trust
root. Incorrect materialized state should either fail proof generation or
produce evidence that the verifier rejects.

Freshness, head publication, merge policy, CAS availability, pinning, garbage
collection, tenant isolation, and quotas are deployment or application policy,
not core MALT semantics.

## Related Proposals

- [MIP-1001](../mips/mip-1001-semantic-object-and-arc-terminology.md) tracks
  terminology adoption.
- [MIP-1010](../mips/mip-1010-data-authentication-core-boundary.md) records the
  package-boundary decision that moved verifier-critical semantics into
  `auth/` and kept runtime, storage, API, and SDK code outside the data-auth
  core.
