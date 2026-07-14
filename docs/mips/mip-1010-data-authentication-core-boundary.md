---
mip: 1010
title: Data Authentication Core Boundary
description: Narrow MALT core to data authentication and separate graph ports, layouts, SDKs, APIs, runtime, and storage.
author: MALT maintainers
status: Superseded
type: Standards Track
category: Core
created: 2026-05-25
requires: 1001, 1003, 1004
replaces: none
---

> Implementation note: PR #76 implemented this package split in the sibling
> Go repository on 2026-05-25. The motivation below preserves the pre-split
> problem statement; current package names are summarized in the migration table
> and now use `auth`, `graph`, `runtime`, `storage`, `wire`, `api/http`, and
> `sdk/client` boundaries.
>
> **Historical scope:** this MIP is superseded by
> [MIP-1013](./mip-1013-client-gateway-core-boundary.md) and the v0.0.6
> SDK-only repository split. It does not define the current public core API.
> The portable arc-level
> `Read`/`Apply`/`VerifyRead` contract and the `v0.0.3` release boundary are
> specified by [MIP-1011](./mip-1011-arc-authentication-core-contract.md).

## Abstract

This MIP defines the target architecture for narrowing MALT core to data
authentication. The graph abstraction remains the product-facing contract:
resolver is the read/proof port, writer is the mutation port, and layouts are
domain implementations that translate application data into graph queries and
mutations. CAS, codecs, metrics, node factories, HTTP payloads, SDK clients,
and server lifecycle code are separated from the data-authentication core.

## Motivation

The current implementation has improved graph/resolver/writer/layout naming,
but `core` still contains packages that do not belong to a data
authentication core:

- `core/api` is a node/runtime factory that wires config, KV, ArcTable, CAS,
  graph managers, and metrics.
- `core/cas` defines content-addressed storage clients and block helpers.
- `core/codec` mixes typed MALT root CIDs with `malt-manifest` application
  layout CIDs.
- `core/kvstore` provides generic persistence adapters.
- `core/manifest` and `core/querypath` are UnixFS/runtime helpers.
- `core/metrics` is observability and benchmark accounting.
- `httpapi`, `client`, `server`, and `daemon` currently import several
  `core/*` packages directly, which makes the word `core` mean both
  verifier-critical semantics and product/runtime plumbing.

This matters because paper logic, security reasoning, and open-source package
layout should make the trust boundary obvious. MALT core should be the part
needed to compute, bind, and verify authenticated semantic state relative to a
trusted root. Everything else should be an adapter, runtime, storage library,
API schema, SDK facade, layout, command, or evaluation harness.

## Specification

### Architectural Layers

The repository should converge on this conceptual stack:

```text
client / SDK / layout
  -> graph ports and graph API contracts
  -> data authentication core
  -> runtime state and storage adapters
  -> server transport and daemon lifecycle
```

The stack is not a call stack for every operation. It is an ownership model:

- layouts live on the client/SDK side and implement domain-specific graph
  behavior by translating source data into graph queries and writer mutations.
- graph is the abstraction boundary, made of resolver and writer ports.
- writer is the mutation port and mutation executor surface; it is not a
  separate semantic layer above graph.
- resolver is the read/proof port; it is root-relative and returns verifier
  evidence for caller-supplied roots.
- data authentication core owns only semantic state, commitments, proof
  semantics, canonical encodings used by commitments, and verification rules.
- server/runtime code implements graph ports over storage and data-auth core,
  but it is not itself the trust root.

### Data Authentication Core

The data authentication core should contain only verifier-critical semantics:

- canonical ArcSet and coordinate representations
- map and list semantic authentication rules
- commitment interfaces and commitment backends
- proof artifact semantics and verification rules
- canonical commitment-input encodings
- root computation and root verification rules
- error types needed by these semantics

The data authentication core must not contain:

- CAS clients or Kubo/IPFS adapters
- generic KV stores or Badger/filesystem/memory implementations
- daemon node factories
- HTTP payload structs or route handlers
- SDK clients or CLI command workflows
- metrics, tracing, or benchmark accounting
- UnixFS manifests, path routing helpers, or application layout metadata
- evaluation compatibility code or baselines
- head publication, latest-root choice, freshness, merge, pinning, GC, quota,
  ACL, or replication policy

### Graph Ports

`graph` should be a small abstraction package rather than the owner of core
semantics. It should expose:

- `Graph`: a composition of resolver and writer capabilities.
- `Resolver`: root-relative read/proof interface.
- `Writer`: mutation interface. The writer request is the mutation model.
- `Mutation`, `Delta`, `Receipt`, `Query`, and `Resolution` contract types.

Server-side graph runtimes implement these ports over data-auth core plus
runtime state. Client-side layouts may expose graph-shaped facades, but their
job is translation: domain operation to graph query/mutation, and graph result
to domain result.

### Codec Ownership

The current `core/codec` package should be split by ownership:

