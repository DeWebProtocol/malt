# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

## [0.0.6] - 2026-07-14

### Changed

- Recast this module as an SDK-only core: canonical contracts, commitment and
  semantic algorithms, ProofList verification, portable mutation values, and
  untrusted execution composition remain in-tree.
- Move map/list reference implementations to `auth/semantic` and compose graph
  execution under `graph/runtime` over a caller-injected ArcSet materializer.
- Define `auth/arcset/materializer.Store` as an implementation-neutral
  capability; persistent ArcTable/KV implementations now belong to gateways.

### Removed

- CLI/client daemon, HTTP server, CAS/KV backends, concrete ArcTable modes,
  UnixFS, reference executor, and evaluation application packages.
- Core documentation for product HTTP routes and evaluator commands.

These are intentional pre-v1 source breaks. Use `DeWebProtocol/malt-client`
for the trusted CLI/daemon and UnixFS application, and
`DeWebProtocol/gateway` for ArcTable/KV/CAS-backed execution.

## [0.0.5] - 2026-07-13

### Added

- Operation-specific `malt.resolve/v0alpha1` and `malt.read/v0alpha1`
  request, result, verification, JSON Schema, and reference HTTP contracts.
- Portable `mutation` value contracts and a separate untrusted
  `execution.Executor` facade implementing resolve, primitive read, and apply.
- Client-local `sdk/verifier` plus a reproducible browser/WASM verifier build.
- Explicit UnixFS `model/unixfs`, `sdk/unixfs`, and `runtime/unixfs` boundaries.

### Changed

- The module-root `malt` package no longer imports graph writer/execution code;
  it owns query/result/mutation projections and verification bindings only.
- Commitment verification-only interfaces are separated from prover/updater
  capabilities for light-client and WASM consumers.
- The old local daemon bootstrap is identified as a reference executor, and
  remote verify routes are diagnostic/conformance surfaces only.
- `malt verify` performs portable verification locally, binds an explicit
  trusted root and caller-selected canonical query, and exits non-zero on
  rejection.
- The local Go/WASM verifier request binds caller-selected root, operation,
  query, and optional expected target inside the verifier boundary.

### Fixed

- `malt.artifact/v0alpha2` decoders preserve compatibility with v0.0.4
  zero-segment identity queries that omitted `segments`, while canonical output
  emits `segments: []`.
- `malt verify --query ""` accepts a valid zero-step root-identity artifact and
  still binds its root and implied target locally.
- Reference diagnostic verification reuses one lazily initialized portable
  verifier per server and rejects oversized request bodies before initializing
  the KZG/IPA registry.
- UnixFS compatibility helpers return a diagnostic error for a nil CAS reader
  instead of panicking.
- Reference-executor CORS exposes `X-Malt-Verification-Role` so browser clients
  can distinguish diagnostic verification responses.

## [0.0.4] - 2026-07-12

### Added

- Canonical immutable `SegmentPath` values and slash textual projection for
  application-neutral multi-arc resolution.
- Unversioned `artifact` package with the explicit
  `malt.artifact/v0alpha2` resolve/prove/verify contract.
- Embedded JSON Schemas, root-identity conformance fixtures, and stable
  `/v1/artifacts/{resolve,prove,verify}` reference endpoints.
- MIP-1012 and reference specifications for segment-path composition and
  existential resolution.

### Changed

- New integrations carry canonical segment arrays instead of pre-discovering
  how the current graph groups a long path into arcs.
- Reference resolution may prefer the longest prefix, while verification proves
  only the complete returned derivation and makes no longest/unique claim.
- MIP-1004 is finalized with profiled artifacts and machine-readable schemas.

## [0.0.3] - 2026-07-12

### Added

- Module-root `malt` facade with typed `Query`, `ReadRequest`, `ReadResult`,
  `Engine.Read`, `Engine.Apply`, and `Engine.VerifyRead` contracts.
- Portable `auth/verifier` support for runtime-generated KZG and IPA map,
  list-index, and measured-range ProofList evidence without runtime, ArcTable,
  CAS, layout, server, daemon, or network dependencies.
- Separate UnixFS Reader and Writer facades with explicit CAS capabilities.
- MIP-1011 and the experimental `v0alpha1` typed read/result and ProofList
  binding profile.
- Resumable full write-trace replay and expanded read/evaluation workloads.

### Changed

- Generic maps may omit or delete the reserved `@payload` coordinate; UnixFS
  continues to require it as a layout invariant.
- `graph/verifier` is now a thin reference-runtime adapter over the portable
  authentication kernel.
- Graph resolver/writer code consumes narrow ArcTable interfaces, and
  `runtimegraph.NewGraph` no longer accepts an unused CAS argument.
- Upgraded Badger from 4.9.2 to 4.9.4, `go-ipld-format` from 0.6.3
  to 0.6.4, and `golang.org/x/sync` from 0.21.0 to 0.22.0.

### Fixed

- Portable verification now preserves all generic canonical map coordinates
  and maps semantic absence to the public `ErrQueryNotFound` facade error.
- Typed MALT roots reject backend-invalid commitment lengths instead of
  truncating extra digest bytes during verification.
- KZG verification rejects out-of-range proof indices and non-canonical proof
  lengths instead of allowing malformed input to panic or reuse a commitment.

[Unreleased]: https://github.com/DeWebProtocol/malt/compare/v0.0.6...HEAD
[0.0.6]: https://github.com/DeWebProtocol/malt/compare/v0.0.5...v0.0.6
[0.0.5]: https://github.com/DeWebProtocol/malt/compare/v0.0.4...v0.0.5
[0.0.4]: https://github.com/DeWebProtocol/malt/compare/v0.0.3...v0.0.4
[0.0.3]: https://github.com/DeWebProtocol/malt/compare/v0.0.2...v0.0.3
