# MALT Docs

This directory is the implementation-bound documentation surface for
`DeWebProtocol/malt`.

Use these documents as the source of truth for behavior that must stay aligned
with code, tests, schemas, wire formats, and evaluator artifacts. Public
website pages in `DeWebProtocol/malt-web` may summarize this material, but they
should link back here for protocol, policy, and compatibility details.

Managed gateway service behavior, including tenancy, identity, authorization,
root publication, backend orchestration, S3/Filecoin/IPFS deployment policy,
quota, cache policy, and operations, belongs in `DeWebProtocol/gateway` or
private deployment overlays. This repository may keep a reference/evaluation
gateway only to exercise MALT core end to end.

## Concepts

- [Concepts index](./concepts/README.md)
- [Data authentication background](./concepts/data-authentication.md)
- [Merkle DAG vs MALT](./concepts/merkle-dag-vs-malt.md)

## Policy

- [Threat model](./policy/threat-model.md)
- [Compatibility policy](./policy/compatibility.md)
- [Release process](./policy/releasing.md)
- [Release notes](./releases/v0.0.3-core-boundary.md)

## Evaluation

- [Evaluation guide](./evaluation.md)

## Specifications

- [Specification index](./spec/README.md)

## MALT Improvement Proposals

MALT Improvement Proposals live in [docs/mips](./mips/). MIPs are the review
path for semantic, verifier-facing, API, layout, tooling, and evaluation
changes before they become implementation work.

MIPs should define the proposal boundary, motivation, decision, alternatives,
compatibility impact, security impact, and implementation planning state. Long
field lists, wire formats, JSON schemas, and benchmark record rules belong in
the reference docs under `spec/` or `evaluation.md`, with MIPs linking to them.

The previous `documents/MIPs` mirror in the research-paper workspace was
removed after migration. New implementation-bound MIP work should happen here.

## What Goes Where

- `concepts/` for reader-facing background, comparisons, and orientation
- `policy/` for stability, safety, and release policy
- `releases/` for source-release candidate notes and validation checklists
- `evaluation.md` for benchmark methods, headline results, and artifact rules
- `spec/` for formal protocol and schema documents
- `mips/` for design proposals and process records
