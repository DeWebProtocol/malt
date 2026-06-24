# Compatibility Policy

MALT is an experimental reference implementation. It is runnable end to end,
but public APIs, ProofList schemas, wire formats, and deployment policies may
change. It is not production-ready.

This document describes the current compatibility expectations before a stable
release line exists.

## Compatibility Surfaces

| Surface | Current status |
| --- | --- |
| Go internal packages | Unstable |
| Public Go semantic interfaces | Experimental |
| CLI command names | Best-effort stability |
| CLI output text | Experimental |
| Daemon HTTP API | Experimental |
| ProofList JSON | Experimental and verifier-facing |
| MALT root encoding | Experimental |
| CID codecs and root-kind metadata | Experimental |
| ArcTable internal storage | Not a public compatibility surface |
| Runtime configuration file | Experimental |
| Evaluation schemas | Versioned where practical |
| Immutable payload CIDs | Preserved according to the underlying CAS |

## Change Expectations

Before a stable release, breaking changes are allowed. Changes to public or
verifier-facing surfaces should be explicit, reviewed, and documented.

When changing any of the following, update tests and documentation in the same
PR:

- daemon request or response shapes
- CLI commands used in README or contributor workflows
- ProofList JSON fields or proof-step semantics
- root/CID encodings
- wire-format behavior
- evaluator record schemas
- UnixFS layout behavior that affects verification

Where practical, include migration notes or test vectors for serialized formats
that another implementation or stored artifact could depend on.

## Stable And Unstable Boundaries

Payload bytes remain ordinary content-addressed data. Their integrity follows
the underlying CAS and CID rules.

MALT structure roots, ProofLists, daemon routes, and evaluator schemas are still
experimental. A verifier should not assume that a ProofList JSON shape or MALT
root encoding from an unreleased commit will remain compatible with a future
commit unless release notes say so.

ArcTable records, local KV paths, materialized indexes, caches, metrics
counters, and daemon process files are operational state. They are not public
compatibility surfaces.

## Release Notes

When releases begin, release notes should call out changes to:

- CLI workflows
- daemon API routes and DTOs
- ProofList schemas
- root and CID encodings
- runtime configuration
- evaluator schemas
- known verification or body-binding limitations

Until then, users should treat `main` as an experimental integration branch and
pin commits for reproducible experiments.
