---
mip: 1004
title: Resolve And ProofList Artifact Schema
description: Decide whether exported resolve JSON and bare ProofList JSON need stable named schemas.
author: MALT maintainers
status: Draft
type: Standards Track
category: Interface
created: 2026-05-25
requires: 1003
replaces: none
---

## Abstract

This MIP decides whether `malt resolve` JSON, bare ProofList JSON, and related
proof-bearing artifacts should become stable named schemas.

## Motivation

`malt resolve` currently prints JSON containing `target` plus optional
`prooflist`, and `malt verify --prooflist` accepts both bare ProofList JSON and
resolve JSON containing a `prooflist` field. Benchmark and paper artifacts may
need a stronger schema contract.

## Specification

The MIP should decide whether to add named schemas for:

- resolve response JSON
- bare ProofList JSON
- proof-bearing content response metadata

It should also define schema paths, compatibility expectations, and CLI help
wording.

## Rationale

Current code evidence:

- `cmd/malt/resolve.go` prints `api/http.ResolveResponse`.
- `api/http/types.go` defines `ResolveResponse`.
- `cmd/malt/verify.go` accepts ProofList inputs.
- `auth/proof/prooflist/prooflist.go` defines bare ProofList.
- `cmd/eval/schemas` has evaluator schemas but no stable resolve or
  bare ProofList schema.

## Backwards Compatibility

Adding schemas should not change current runtime output unless the accepted MIP
explicitly introduces a versioned artifact contract.

## Security Considerations

Schema validation is not proof verification. The MIP must keep structural JSON
validation separate from cryptographic or semantic verification.

## Implementation Plan

No implementation work is approved while this MIP is Draft. If accepted, a
phase plan should cover schema files, CLI tests, docs, and optional schema
listing integration.

## History

- 2026-05-25: Created from the previous open TODO list.
