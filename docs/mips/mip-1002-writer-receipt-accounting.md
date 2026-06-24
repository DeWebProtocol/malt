---
mip: 1002
title: Writer Receipt Accounting
description: Decide how writer receipts support storage, indexing, accounting, and benchmark reporting.
author: MALT maintainers
status: Draft
type: Standards Track
category: Interface
created: 2026-05-25
requires: none
replaces: none
---

## Abstract

This MIP defines the meaning of writer receipt fields and decides which fields
are API contract, storage accounting, indexing hints, benchmark metrics, or
operational diagnostics.

## Motivation

The writer already returns mutation receipts, and HTTP mutation responses expose
receipt-derived counts. The paper and benchmark pipeline need a stable meaning
for those counts before treating them as evidence.

## Specification

The MIP should classify current and possible receipt fields:

- base root and new root
- delta count
- arc count
- map count
- list count
- persisted byte or record counts, if added
- root publication metadata, if explicitly kept out of core

## Rationale

Current code evidence:

- `graph/writer` owns semantic mutation execution and receipts.
- `api/http/types.go` exposes `SemanticMutationResponse`.
- `server/routes_write.go` maps writer receipts into HTTP responses.
- `layout/unixfs/mutation.go` converts layout plans into writer
  semantic mutations.

## Backwards Compatibility

Existing HTTP response fields should remain compatible unless the accepted MIP
explicitly changes the API and defines a migration path.

## Security Considerations

Receipt fields are not correctness proofs. They must not be described as
verification evidence unless tied to a ProofList or commitment proof.

## Implementation Plan

No implementation work is approved while this MIP is Draft. If accepted, a
phase plan should cover `graph/writer`, `api/http`, server routes, eval schemas,
summary generation, and tests.

## History

- 2026-05-25: Created from the previous open TODO list.
