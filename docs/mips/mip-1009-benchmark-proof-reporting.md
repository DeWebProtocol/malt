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

This MIP defines benchmark-facing reporting for proof bytes, evidence items,
writer receipts, CAS counters, ArcTable counters, and summary CSV fields.

## Motivation

Benchmark suites already emit structured outputs, but paper-facing proof and
receipt reporting needs a stable cross-suite rule before it can support final
figures.

## Specification

The MIP should define reporting rules for:

- ProofList bytes and step counts
- comparable Merkle DAG and HAMT evidence items
- writer receipt fields
- CAS operation counters
- ArcTable operation counters
- summary CSV fields used in paper figures

## Rationale

Current code evidence:

- `cmd/eval/schemas` contains evaluator schemas.
- `runtime/metrics` records CAS, ArcTable, and ProofList counters.
- `server/routes_metrics.go` exposes metrics snapshot and reset routes.
- `cmd/eval/internal/eval` emits raw result envelopes and summary CSVs.

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
