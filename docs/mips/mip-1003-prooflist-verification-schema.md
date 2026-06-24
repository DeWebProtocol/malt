---
mip: 1003
title: ProofList Verification Schema
description: Formalize ProofList step semantics, body/header binding, proof omission, and range-body verification.
author: MALT maintainers
status: Draft
type: Standards Track
category: Core
created: 2026-05-25
requires: none
replaces: none
---

## Abstract

This MIP formalizes the paper-facing ProofList schema and verifier contract for
map traversal, terminal `@payload`, raw blob binding, measured-list `list_range`
evidence, proof omission, and returned-body binding.

## Motivation

The implementation emits and verifies ProofList evidence, but the paper-facing
contract remains incomplete. The main remaining gap is binding returned HTTP
bytes to the requested byte range and proved segment contents.

## Specification

The MIP should define verifier behavior for:

- ordered path traversal
- terminal `@payload` binding
- raw blob binding
- measured-list `list_range` step semantics
- optional proof omission via query/header
- `X-Malt-ProofList` header semantics
- returned byte range binding to proved segment contents

## Rationale

Current code evidence:

- `auth/proof/prooflist/prooflist.go` defines ProofList shape.
- `server/service_verify.go` verifies map, list, and measured-list
  structure evidence.
- `server/routes_content.go` sends proof-bearing content responses in
  `X-Malt-ProofList`.
- `layout/unixfs/prooflist.go` emits map, payload, list-index, and
  `list_range` evidence.

## Backwards Compatibility

The first accepted version may be documentation-only. If fields or verifier
behavior change, existing CLI and API compatibility must be handled in the
implementation plan.

## Security Considerations

This MIP is security-sensitive because it defines what a client actually
verifies. It must reject forged paths, malformed list metadata, branch jumps,
and mismatches between returned bytes and proved segment contents.

## Implementation Plan

No implementation work is approved while this MIP is Draft. If accepted, a
phase plan should include schema docs, verifier tests, CLI validation, and
benchmark artifact validation.

## History

- 2026-05-25: Created from the previous open TODO list.
