# MALT Roadmap

This roadmap describes the implementation repository. It is intentionally
focused on executable behavior, public schemas, and maintainer workflows.

## Current Focus

MALT is an experimental reference implementation. It is runnable end to end, but
its public APIs, ProofList schemas, wire formats, and deployment policies may
change. It is not production-ready.

The near-term goal is to make the repository easier to understand, validate,
and integrate experimentally while preserving honest boundaries around unstable
schemas and deployment policy.

## Near Term

- Stabilize the public `malt` CLI around root-centric add, resolve, verify, and
  daemon lifecycle workflows.
- Keep proof-bearing HTTP reads explicit and verifier-facing.
- Tighten `ProofList` verifier documentation for path resolution, terminal
  `@payload` binding, and list-backed byte-range evidence.
- Keep `malt-eval` schemas, raw envelopes, manifests, and summary outputs stable
  enough for repeatable research runs.
- Preserve clear package boundaries among `auth`, `graph`, `runtime`,
  `layout/unixfs`, `server`, `storage`, and `cmd/eval/internal`.
- Add release discipline once the first public tag is ready.

## Active Research And Design Areas

- Response-body binding for large-file byte ranges.
- Writer receipt semantics and accounting.
- Benchmark-facing proof reporting.
- Variable-size measured list evidence.
- Incremental `malt add --root` optimization.
- Head publication, freshness, and multi-writer policy as application or
  deployment concerns.

## Not Yet In Scope

- Production storage-service guarantees.
- Managed global head publication.
- Multi-tenant ACL, quota, pinning, and garbage-collection policy.
- Stable API compatibility across releases.
- A full replacement for Kubo, IPFS, or any CAS implementation.

## Release Readiness Checklist

Before the first public release tag:

- `go test ./...` passes on CI.
- `go vet ./...` passes on CI.
- `README.md` quick start works from a clean checkout.
- `malt-eval run --plan examples/eval-smoke-plan.json` writes a manifest and
  summaries.
- Security reporting path is enabled in GitHub repository settings.
- Release notes state experimental limits and any known ProofList verifier
  contract TODOs.
- Public docs avoid claims that are not implemented in the current code.
