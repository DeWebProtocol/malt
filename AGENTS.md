# AGENTS.md

## Scope

This file defines implementation-specific rules for the `malt/` Go submodule.
For repo-level workflow, also read the parent repository `../AGENTS.md`.

## Implementation Focus

This submodule contains the Go implementation of MALT:

- resolver / typed step executors
- EAT
- SCE and commitment backends
- graph and writer layers
- gateway, CLI, evaluation, and replication

## Current Architectural Conventions

- `arcset.Path` is the canonical internal path type.
- Canonicalization happens at read/write boundaries, not inside every backend.
- `arcset.ArcSet` is the interface for immutable arc-set views.
- `arcset.Set` is the default in-memory implementation.
- `Hybrid Resolver` is the upper resolution loop.
- explicit / implicit components are lower typed step executors.
- `graph-scoped` is the semantic unit; in current code this maps to one bucket per graph.

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

