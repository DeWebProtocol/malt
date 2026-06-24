---
mip: 1006
title: Variable-Size Measured List Evidence
description: Specify a future range-addressable list model for variable-size children.
author: MALT maintainers
status: Draft
type: Standards Track
category: Core
created: 2026-05-25
requires: 1003
replaces: none
---

## Abstract

This MIP specifies a future measured-list proof model for variable-size child
segments.

## Motivation

Current range evidence is fixed-width and sufficient for the current UnixFS
large-file path. A variable-size list would need each range-addressable child
slot to authenticate both child size and CID, while root metadata
authenticates child count and total size.

## Specification

If accepted, the MIP should define:

- child descriptor shape
- root metadata
- range selection algorithm
- proof payload contents
- verifier checks

## Rationale

Current code evidence:

- `auth/semantic/list` defines list semantics and measured range
  interfaces.
- `runtime/semantic/list/tree` supports fixed-width measured range
  evidence.
- `layout/unixfs/prooflist.go` emits `list_range` ProofList steps with
  metadata and segment CIDs.

## Backwards Compatibility

The fixed-width measured-list path should remain valid unless the accepted MIP
explicitly replaces it.

## Security Considerations

The proof must bind both segment size and CID. A verifier must reject any range
proof that shifts byte boundaries while preserving the same segment CIDs.

## Implementation Plan

No implementation work is approved while this MIP is Draft. If accepted, a
phase plan should cover list backend changes, ProofList updates, server
verification, and evaluation schemas.

## History

- 2026-05-25: Created from the previous open TODO list.
