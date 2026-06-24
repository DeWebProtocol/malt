---
mip: 1007
title: Incremental malt add --root Optimization
description: Explore path-local incremental add for very large existing roots.
author: MALT maintainers
status: Draft
type: Standards Track
category: Tooling
created: 2026-05-25
requires: 1002
replaces: none
---

## Abstract

This MIP explores a path-local incremental update path for `malt add --root`
on very large existing roots.

## Motivation

Current `malt add --root` prioritizes correctness and client-side CAS
batch/dedup by loading and merging the current tree before re-materialization.
That is correct for the prototype, but a future optimization may avoid full
current-tree reconstruction.

## Specification

The MIP should decide whether the next mainline implementation needs:

- path-local current-root loading
- path-local mutation planning
- unchanged CAS dedup behavior
- unchanged layout-side root computation
- compatible writer receipts and ProofList behavior

## Rationale

Current code evidence:

- `cmd/malt/add_workflow.go` routes `malt add` through staged UnixFS
  ingestion.
- `cmd/malt/add_staging.go` builds the staged tree.
- `layout/unixfs/mutation.go` exposes root-relative mutation plans.
- PR #49 completed the current batch/dedup path without making updates
  path-local in the old daemon-write sense.

## Backwards Compatibility

The optimization must preserve current `malt add` behavior, root semantics, CAS
dedup semantics, and symlink directory boundary behavior.

## Security Considerations

Incremental update shortcuts must not skip verification-relevant map/list
bindings or publish a root that cannot be reconstructed from semantic deltas.

## Implementation Plan

No implementation work is approved while this MIP is Draft. If accepted, a
phase plan should cover add workflow tests, existing-root merge behavior,
large-file lists, symlink directory boundaries, and CAS dedup behavior.

## History

- 2026-05-25: Created from the previous open TODO list.
