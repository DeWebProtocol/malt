# MALT Roadmap

This roadmap describes the MALT core implementation repository. It is
intentionally focused on executable core behavior, public schemas, evaluator
artifacts, and maintainer workflows.

## Current Focus

MALT is an experimental reference implementation. It is runnable end to end, but
its public APIs, ProofList schemas, wire formats, and deployment policies may
change. It is not production-ready.

`v0.0.4` establishes canonical segment-path composition and profiled
resolve/prove/verify artifacts. The post-release focus is to consume that
contract from product and application layers without importing the reference
server or treating UnixFS as the core abstraction.

## Completed In The Active Core-Boundary Refactor

- Separate portable mutation values from graph writer execution.
- Move proof generation and mutation application behind `execution.Executor`.
- Split commitment verification-only capabilities from prover/update methods.
- Split UnixFS into model, client SDK, and reference-runtime adapter packages.
- Publish a browser/WASM verifier and make CLI/Web verification local.
- Define the existing local daemon as a reference executor and remote verify as
  diagnostic-only.

## Near Term

- Harden the `DeWebProtocol/gateway` integration around identity,
  authorization, root publication, backend availability, and cache policy.
- Implement a standalone client agent/daemon for local root selection,
  upload/synchronization, local verification, and gateway interaction. This is
  distinct from the existing reference executor process.
- Expand `malt.artifact/v0alpha2` conformance vectors with KZG/IPA map, list,
  multi-hop resolve, and measured-range examples before cross-language SDKs
  depend on it.
- After the tightened core boundary is reviewed, demonstrate a second
  application model such as a PoDs-style datastore or agent-memory relation
  model.
- Refresh paper-grade evaluation artifacts with locked workloads, repeated
  runs, backend/config labels, and explicit aggregation policy.
- Keep `auth/verifier` free of runtime, storage, layout, server, daemon, and
  network dependencies as integrations expand.

## Active Research And Design Areas

- Complete payload CID/range-body verification integration in every client.
- Writer receipt semantics and accounting.
- Benchmark-facing proof reporting.
- Variable-size measured list evidence.
- Incremental `malt add --root` optimization.
- Native KZG multi-opening proofs instead of concatenated single-index proofs.
- Additional application models for Pods, protocols, agent memory, and
  application data.
- Head publication, freshness, and multi-writer policy as application or
  deployment concerns.

## Not Yet In Scope

- Production storage-service guarantees.
- Managed global head publication.
- Multi-tenant ACL, quota, pinning, and garbage-collection policy.
- Production managed gateway behavior such as identity providers, authorization
  policy, S3 backend orchestration, billing, abuse control, or operational
  deployment.
- Stable API compatibility across releases.
- A full replacement for Kubo, IPFS, or any CAS implementation.

## v0.0.3 Released Baseline

The `v0.0.3` source release was completed on 2026-07-12. It includes:

- the module-root typed `malt` facade
- portable KZG/IPA ProofList verification under `auth/verifier`
- generic relation-only maps without mandatory `@payload`
- explicit UnixFS Reader/Writer and range-body verification boundaries
- narrow graph/runtime dependency ports and import guards
- malformed typed-root and KZG proof fail-closed hardening
- source-release validation through full tests, vet, command builds, evaluator
  smoke, external-consumer import, and isolated CLI proof verification

The validation record and compatibility limits live in
[`docs/releases/v0.0.3.md`](docs/releases/v0.0.3.md).

## v0.0.4 Release Layer

The `v0.0.4` source release adds:

- canonical `SegmentPath` semantics and slash textual projection
- longest-prefix candidate discovery without a verifier maximality claim
- the unversioned `artifact` package and `malt.artifact/v0alpha2` profile
- checked-in and embedded resolve/prove/verify JSON Schemas
- stable `/v1/artifacts/{resolve,prove,verify}` reference endpoints
- root-identity codec fixtures and end-to-end artifact verification tests

See [`docs/releases/v0.0.4.md`](docs/releases/v0.0.4.md).
