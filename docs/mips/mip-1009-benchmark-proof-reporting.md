---
mip: 1009
title: Benchmark Proof Reporting
description: Define paper-facing proof, receipt, and metrics reporting across benchmark suites.
author: MALT maintainers
status: Draft
type: Standards Track
category: Evaluation
created: 2026-05-25
requires: 1002, 1003, 1004
replaces: none
---

## Abstract

This MIP proposes stabilizing paper-facing reporting rules in the research
workspace, including proof
bytes, evidence items, writer receipts, CAS counters, ArcTable counters, and
write-amplification summary CSV fields.

## Motivation

Benchmark suites already emit structured outputs, but paper-facing proof and
receipt reporting needs a stable cross-suite rule before it can support final
figures.

## Specification

The current evaluation artifacts live in `DeWebProtocol/documents`. This MIP should decide
which reporting rules must become stable for paper-facing figures:

- ProofList bytes and step counts
- comparable Merkle DAG and HAMT evidence items
- writer receipt fields
- CAS operation counters
- ArcTable operation counters
- write-amplification accounting categories, including canonical delta bytes
  and derived-cache bytes
- summary CSV fields used in paper figures

## Rationale

The former in-tree evaluator was removed from SDK-only core in v0.0.6. Core
benchmark tests may measure commitment/proof behavior; gateway product E2E owns
CAS/ArcTable operational metrics; paper aggregation belongs in documents.

## Backwards Compatibility

Historical smoke artifacts should be labeled legacy if they do not match the
accepted reporting schema.

## Security Considerations

Benchmark reporting must distinguish proof bytes from payload bytes and must
not count unverifiable helper metadata as verification evidence.

## Implementation Plan

No implementation work is approved while this MIP is Draft. If accepted, a
phase plan should cover eval schemas, suite structs, summary generation,
benchmark protocol docs, and fixture/result validation.

## History

- 2026-05-25: Created from the previous open TODO list.
- 2026-06-25: Moved current reporting guidance to `docs/evaluation.md`;
  this MIP now tracks stabilization of paper-facing benchmark reporting.
- 2026-07-14: Routed evaluator artifacts to documents and operational metrics
  to gateway after the SDK-only core split.
