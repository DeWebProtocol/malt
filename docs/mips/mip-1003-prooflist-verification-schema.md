---
mip: 1003
title: ProofList Verification Contract
description: Formalize ProofList step semantics, body/header binding, proof omission, and range-body verification.
author: MALT maintainers
status: Review
type: Standards Track
category: Core
created: 2026-05-25
requires: none
replaces: none
---

## Abstract

This MIP formalizes the verifier contract described in
[`docs/spec/prooflist-format.md`](../spec/prooflist-format.md), including map
traversal, terminal `@payload`, blob binding, measured-list `list_range`
evidence, proof omission, and returned-body binding.

## Motivation

The implementation emits and verifies ProofList evidence through a reusable
`graph/verifier` package. The paper-facing contract still needs a clear review
boundary so readers can distinguish ProofList verification from HTTP body-byte
binding and from JSON shape validation.

## Specification

The current ProofList reference lives in
[`docs/spec/prooflist-format.md`](../spec/prooflist-format.md). For the
`v0.0.3-core-boundary` candidate, the review boundary is:

- ordered path traversal is verifier-facing and implemented by
  `graph/verifier`;
- terminal `@payload` binding is a terminal proof step and must not be followed
  by later traversal steps;
- map, list-index, and measured-list `list_range` structure evidence is
  verified through the runtime semantic backends;
- proof omission via query/header is a transport option and does not change the
  verifier contract for artifacts that are returned;
- `X-Malt-ProofList` carries base64url-encoded ProofList JSON for content reads;
- returned byte-range body bytes are not authenticated by `/verify` alone and
  must be bound with `layout/unixfs.VerifyRangeBody` or an equivalent segment
  byte check after ProofList verification.

This MIP does not promote ProofList JSON to a stable cross-release schema. It
records the current verifier contract for review before a stable API line.

## Rationale

Current code evidence:

- `auth/proof/prooflist/prooflist.go` defines ProofList shape.
- `graph/verifier/verifier.go` verifies ordered traversal, query binding, and
  map evidence.
- `auth/verifier/list.go` verifies list evidence and measured-list range
  evidence against the runtime semantic backends.
- `server/routes_verify.go` projects the reusable verifier through `/verify`.
- `server/routes_content.go` sends proof-bearing content responses in
  `X-Malt-ProofList`.
- `layout/unixfs/prooflist.go` emits map, payload, list-index, and
  `list_range` evidence.
- `layout/unixfs/range_body.go` binds returned range bytes to authenticated
  segment CIDs.

## Backwards Compatibility

The `v0.0.3-core-boundary` contract remains experimental. If field names,
evidence labels, or verifier behavior change before a stable release, the same
change must update `docs/spec/prooflist-format.md`, tests, CLI/HTTP examples,
and release notes.

## Security Considerations

This MIP is security-sensitive because it defines what a client actually
verifies. ProofList verification must reject forged paths, malformed list
metadata, branch jumps, and traversal after terminal payload bindings. Range
body acceptance must also reject shifted ranges, segment CID mismatches, short
segment data, and tampered returned bytes.

## Implementation Plan

For the current review pass:

- keep the durable field reference in `docs/spec/prooflist-format.md`;
- keep reusable verifier orchestration in `graph/verifier`;
- keep verifier-critical list-step helpers in `auth/verifier`;
- keep range body-byte binding in `layout/unixfs.VerifyRangeBody`;
- run verifier, server, UnixFS, CLI, and full Go validation before tagging.

## History

- 2026-05-25: Created from the previous open TODO list.
- 2026-06-25: Moved current ProofList shape and transport rules to
  `docs/spec/prooflist-format.md`; this MIP tracks formal acceptance of the
  verifier contract.
- 2026-07-06: Updated for `graph/verifier` extraction and
  `layout/unixfs.VerifyRangeBody`; moved to Review for maintainer judgment.
