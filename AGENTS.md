# AGENTS.md

## Scope

This file defines implementation-specific rules for the `malt/` Go submodule.
For repo-level workflow, also read the parent repository `../AGENTS.md`.

## Implementation Focus

This submodule contains the Go implementation of MALT:

- list/map semantic abstractions over immutable CAS payloads
- Bucket-based ArcTable-backed arcset persistence/materialization
- stateless primitive commitment backends
- the current `core/layout/unixfs` prototype built from list/map/CAS blob composition
- runtime adapters for current resolver / writer / graph packages
- daemon/API surface, CLI, and local CAS integration

## Current Architectural Conventions

- `arcset.Path` is the canonical internal path type.
- Canonicalization happens at read/write boundaries, not inside every backend.
- `arcset.ArcSet` is the interface for immutable arc-set views.
- `arcset.Set` is the default in-memory implementation.
- `list` and `map` are semantic abstractions, not merely concrete implementations.
- `list` describes complex graph nodes with indexed/ranged child references.
- `map` describes authenticated relations among graph nodes.
- current `core/graph` code is runtime metadata/composition, not the target semantic abstraction.
- current `resolver` and `writer` code is adapter/runtime machinery, not the semantic owner.
- explicit resolution is a compatibility layer above map reads.
- list semantics use index/range reads and append/replace/truncate writes, not path resolution.
- `core/layout/unixfs` is the current application-layout prototype; it should not be treated as the core semantic abstraction.
- unresolved graph-node, arc, resolver, and UnixFS runtime-integration questions should be tracked as TODOs for later design discussion.
- `bucketpath` and `manifest` are current bucket/file layout helpers and should not leak into core semantic rules.
- overwrite ArcTable is the simple current-state implementation; versioned ArcTable is the default MVCC-style implementation.

## Go Workflow

- Run Go commands from `malt/`.
- After Go code changes, run:
  - `go test ./...`
- Run targeted additional checks when the changed area justifies them.

## Git Workflow Inside The Submodule

- Follow the parent repository coordinator workflow in `../AGENTS.md`.
- Worker sessions must create their own `feature/`, `refactor/`, or `bugfix/`
  branch for submodule work.
- Worker sessions must not commit directly to `main`.
- Worker sessions must not merge PRs into `main`.
- Worker sessions should target the current coordinator branch for PRs unless
  the user explicitly requests a different target.
- The coordinator session must not merge branches or PRs unless the user
  explicitly instructs it to do so.
- Commit and push the submodule branch before updating the parent repo pointer.

## Naming And Package Guidance

- Prefer names that reflect semantic roles rather than Go built-in terms.
- Keep upper-level runtime abstractions distinct from lower-level executor packages.
- Avoid duplicating path semantics or canonicalization logic across packages.
