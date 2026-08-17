# AGENTS.md

## Scope

This repository owns the user-controlled MALT local data runtime: CLI, local
daemon, accepted-root policy, UnixFS application semantics, Merkle DAG import
compatibility, pluggable transport capabilities, and payload-byte verification.
It also keeps the runtime-owned executable adapters needed to measure those exact behaviors;
the evaluator that plans and interprets campaigns lives in `malt-evaluation`.

## Boundaries

- Do not define MALT protocol, ProofList, commitment, CID, schema, or canonical
  graph semantics here; use the `malt-core` module.
- Do not add ArcTable/KV persistence or managed gateway policy here; those
  belong in `gateway`.
- Treat gateway responses as untrusted and verify against caller-selected
  roots locally.
- Never promote a mutation result to a trusted root without explicit local
  acceptance or an independent root-publication policy.
- Keep application separators and UnixFS path rules in this repository. Pass
  typed segment arrays to MALT core.
- Keep IPFS-style Merkle DAG import and compatibility policy here. A Merkle DAG
  root is a compatibility output, not a MALT-authenticated root or ProofList.
- Keep benchmark suites, run plans, comparison policy, result schemas, and
  result provenance in `malt-evaluation`. Code under `tools/evaluation` is a
  pinned process adapter, not a supported runtime command or an evaluator
  source of truth.

## Package Ownership

- `transport/capability` owns URL-free, route-free, trust-free semantic ports
  and untrusted dataset/mutation results shared by Gateway, local, peer, and
  hybrid implementations. The root `transport` package is the current Gateway
  HTTP adapter plus compatibility DTOs. Neither may import `trust`, `unixfs`,
  or `merkledag`. Do not expose evaluation instance credentials, bootstrap
  controls, unchecked raw-CAS reads, or selective-CAR routes here.
- `trust/` is the only package that persists accepted/candidate roots or
  promotes a candidate. It must not depend on network or application packages.
- `application/` owns reusable accepted-root selection, candidate recording,
  explicit acceptance, UnixFS use cases, bulk local-input staging, and Merkle
  DAG import/read orchestration shared by CLI and daemon adapters. It depends
  on narrow ports, not Cobra or arbitrary transport routes.
- `application/backup.BatchRunner` owns plan selection, batch execution, and
  typed partial-failure aggregation. Foreground, daemon, and scheduled adapters
  must use that same runner contract.
- `bucketsync` production synchronization uses
  `transport/capability.DatasetBranch`; its concrete Gateway DTO adapter lives
  only in `gateway_compat.go` for the pre-release compatibility window.
- `internal/runtime` is the process-independent composition root. It may bind
  concrete local configuration, transport, trust, keyring, synchronization,
  and UnixFS capabilities into application services; command handlers must not
  duplicate that composition.
- `unixfs/` owns the MALT-authenticated UnixFS facade, staging,
  materialization, and payload/range verification. Keep reusable UnixFS
  behavior here rather than under `cmd/malt`. Its semantic mutation adapter
  consumes `transport/capability.Mutations`; only `gateway_compat.go` may
  import legacy Gateway mutation DTOs during the compatibility window.
- `merkledag/` owns the compatibility adapter and local CID/link-evidence
  replay; `merkledag/importer` owns import construction. Do not represent this
  evidence as a MALT ProofList.
- `cmd/malt/` is the process/composition adapter. Command handlers should
  select configured runtime services, call `application/` use cases, and
  format results, not become a second UnixFS, trust, or plan-composition
  implementation. `cmd/` contains only the supported `malt` product binary.
- `tools/evaluation/cmd/` owns evaluator-launched runtime process adapters.
  Preserve their external binary names and wire contracts, but do not treat
  their Go packages as a compatibility surface.
- `internal/evaluation/` owns shared implementation for those adapters,
  including disposable-Gateway instance authentication, bootstrap, raw-CAS,
  and selective-CAR transport. Production packages and `cmd/malt` must not
  import it.
- Preserve `internal/architecture` dependency tests when adding packages or
  moving responsibilities.

## Validation

Run `gofmt`, `git diff --check`, `go test ./...`, `go vet ./...`, and
`go build -buildvcs=false ./...` before committing. Also run a Windows
cross-build for changes to daemon lifecycle, locking, filesystem, or CLI code.