| Current responsibility | Target owner | Reason |
|---|---|---|
| Canonical bytes fed into commitments | `auth/encoding` | Verifier-critical input to commitments. |
| MALT map/list root CID multicodecs such as `malt-map-kzg` and `malt-list-kzg` | `wire/maltcid` | Lower shared wire library used by auth, graph API, SDKs, and server. |
| Commitment byte-size constants | commitment packages or `wire/maltcid` only where needed for CID validation | Avoid making a generic codec package own commitment semantics. |
| `malt-manifest` CID | `layout/unixfs/internal/format` | UnixFS/application layout artifact, not data-auth core. |
| CAS block CID helpers and codec normalization | `storage/cas` | Storage library concern. |
| JSON DTO encoding | `api/http` or `wire/json` | Transport schema concern, not core auth. |

This means `codec` is neither wholly upper-layer nor wholly lower-layer. It is
a mixed package and should be dissolved into narrower packages. Data
authentication may depend on lower wire root encoders, but application codecs
must not leak into auth.

### Target Repository Layout

The implementation repository should converge toward:

```text
auth/
  arcset/
  commitment/
    ipa/
    kzg/
  encoding/
  proof/
  semantic/
    list/
    mapping/
  verifier/

wire/
  maltcid/
  json/

graph/
  graph.go
  mutation/
  resolver/
  writer/

layout/
  unixfs/
    model/
    mutation/
    proof/
    wire/

sdk/
  client/
  unixfs/
  verify/
  mutation/

api/
  http/

server/
  routes_*.go
  service_graph.go
  service_verify.go

runtime/
  node/
  arctable/
  metrics/

storage/
  cas/
    kubo/
    mock/
  kv/
    badger/
    fs/
    memory/

cmd/
  malt/
  eval/
    internal/
      compat/
      baseline/
      eval/
  internal/
```

Package names can be adjusted during implementation, but import direction is
normative:

```text
auth -> wire/maltcid only for typed root CIDs; no server, SDK, storage, layout, or API imports
graph -> auth contract types and wire types; no daemon lifecycle or storage adapters
layout -> graph + auth value types + storage/CAS upload interfaces
sdk -> graph/api/http clients + layout facades + verification helpers
api/http -> DTOs only; no runtime construction and no direct dependency on runtime metrics objects
server -> graph + runtime + storage + api/http
runtime -> auth + storage + graph implementations + metrics wrappers
storage -> CAS/KV libraries; no auth semantics except opaque CIDs/bytes
cmd -> SDK/server/runtime orchestration only
```

### Current Package Classification

| Current package | Target owner | Classification |
|---|---|---|
| `core/types/arcset` | `auth/arcset` | Data-auth core. |
| `core/structure/mapping` | `auth/semantic/mapping` | Data-auth core. |
| `core/structure/list` | `auth/semantic/list` | Data-auth core. |
| `core/commitment` | `auth/commitment` | Data-auth core. |
| `core/types/evidence` | `auth/proof` | Data-auth core proof material. |
| `core/types/prooflist` | `auth/proof` or `graph/resolution` | Verifier artifact; keep core semantics in auth, transport projection in graph/API. |
| `core/writer` | `graph/writer` plus auth helpers | Writer is mutation port/executor surface, not auth core ownership. |
| `core/resolver` | `graph/resolver` plus auth proof helpers | Resolver is read/proof port/executor surface. |
| `core/graph` | `graph` and `runtime/node` | Interfaces belong in graph; concrete runtime composition belongs in runtime/server. |
| `core/arctable` | `runtime/arctable` | Root-relative materialization/performance plane, not trust root. |
| `core/kvstore` | `storage/kv` | Generic persistence library. |
| `core/cas` | `storage/cas` | Payload and block storage library. |
| `core/codec` | `auth/encoding`, `wire/maltcid`, `layout/unixfs/internal/format`, `storage/cas` | Mixed ownership; split instead of moving as one unit. |
| `core/api` | `runtime/node` or `sdk` depending on entrypoint | Node factory/runtime composition, not auth core. |
| `core/metrics` | `runtime/metrics` or `observability/metrics` | Observability and evaluation accounting. |
| `core/manifest` | `layout/unixfs/model` or `layout/unixfs/internal/format` | UnixFS/application layout. |
| `core/querypath` | `layout/unixfs` or `graph/query` | Path parsing/query convenience, not auth core. |
| `client` | `sdk/client` | SDK facade over API. |
| `httpapi` | `api/http` | Transport DTOs and JSON contract. |
| `server` | `server` | Transport handlers and service composition. |
| `daemon` | `runtime/node` plus `cmd/malt` orchestration | Process lifecycle. |
| `config` | `runtime/config` or `config` | Runtime configuration, not auth core. |
| `cmd/eval/internal/compat` | unchanged under `cmd/eval/internal/compat` | Evaluation-only compatibility code. |
| `cmd/eval/internal/baseline` | unchanged under `cmd/eval/internal/baseline` | Evaluation baseline code. |

