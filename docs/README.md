# MALT Docs

This directory is the implementation-bound documentation surface for
`DeWebProtocol/malt`.

Use these documents as the source of truth for behavior that must stay aligned
with code, tests, schemas, wire formats, and evaluator artifacts. Public
website pages in `DeWebProtocol/malt-web` may summarize this material, but they
should link back here for protocol, policy, and compatibility details.

## Policy

- [Threat model](./policy/threat-model.md)
- [Compatibility policy](./policy/compatibility.md)
- [Release process](./policy/releasing.md)

## Evaluation

- [Evaluation guide](./evaluation/README.md)

## Specifications

- [Specification index](./spec/README.md)

## MALT Improvement Proposals

MALT Improvement Proposals live in [docs/mips](./mips/). MIPs are the review
path for semantic, verifier-facing, API, layout, tooling, and evaluation
changes before they become implementation work.

MIPs should define the proposal boundary, motivation, decision, alternatives,
compatibility impact, security impact, and implementation planning state. Long
field lists, wire formats, JSON schemas, and benchmark record rules belong in
the reference docs under `spec/` or `evaluation/`, with MIPs linking to them.

The previous `documents/MIPs` mirror in the research-paper workspace was
removed after migration. New implementation-bound MIP work should happen here.

## What Goes Where

- `policy/` for stability, safety, and release policy
- `evaluation/` for benchmark protocol and artifact rules
- `spec/` for formal protocol and schema documents
- `mips/` for design proposals and process records
