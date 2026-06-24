---
mip: 1001
title: Semantic Object And Arc Terminology
description: Define graph node, semantic object, payload, outgoing arc, map relation, and list node terminology.
author: MALT maintainers
status: Draft
type: Standards Track
category: Core
created: 2026-05-25
requires: none
replaces: none
---

## Abstract

This MIP defines the paper-facing terminology for graph nodes, semantic objects,
payloads, outgoing arcs, map relations, list child references, and CAS blobs.

## Motivation

The current implementation separates semantic abstractions from graph runtime
composition: `map` and `list` own semantic behavior, while `graph` composes
resolver and writer ports. The paper needs vocabulary that preserves this
boundary and avoids turning implementation helpers into semantic definitions.

## Specification

The MIP should define each term and classify it as semantic, implementation, or
layout-specific:

- semantic object
- graph node
- payload
- outgoing arc
- map relation
- list child reference
- CAS blob

## Rationale

Current code evidence:

- `auth/arcset` owns canonical path and arcset representation.
- `auth/semantic/mapping` defines map semantics.
- `auth/semantic/list` defines indexed child-reference semantics.
- `graph` composes resolver and writer ports.
- `layout/unixfs` is an application layout built from map/list/CAS blob
  composition.

## Backwards Compatibility

This MIP starts as documentation and naming work. Code renames, if any, require
a separate accepted MIP or implementation plan.

## Security Considerations

Terminology must not imply that ArcTable, local storage placement, or runtime
graph state is a trust root. Client verification remains rooted in semantic
proofs and published roots.

## Implementation Plan

No implementation work is approved while this MIP is Draft. If accepted, update
`README.md`, `paper.md`, active memory notes, and any code comments that use
conflicting terminology.

## History

- 2026-05-25: Created from the previous open TODO list.