### Client, Server, SDK, And API Boundaries

Client-side responsibilities:

- interpret source-domain data, such as UnixFS files and directories
- upload or reference CAS payload objects as an application/storage step
- build graph writer mutations from layout semantics
- build graph resolver queries from user operations
- verify `result + ProofList` against a trusted root
- publish or consume roots through an application layer outside MALT core

Server-side responsibilities:

- receive explicit-root graph resolver requests
- receive writer mutation requests with an explicit base root
- apply mutations through graph writer executors
- assemble root-relative proof material
- expose runtime metrics and health as operational endpoints
- never choose the authoritative latest root for MALT core
- never become a correctness trust root

SDK responsibilities:

- provide Go-facing client APIs over the graph/API surface
- expose UnixFS convenience methods
- expose mutation builders and verification helpers
- hide HTTP transport details from callers
- avoid owning daemon lifecycle unless explicitly in a runtime package

API responsibilities:

- define stable wire DTOs and JSON compatibility rules
- map API DTOs to graph contract types at the server boundary
- avoid runtime construction, storage adapter selection, and metrics object
  ownership inside DTO packages
- keep schema validation separate from cryptographic or semantic verification

### Gateway Integration Contract

A managed gateway should depend on MALT through core packages, not the
reference server. The expected import surface is:

- `auth/proof/prooflist` for verifier-facing proof artifacts
- `auth/verifier` for portable ProofList verification
- the module-root `malt` package for typed read, mutation, and verification
  orchestration
- `graph` resolver and writer contracts where a gateway exposes root-relative
  core operations
- `graph/writer` semantic mutation and receipt types
- `layout/unixfs` only when the gateway intentionally reuses the reference UnixFS-style layout

A managed gateway must not import `server` as its product API boundary. The
gateway owns tenants, projects, API keys, authorization, root registry,
latest-head publication, freshness and rollback policy, backend credentials,
S3/Filecoin/IPFS orchestration, cache policy, quota, billing, pinning, garbage
collection, abuse controls, and operations. Gateway roots are product metadata;
they do not become MALT core state.

## Rationale

Three layouts were considered.

The conservative option keeps `core/` and moves only obvious outsiders into
`core/auth`, leaving many existing import paths in place. This reduces churn,
but it keeps the misleading `core` umbrella and makes future contributors ask
which parts are verifier-critical.

The recommended option removes the broad `core` umbrella and uses top-level
packages for real ownership: `auth`, `graph`, `layout`, `sdk`, `api`, `server`,
`runtime`, `storage`, `cmd`, and `wire`. This is the clearest open-source
layout because package names describe trust boundaries and operational
boundaries directly.

The Go-common option uses `pkg/` and `internal/` as the main split. This is
familiar to many Go projects, but it risks turning `pkg/` into the same broad
bucket as today's `core`. It can still be used selectively for private
server/runtime internals, but it should not be the primary architecture.

This MIP chooses the recommended option.

## Backwards Compatibility

This MIP is Final. PR #76 completed the package-boundary migration in the Go
implementation while preserving public behavior.

Temporary type aliases and forwarding packages were acceptable during the
migration, especially for:

- `core/types/arcset` to `auth/arcset`
- `core/types/prooflist` to `auth/proof` or the chosen proof package
- `core/codec` root CID helpers to `wire/maltcid`
- `core/cas` to `storage/cas`
- `core/kvstore` to `storage/kv`

Forwarding packages should have removal criteria and source-layout guards so
the repo does not keep two architectures indefinitely.

## Security Considerations

This proposal is security-relevant because it makes trust boundaries explicit.
Only data-authentication packages should be used to reason about correctness.
Server, runtime, storage, metrics, CAS availability, and API transport are
performance or deployment concerns unless their outputs are verified by the
auth core against a trusted root.

The split must preserve these invariants:

- verification is relative to a trusted root supplied by the caller
- server/runtime nodes do not attest freshness or latest-head authority
- CAS availability is distinct from authenticated CID binding
- schema validation is distinct from proof verification
- metrics and write receipts are operational evidence, not correctness
  evidence
- layout-specific codecs and metadata must not affect canonical auth inputs
  unless explicitly represented as authenticated semantic arcs

## Implementation Plan

Implemented by PR #76. The historical research note
`documents/memories/notes/Data Authentication Core Refactor Plan.md` remains
useful background, but future implementation planning should live in this
repository's normal issue or PR workflow.

## History

- 2026-05-25: Created after the graph/resolver/writer/layout refactor exposed
  that the remaining `core` package still mixed data authentication with
  runtime, storage, API, SDK, metrics, and layout responsibilities.
- 2026-05-25: Finalized by PR #76, which moved the former broad `core`
  package surface into current `auth`, `graph`, `runtime`, `storage`,
  `api/http`, `sdk/client`, and `wire/maltcid` package families.
- 2026-07-11: Clarified that this MIP is the historical package-split record;
  MIP-1011 now owns the portable arc-authentication contract.
