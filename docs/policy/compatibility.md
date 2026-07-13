# Compatibility Policy

MALT is an experimental reference implementation. It is runnable end to end,
but public APIs, ProofList schemas, wire formats, and deployment policies may
change. It is not production-ready.

This document describes the current compatibility expectations before a stable
release line exists.

## Compatibility Surfaces

| Surface | Current status |
| --- | --- |
| Root `malt` typed facade | Experimental `v0alpha1` |
| Go internal packages | Unstable |
| Public Go semantic interfaces | Experimental |
| CLI command names | Best-effort stability |
| CLI output text | Experimental |
| Daemon HTTP API | Experimental |
| ProofList JSON and typed read binding | Experimental `v0alpha1` and verifier-facing |
| `artifact` resolve/prove/verify envelope | Profiled `malt.artifact/v0alpha2`; incompatible revisions require a new profile |
| `SegmentPath` textual projection | `/`-joined UTF-8 segments; experimental before `v1` |
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
- root-facade query labels and request/result binding rules
- root/CID encodings
- wire-format behavior
- evaluator record schemas
- UnixFS model/runtime behavior that affects verification

Where practical, include migration notes or test vectors for serialized formats
that another implementation or stored artifact could depend on.

## Stable And Unstable Boundaries

Payload bytes remain ordinary content-addressed data. Their integrity follows
the underlying CAS and CID rules.

MALT structure roots, ProofLists, daemon routes, and evaluator schemas are still
experimental. A verifier should not assume that a ProofList JSON shape or MALT
root encoding from an unreleased commit will remain compatible with a future
commit unless release notes say so.

The `v0.0.3` source release names the typed read/result and ProofList binding
profile `v0alpha1`. The `v0.0.4` source release adds the explicit serialized
profile `malt.artifact/v0alpha2` and checked-in JSON Schemas for resolve, prove,
and verify. Consumers must reject unknown profiles, pin an exact MALT tag or
module version, and review release notes before upgrading.

ArcTable records, local KV paths, materialized indexes, caches, metrics
counters, and daemon process files are operational state. They are not public
compatibility surfaces.

## Release Notes

Release notes must call out changes to:

- CLI workflows
- daemon API routes and DTOs
- ProofList schemas
- root and CID encodings
- runtime configuration
- evaluator schemas
- known verification or body-binding limitations

Users should treat `main` as an experimental integration branch and pin exact
release tags or commits for reproducible experiments.
