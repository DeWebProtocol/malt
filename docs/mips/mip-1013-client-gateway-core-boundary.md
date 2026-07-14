---
mip: 1013
title: Client, Gateway, And Core Responsibility Boundary
description: Separate client trust decisions, gateway execution, CAS payload storage, and UnixFS application-model responsibilities.
author: MALT maintainers
status: Final
type: Standards Track
category: Core
created: 2026-07-13
requires: 1010, 1011, 1012
replaces: 1010
---

## Abstract

This MIP tightens MALT around four roles: portable authentication core, trusted
client, untrusted gateway/executor, and immutable CAS payload backend. It moves
proof generation and mutation execution out of the module-root facade, makes
verification a local client decision, and defines UnixFS as a first-party
application model/profile rather than a MALT layout or core semantic.

## Motivation

The authentication kernel already verifies ProofLists without ArcTable or CAS,
but previous package and process names obscured the trust boundary:

- the root `malt.Engine` combined proof generation, mutation application, and
  verification;
- the process called a daemon actually held ArcTable/KV, connected CAS, and
  served proofs, so it was an execution backend rather than a client daemon;
- gateway and Web integrations could call remote `/verify`, making an
  untrusted service appear to decide correctness; and
- `layout/unixfs` combined an application model, client planning, CAS work,
  runtime proof generation, and content serving.

Those ambiguities make a second application model harder to add safely.

## Decision

### Responsibility Matrix

| Role | Owns | Does not own |
| --- | --- | --- |
| MALT core | canonical arcs/segments, root/CID rules, resolve/read/mutation values, ProofList and serialized contract schemas, commitment verification, portable verification | HTTP, ArcTable placement, CAS I/O, tenant policy, UnixFS or language-object syntax |
| Client | application syntax mapping, mutation construction, trusted-root selection, local resolve/read verification, authenticated payload-byte checks | ArcTable, proof generation, server indexes |
| Gateway/executor | resolve/read/apply execution, ArcTable/KV, proof generation, CAS orchestration, identity/authorization/cache/quota policy | the client's final proof acceptance decision |
| CAS backend | immutable bytes addressed by CID | graph paths, arcs, ProofLists, application syntax |

Gateway is a logical product/execution boundary, not a required process
topology. It may embed an executor or call one remotely. In either form the
ArcTable is untrusted materialized execution state.

### Operation Ownership

- `resolve`: core defines segment/result and verification semantics; the
  gateway/executor selects and proves a complete derivation; the client verifies
  it locally. Selection is existential and need not prove longest or unique.
- `read`: core defines one primitive typed map/list query, result, and proof
  binding; the executor produces the result and evidence; the client verifies
  them locally. Proof generation is part of execution, not a separate generic
  `prove` operation.
- `verify`: core supplies deterministic algorithms; the client executes the
  authoritative decision. Remote verify routes are diagnostic only.
- `apply`: core defines namespace-free mutation and receipt values; the client
  application constructs mutations; the gateway/executor applies them. A
  receipt is not a delta or state-transition proof.

### Package Boundary

The target implementation packages are:

```text
core contracts:       package malt, auth/, protocol/, mutation/, wire/
legacy compatibility: artifact/
execution contract:   execution/
execution algorithms: graph/, graph/runtime/
client verification:  sdk/verifier/, cmd/malt-verifier-wasm/
client application:    DeWebProtocol/malt-client
gateway runtime:       DeWebProtocol/gateway
```

The client repository keeps three independently reviewable layers:

```text
transport/    untrusted native MALT, CAS, and diagnostic HTTP capabilities
trust/        accepted/candidate root persistence and explicit promotion
unixfs/       MALT-authenticated UnixFS application and payload binding
merkledag/    separate CID/link-replay compatibility application
```

The concrete transport may share one HTTP connection, but callers depend on
narrow capability interfaces. `transport` must not import application or trust
packages. Merkle DAG compatibility must not import or manufacture MALT
ProofList contracts.

