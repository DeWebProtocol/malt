# AGENTS.md

## Scope

This file defines implementation-specific rules for the `malt/` Go submodule.
For repo-level workflow, also read the parent repository `../AGENTS.md`.

## Implementation Focus

This submodule contains the Go implementation of MALT:

- authenticated graph contracts and map/list graph implementations
- ArcTable-backed structure materialization
- primitive commitment backends
- the current `core/layout/unixfs` prototype built directly over map/list semantics
- runtime adapters for current resolver / writer / graph packages
- daemon/API surface, CLI, and local CAS integration

## Current Architectural Conventions

- `arcset.Path` is the canonical internal path type.
- Canonicalization happens at read/write boundaries, not inside every backend.
- `arcset.ArcSet` is the interface for immutable arc-set views.
- `arcset.Set` is the default in-memory implementation.
- `graph` means an abstract authenticated read/write/verify contract.
- `map` and `list` are concrete graph implementations.
- current `core/graph` code is runtime metadata/composition, not the target graph abstraction.
- current `resolver` and `writer` code is adapter/runtime machinery, not the semantic owner.
- explicit resolution is a compatibility layer above map graph reads.
- list semantics use index/range reads and append/replace/truncate writes, not path resolution.
- `core/layout/unixfs` is the current application-layout prototype; it should not be treated as the final graph interface.
- unresolved graph-node, arc, resolver, and UnixFS runtime-integration questions should be tracked as TODOs for later design discussion.
- `bucketpath` and `manifest` are current bucket/file layout helpers and should not leak into core graph semantics.
- lineage/MVCC/versioned ArcTable concerns are pending design topics outside the minimal graph contract.

## Go Workflow

- Run Go commands from `malt/`.
- After Go code changes, run:
  - `go test ./...`
- Run targeted additional checks when the changed area justifies them.

## Git Workflow Inside The Submodule

- For feature work, use a feature branch.
- For bug fixes or small changes, direct changes on `main` are allowed.
- Commit and push the submodule before updating the parent repo pointer.

## Naming And Package Guidance

- Prefer names that reflect semantic roles rather than Go built-in terms.
- Keep upper-level runtime abstractions distinct from lower-level executor packages.
- Avoid duplicating path semantics or canonicalization logic across packages.
