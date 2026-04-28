# AGENTS.md

## Scope

This file defines implementation-specific rules for the MALT Go repository.

This repository is independent. It is not a submodule of the documents
repository. For workspace-level coordination, also read `../AGENTS.md` when it
exists. For paper, planning, and TODO context, read the sibling documents
repository at `../documents`.

## Implementation Focus

This repository contains the Go implementation of MALT:

- list/map semantic abstractions over immutable CAS payloads
- bucket/namespace-scoped ArcTable-backed arcset persistence/materialization
- stateless primitive commitment backends
- the current `core/layout/malt/unixfs` prototype built from list/map/CAS blob composition
- runtime adapters for current resolver / writer / graph packages
- daemon/API surface, CLI, and local CAS integration

## Adjacent Repositories

- `../documents` is the paper, notes, planning, and TODO repository.
- `../worktrees` is reserved for linked worktrees created from this implementation repository.
- Do not add this repository back as a submodule of `../documents`.
- Do not update documents-repo submodule pointers; there are no submodule pointers in the new workflow.
- When documents need to reference an implementation version, use this repository's commit SHA or tag.

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
- every map semantic object carries reserved `@payload` as its terminal materialization binding.
- layouts translate source-domain data into MALT semantic mutations.
- the gateway accepts converted semantic mutations and returns `result + ProofList` for standardized reads.
- `core/layout/malt/unixfs` is the current application-layout prototype; it should not be treated as the core semantic abstraction.
- unresolved graph-node, arc, resolver, and UnixFS runtime-integration questions should be tracked as TODOs for later design discussion.
- `bucket` is an operational namespace/collection boundary for runtime state, not a core list/map or arcset semantic.
- `bucketpath` and `manifest` are current bucket/file layout helpers and should not leak into core semantic rules.
- head publication, freshness, merge, and multi-writer arbitration belong to application/deployment policy.
- overwrite ArcTable is the simple current-state implementation; versioned ArcTable is the default MVCC-style implementation.

## Go Workflow

- Run Go commands from this repository root.
- After Go code changes, run:
  - `go test ./...`
- Run targeted additional checks when the changed area justifies them.
- Documentation-only changes in this repository do not require Go tests.

## Git Workflow

- `main` is the integration branch for implementation work.
- Do not commit implementation changes directly to `main` unless the user explicitly asks for that exact action.
- Implementation work must happen on one-time branches and linked worktrees.
- Use branch names with this pattern:
  - `codex/one-time/feature/<slug>`
  - `codex/one-time/refactor/<slug>`
  - `codex/one-time/bugfix/<slug>`
- Create linked worktrees under `../worktrees`.
- Worker sessions must commit their changes, push their branch, open a PR targeting `main`, and then stop or archive the worker session.
- Worker sessions must not merge PRs or push directly to `main`.
- The coordinator or merge session may merge PRs into `main` after explicit user instruction.
- After a one-time PR is merged or explicitly abandoned, the coordinator may delete the one-time branch and remove its worktree after verifying the worktree has no uncommitted changes.
- Keep `main` and remote `origin/main` synchronized before creating new worker branches.

## Commit Policy

- Commit messages should use Conventional Commits style.
- Include a complete commit message, not a placeholder title.
- Commit messages must follow this format:
  - first line: summary in Conventional Commits style, 50 characters or fewer
  - second line: blank
  - remaining lines: detailed explanation of what changed and why
- The body should explain both the concrete changes and the rationale.
- Include `Co-authored-by: Codex <codex@openai.com>` or the current agent equivalent when the agent authors the change.

## Naming And Package Guidance

- Prefer names that reflect semantic roles rather than Go built-in terms.
- Keep upper-level runtime abstractions distinct from lower-level executor packages.
- Avoid duplicating path semantics or canonicalization logic across packages.