The gateway repository likewise separates composition from execution:

```text
internal/runtime/              per-scope service composition
internal/profile/malt/         native Resolve/Read/mutation ports
internal/profile/cas/          immutable byte ports
internal/profile/merkledag/    CID/link-evidence compatibility ports
internal/backend/              profile-neutral persistence capabilities
internal/policy/               scope-adjacent managed-service policy
```

Named root publication is a gateway policy record, not a MALT head and not a
client trust decision. It may track revisions or freeze a name, but every core
resolve/read request still carries its explicit root.

The root `malt` package must not import graph, runtime, server, storage,
application-model, or SDK packages. `mutation` contains value types and
validation only. `execution.Executor` may generate candidates but never
establishes trust.

### Verification Backends

Commitment implementations expose a verification-only interface separately
from prover/update capabilities. Portable clients and browser/WASM builds depend
on verification only. Full commitment backends may implement both interfaces.

### UnixFS Application Model

UnixFS is called the MALT UnixFS application model/profile. The term `layout`
is reserved for a narrower application materialization choice. The current
native client exposes `hybrid`, combining authenticated directory map roots
with descendant full-path bindings. Pure `flat` versus `hierarchical`, or raw
versus fixed-list payload organization, remain possible future choices.

The independent `malt-client` repository owns UnixFS schema, reserved
coordinates, manifest/chunk formats, staging, upload planning, mutation
construction, local payload/range-body verification, and optional
IPFS-compatible Merkle DAG UnixFS import. A Merkle DAG root is a client
compatibility output, not MALT authentication evidence. The generic gateway
does not parse UnixFS paths or expose a UnixFS content adapter.

The frozen v0.0.5 evaluator retains `MALT-flat`/`maltflat` for its full-path
flat-map baseline. Those historical identifiers do not configure or describe
the current `malt add --layout hybrid` path.

Future TypeScript object support belongs primarily in a client SDK that maps
JavaScript object syntax into canonical segments and mutations. Core does not
parse `.` or `[]` syntax.

### Process Naming

`malt-client` supplies the trusted local daemon/agent. It stores accepted and
candidate roots, verifies locally, and communicates with a gateway without
owning server ArcTable/KV/CAS state. The gateway is the untrusted execution
process. A gateway may understand Merkle DAG/IPLD semantics only inside its
separate compatibility profile; native MALT execution and generic CAS backends
do not inherit those application semantics.

## Compatibility

This is a pre-`v1` source-level boundary change. The serialized
`malt.artifact/v0alpha2` profile remains frozen with `resolve` and `prove` for
v0.0.4 compatibility. New integrations use `malt.resolve/v0alpha1` and
`malt.read/v0alpha1`; remote verify endpoints remain diagnostic. The
`graph/writer` mutation names are retained as deprecated aliases while
integrations move to `mutation`.

v0.0.6 removes the in-module CLI, daemon, server, storage, ArcTable, and UnixFS
packages without forwarding packages. This is an intentional pre-v1 source
break; integrations use the separate client and gateway repositories.

## Security Considerations

A remote `valid: true` response cannot establish correctness because it may
come from the same untrusted service that produced the candidate artifact.
Clients fail closed if the local verifier cannot load or rejects any root,
caller-selected request, returned target, ordering, or cryptographic binding.

The local verifier receives the caller-constructed resolve/read request
separately from the untrusted result. It rejects mismatches inside the Go/WASM
verification boundary, not only in UI or transport code.

Proof verification authenticates the relation to a payload CID. Clients must
also validate fetched bytes against that CID; measured UnixFS ranges additionally
require range-body binding. Root freshness, authority, rollback prevention, and
multi-writer policy remain application or managed-gateway concerns.

## Implementation Status

The operation-specific contracts and trust split were released in v0.0.5.
v0.0.6 completes the repository split: core is SDK-only, the gateway owns
ArcTable/KV/CAS execution, and `malt-client` owns the trusted CLI/daemon and
UnixFS application.
