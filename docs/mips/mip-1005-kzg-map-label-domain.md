---
mip: 1005
title: KZG Map Label Domain
description: Record the canonical binding-CID slot model for storage-free map commitment primitives.
author: MALT maintainers
status: Final
type: Standards Track
category: Core
created: 2026-05-25
requires: 1001
replaces: none
---

## Abstract

This MIP records the current map-label commitment domain used by storage-free
map commitment primitives.

## Motivation

The paper needs to state what the verifier checks and what the commitment input
binds for keyed/path-like map labels. PR #108 resolved the immediate
storage-free primitive by committing canonical binding CID slots that encode
both key and value.

## Specification

`auth/semantic/mapping.Commitment` uses a canonical binding vector for its
storage-free commit path. Each committed slot is a binding CID derived from:

- a fixed `malt:map:binding:v1:` domain prefix
- the canonical path key bytes
- the target CID bytes

`Commit(view)` requires canonical key order and commits the resulting binding
CID cells. `ProveSlot(root, slots, slot)` proves one binding slot under a
committed root, and `VerifySlot(root, slot, value, proof)` verifies one slot
proof against the root.

This is distinct from the primary runtime map layout. `runtime/semantic/mapping/radix`
uses digest-keyed radix traversal and bucket materialization, but it composes
that runtime path from the same single-step slot proof primitive.

The current reference summary lives in
[`docs/spec/commitment.md`](../spec/commitment.md).

## Rationale

Current code evidence:

- `auth/commitment/kzg` provides the current KZG backend.
- `auth/semantic/mapping.BindingCID` binds canonical path keys to target CIDs
  for the storage-free map commit path.
- `runtime/semantic/mapping/radix` is the current runtime map semantic
  implementation and composes radix/bucket traversal from single-step slot
  proofs.
- `auth/arcset` canonicalizes map paths and arcsets before
  commitment-facing operations.

## Backwards Compatibility

Changing proof formats or commitment inputs can invalidate existing test vectors
or artifacts. Current artifacts after PR #108 should treat the binding-CID slot
model as the storage-free map primitive shape.

## Security Considerations

The binding CID domain binds labels and values together. A verifier should not
accept a bare value proof as a map binding proof unless the committed cell also
encodes the expected key.

## Implementation Plan

Implemented in PR #108. Follow-up work should only reopen this MIP if the
project moves from binding CID slots to a different map-label domain, such as
label-derived opening points.

## History

- 2026-05-25: Created from the previous open TODO list.
- 2026-06-07: Finalized after PR #108 introduced
  `auth/semantic/mapping.Commitment`, `BindingCID`, and runtime/radix use of
  single-step slot proof primitives.
