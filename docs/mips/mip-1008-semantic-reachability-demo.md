---
mip: 1008
title: Semantic Reachability Demo
description: Define a small demo for reverse, cross-object, and multi-view relations.
author: MALT maintainers
status: Draft
type: Informational
category: Evaluation
created: 2026-05-25
requires: 1001
replaces: none
---

## Abstract

This MIP defines a small capability demo for semantic relations beyond ordinary
UnixFS path reads.

## Motivation

MALT should be able to demonstrate reverse relations, cross-object relations,
and multi-view path/index relations. A small demo can make that capability
concrete without turning it into the primary benchmark path.

## Specification

This MIP should choose a demo shape:

- relation types to demonstrate
- source dataset
- verifier story
- CLI, eval-suite, or documentation-only delivery
- whether the result is a paper-facing capability case study

It does not define a protocol schema. If accepted, any reusable benchmark or
artifact rules should be documented under `docs/evaluation/`.

## Rationale

Current implementation evidence:

- UnixFS demonstrates map/list/CAS composition.
- Resolver and writer ports express root-relative semantic reads and mutations.
- Current evaluation primarily targets UnixFS and IPLD baselines.

## Backwards Compatibility

The demo should not change core API behavior unless a separate accepted MIP
requires it.

## Security Considerations

The demo must not imply unsupported freshness, publication, or multi-writer
semantics. It should remain root-relative and verifier-facing.

## Implementation Plan

No implementation work is approved while this MIP is Draft. If accepted, a
phase plan should name the command, suite, fixture, and docs paths to change.

## History

- 2026-05-25: Created from the previous open TODO list.
- 2026-06-25: Clarified that this is a demo/case-study proposal, not a
  reference specification.
