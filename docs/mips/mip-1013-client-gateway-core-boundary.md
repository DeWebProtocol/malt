---
mip: 1013
title: Client, Gateway, And Core Responsibility Boundary
description: Separate client trust decisions, gateway execution, CAS payload storage, and UnixFS application-model responsibilities.
author: MALT maintainers
status: In Progress
type: Standards Track
category: Core
created: 2026-07-13
requires: 1010, 1011, 1012
replaces: none
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
| MALT core | canonical arcs/segments, root/CID rules, query and mutation values, ProofList/artifact schemas, commitment verification, portable verification | HTTP, ArcTable placement, CAS I/O, tenant policy, UnixFS or language-object syntax |
| Client | application syntax mapping, mutation construction, trusted-root selection, local artifact/proof verification, authenticated payload-byte checks | ArcTable, proof generation, server indexes |
| Gateway/executor | resolve/prove/apply execution, ArcTable/KV, proof generation, CAS orchestration, identity/authorization/cache/quota policy | the client's final proof acceptance decision |
| CAS backend | immutable bytes addressed by CID | graph paths, arcs, ProofLists, application syntax |

Gateway is a logical product/execution boundary, not a required process
topology. It may embed an executor or call one remotely. In either form the
ArcTable is untrusted materialized execution state.

### Operation Ownership

- `resolve`: core defines segment/artifact and verification semantics; the
  gateway/executor selects and proves a complete derivation; the client verifies
  it locally. Selection is existential and need not prove longest or unique.
- `prove`: core defines typed query and proof bindings; the executor produces
  evidence; the client verifies it locally.
- `verify`: core supplies deterministic algorithms; the client executes the
  authoritative decision. Remote verify routes are diagnostic only.
- `apply`: core defines namespace-free mutation and receipt values; the client
  application constructs mutations; the gateway/executor applies them.

### Package Boundary

The target implementation packages are:

```text
core contracts:       package malt, auth/, artifact/, mutation/, wire/
execution contract:   execution/
reference backend:    graph/, runtime/, reference/executor/, server/
client verification:  sdk/verifier/, cmd/malt-verifier-wasm/
UnixFS model:          model/unixfs/
UnixFS client:         sdk/unixfs/
UnixFS runtime:        runtime/unixfs/
```

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
is reserved for a narrower materialization choice such as `flat` versus
`hierarchical`, or raw versus fixed-list payload organization.

- `model/unixfs` owns schema, reserved coordinates, manifest/chunk formats,
  invariants, and model-specific mutation plans.
- `sdk/unixfs` owns staging, upload planning, portable mutation construction,
  and local payload/range-body verification.
- `runtime/unixfs` is an optional in-process reader/writer/proof/content adapter
  used by the reference executor or a gateway.

Future TypeScript object support belongs primarily in a client SDK that maps
JavaScript object syntax into canonical segments and mutations. Core does not
parse `.` or `[]` syntax.

### Process Naming

The current process started by `malt start` remains available for development
compatibility, but documentation defines it as the all-in-one reference
executor. A future client daemon/agent is a separate process that stores trusted
roots, verifies locally, and communicates with a gateway without owning the
server ArcTable.

## Compatibility

This is a pre-`v1` source-level boundary change. The serialized
`malt.artifact/v0alpha2` profile does not change. Remote verify endpoints remain
available for diagnostics, and the `graph/writer` mutation names are retained as
deprecated aliases while integrations move to `mutation`.

The former `layout/unixfs` package path is removed rather than made normative
through a forwarding package. Integrators should select the model, client, or
runtime package matching their role.

## Security Considerations

A remote `valid: true` response cannot establish correctness because it may
come from the same untrusted service that produced the candidate artifact.
Clients fail closed if the local verifier cannot load or rejects any root,
caller-selected operation/query, optional expected target, ordering, or
cryptographic binding.

The local verifier request carries `trusted_root` and an `expected` operation,
query, and optional target separately from the artifact. It rejects mismatches
inside the Go/WASM verification boundary, not only in UI or transport code.

Proof verification authenticates the relation to a payload CID. Clients must
also validate fetched bytes against that CID; measured UnixFS ranges additionally
require range-body binding. Root freshness, authority, rollback prevention, and
multi-writer policy remain application or managed-gateway concerns.

## Implementation Status

The active implementation branch introduces the package split, local CLI and
browser/WASM verifier, import-boundary tests, reference-executor terminology,
and diagnostic remote verification. Move this MIP to Final only after that PR
is merged and current documentation points at the merged package paths.
