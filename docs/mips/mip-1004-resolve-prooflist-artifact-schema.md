---
mip: 1004
title: Resolve And ProofList Artifact Schema
description: Decide whether exported resolve JSON and bare ProofList JSON need stable named schemas.
author: MALT maintainers
status: Review
type: Standards Track
category: Interface
created: 2026-05-25
requires: 1003
replaces: none
---

## Abstract

This MIP records the current decision boundary for the artifact surfaces
described in [`docs/spec/artifacts.md`](../spec/artifacts.md): what is
documented for `v0.0.3-core-boundary`, and what is deferred until a stable
schema line.

## Motivation

`malt resolve` currently prints JSON containing `target` plus optional
`prooflist`, and `malt verify --prooflist` accepts both bare ProofList JSON and
resolve JSON containing a `prooflist` field. Benchmark and paper artifacts need
the current shape documented, but premature stable named schemas would imply a
compatibility commitment the repository has not yet made.

## Specification

The current artifact reference lives in
[`docs/spec/artifacts.md`](../spec/artifacts.md). For
`v0.0.3-core-boundary`:

- `malt resolve` JSON is documented as `api/http.ResolveResponse`;
- bare ProofList JSON is documented as `auth/proof/prooflist.ProofList`;
- proof-bearing content response metadata is documented as
  `X-Malt-ProofList` plus `X-Malt-ProofList-Encoding`;
- evaluator record schemas remain the only checked-in machine-readable JSON
  schemas;
- resolve JSON and bare ProofList JSON remain experimental and do not get
  stable named schema files in this release candidate.

A later stable-schema proposal may add named schema files, CLI schema listing,
and compatibility rules, but that work should happen in a separate issue or PR
after the verifier contract has settled.

## Rationale

Current code evidence:

- `cmd/malt/resolve.go` prints `api/http.ResolveResponse`.
- `api/http/types.go` defines `ResolveResponse`.
- `cmd/malt/verify.go` accepts ProofList inputs.
- `auth/proof/prooflist/prooflist.go` defines bare ProofList.
- `cmd/eval/schemas` has evaluator schemas but no stable resolve or
  bare ProofList schema.
- `docs/spec/artifacts.md` records the current artifact surfaces and explains
  that schema validation is separate from proof verification.

## Backwards Compatibility

The review decision is intentionally non-breaking: document the current shapes,
do not add stable named schemas, and do not change runtime output. Adding named
schemas later must not be presented as proof verification and must document any
compatibility commitment it introduces.

## Security Considerations

Schema validation is not proof verification. The MIP must keep structural JSON
validation separate from cryptographic or semantic verification.

## Implementation Plan

For the current review pass:

- keep the durable artifact reference in `docs/spec/artifacts.md`;
- do not add stable resolve or ProofList JSON Schema files for
  `v0.0.3-core-boundary`;
- keep evaluator schema listing scoped to `cmd/eval/schemas`;
- revisit named schemas when the repository is ready to commit to a stable
  artifact compatibility policy.

## History

- 2026-05-25: Created from the previous open TODO list.
- 2026-06-25: Moved current artifact boundaries to `docs/spec/artifacts.md`;
  this MIP now tracks whether those artifacts need stable named schemas.
- 2026-07-06: Recorded the `v0.0.3-core-boundary` decision to document current
  shapes without adding stable named resolve or ProofList JSON schemas; moved
  to Review for maintainer judgment.
