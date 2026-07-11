# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

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

[Unreleased]: https://github.com/DeWebProtocol/malt/compare/v0.0.3...HEAD
[0.0.3]: https://github.com/DeWebProtocol/malt/compare/v0.0.2...v0.0.3
