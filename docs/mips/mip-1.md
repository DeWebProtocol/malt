---
mip: 1
title: MIP Purpose and Guidelines
description: Define the MALT Improvement Proposal process and document format.
author: MALT maintainers
status: Living
type: Meta
created: 2026-05-25
requires: none
replaces: none
---

## Abstract

MALT Improvement Proposals, or MIPs, are the design records used to propose,
review, accept, plan, and archive changes to MALT's semantics, interfaces,
tooling, evaluation framework, and project process. This MIP defines the MIP
format, status lifecycle, numbering policy, and relationship between MIPs,
implementation TODOs, phased implementation plans, and completed-history
records.

## Motivation

The project needs to separate open design questions from accepted
implementation work. Draft proposals should be easy to review and revise
without implying that the work is committed to the implementation mainline.
Accepted proposals should become implementation TODO work only after the
maintainer approves the direction and a model writes a phased plan with concrete
files, tests, verification gates, and PR boundaries.

## Specification

### What Is A MIP?

A MIP is a versioned design document. It can describe:

- a semantic or API change;
- a tooling or implementation change;
- an evaluation or benchmark reporting change;
- a process change for how MALT is developed or documented;
- a non-normative informational design note that should be tracked like a
  proposal.

A MIP is not implementation work by default.

A MIP is also not the canonical home for long-lived field tables, wire formats,
JSON schemas, benchmark record rules, or other reference specifications. Those
belong in `docs/spec/`, `docs/evaluation/`, or implementation-owned schema
directories. A MIP may propose or record changes to those references.

### MIP Types

MIPs use these `type` values:

- `Standards Track`: semantic, API, implementation, tooling, or evaluation
  changes that may become implementation mainline work.
- `Meta`: process changes that govern MIPs, project workflow, or project
  coordination.
- `Informational`: non-normative design notes or case-study proposals.

Standards Track MIPs may use these `category` values:

- `Core`: semantic, verification, commitment, or proof model changes.
- `Interface`: daemon, CLI, HTTP, artifact, or schema-facing changes.
- `Tooling`: developer or user tooling changes.
- `Evaluation`: benchmark, metric, artifact, or demo changes.

Meta and Informational MIPs do not need a `category`.

### MIP Statuses

MIPs use these statuses:

- `Draft`: open candidate, still being shaped.
- `Review`: ready for maintainer review.
- `Last Call`: expected to be accepted unless objections appear.
- `Accepted`: approved to enter the implementation mainline.
- `Planned`: accepted and backed by a phased implementation plan.
- `In Progress`: implementation branch or PR is active.
- `Final`: implemented, merged, and current docs updated.
- `Stagnant`: no longer actively considered.
- `Withdrawn`: deliberately abandoned.
- `Superseded`: replaced by another MIP.
- `Living`: continually updated process or index MIP.

### Numbering

`mip-1.md` is reserved for this Living Meta MIP. Low numbers are reserved for
future process or governance MIPs. Standards Track and Informational MIPs should
start at `mip-1001.md` unless the maintainer explicitly assigns another range.

The `mip` front matter field stores the numeric identifier without leading
zeroes. Filenames use the form `mip-N-title.md`.

### Required Front Matter

Each MIP must start with YAML front matter:

```yaml
mip: 1001
title: Short Title
description: One sentence summary.
author: MALT maintainers
status: Draft
type: Standards Track
category: Core
created: 2026-05-25
requires: none
replaces: none
```

`category` is required only for Standards Track MIPs. `requires` and `replaces`
may be `none`.

### Required Sections

Each non-template MIP should include:

- `## Abstract`
- `## Motivation`
- `## Specification`
- `## Rationale`
- `## Backwards Compatibility`
- `## Security Considerations`
- `## Implementation Plan`
- `## History`

Do not add a separate `Summary` section. The `description` field is the
one-sentence summary, and `Abstract` is the short technical summary.

The `Specification` section should describe the proposed decision or behavior
boundary precisely enough for review. If the change needs a durable reference
document, link to the relevant file under `docs/spec/`, `docs/evaluation/`, or
an implementation schema directory instead of duplicating the full reference
inside the MIP.

### Relationship To Implementation Planning

Draft, Review, Last Call, Stagnant, Withdrawn, Superseded, and Living MIPs stay
out of implementation work queues.

When a MIP becomes Accepted, create a GitHub issue, PR plan, or repository
planning note that names the implementation owner and review boundary. When a
phased implementation plan exists, move the MIP to `Planned`. When a branch or
PR is active, move it to `In Progress`. When implementation merges, update the
MIP `History`, update current status docs, and close the linked implementation
work.

## Rationale

This process keeps open design exploration separate from committed
implementation work. It also gives each accepted change a stable history:
proposal, acceptance, phase plan, implementation, merge, and completed-history
record.

Using `mip-1001.md` and above for technical proposals leaves low numbers for
process MIPs while keeping the current proposal set visually distinct from
process definitions.

## Backwards Compatibility

This MIP replaces the informal proposal workflow previously described in the
original research workspace's `MIPs/README.md`. Existing Draft MIPs are
renumbered into the 1001 range.

## Security Considerations

MIPs that affect verification, proof semantics, commitment inputs, or API
contracts must include concrete security considerations before they can move to
Accepted. A MIP cannot become Final if its security implications are still
unstated.

## Implementation Plan

This process MIP is active immediately once committed. Existing Draft MIPs
should follow this format and numbering policy.

## History

- 2026-05-25: Created as the Living Meta MIP for the MALT proposal process.
