# MALT Roadmap

This roadmap describes the MALT core implementation repository. It is
intentionally focused on executable core behavior, public schemas, evaluator
artifacts, and maintainer workflows.

## Current Focus

MALT is an experimental reference implementation. It is runnable end to end, but
its public APIs, ProofList schemas, wire formats, and deployment policies may
change. It is not production-ready.

The near-term goal is `v0.0.3`: make MALT's application-neutral,
arc-granularity authentication contract usable without importing the reference
server or treating UnixFS as the core abstraction. The in-tree daemon/server
remains a reference/evaluation HTTP surface, not the production managed gateway
product.

## Near Term

- Review MIP-1011 and validate the module-root `malt` facade: typed `Query`,
  `ReadRequest`, `ReadResult`, and `Engine.Read`/`Apply`/`VerifyRead`.
- Keep the portable `auth/verifier` free of runtime, ArcTable, CAS, layout,
  server, and daemon dependencies.
- Validate one non-UnixFS, relation-only map path so generic maps remain valid
  without `@payload`.
- Keep `@payload` reserved and optional in generic map semantics while enforcing
  it as a UnixFS layout invariant.
- Stabilize the public `malt` CLI around root-centric add, resolve, verify, and
  daemon lifecycle workflows.
- Keep proof-bearing HTTP reads explicit and verifier-facing.
- Document and test the reference/evaluation HTTP surface boundary for
  `Read(root, query) -> result + ProofList`.
- Document the experimental ProofList and typed read binding profile as
  `v0alpha1`, without claiming a stable cross-release JSON schema.
- Keep `malt-eval` schemas, raw envelopes, manifests, and summary outputs stable
  enough for repeatable research runs.
- Preserve clear package boundaries among `auth`, `graph`, `runtime`,
  `layout/unixfs`, `server`, `storage`, and `cmd/eval/internal`.
- Build and test a gateway/external consumer against the public facade without
  importing `server` or concrete runtime packages as its API boundary.

## Active Research And Design Areas

- Range-body helper integration in clients and gateway.
- Writer receipt semantics and accounting.
- Benchmark-facing proof reporting.
- Variable-size measured list evidence.
- Incremental `malt add --root` optimization.
- Additional layouts for Pods, protocols, agent memory, and application data.
- Head publication, freshness, and multi-writer policy as application or
  deployment concerns.

## Not Yet In Scope

- Production storage-service guarantees.
- Managed global head publication.
- Multi-tenant ACL, quota, pinning, and garbage-collection policy.
- Production managed gateway behavior such as identity providers, authorization
  policy, S3 backend orchestration, billing, abuse control, or operational
  deployment.
- Stable API compatibility across releases.
- A full replacement for Kubo, IPFS, or any CAS implementation.

## v0.0.3 Release Readiness Checklist

Before `v0.0.3-rc.1`:

- `go test ./...` passes on CI.
- `go vet ./...` passes on CI.
- `cmd/cas`, `cmd/malt`, and `cmd/eval/malt-eval` build from a clean checkout.
- `README.md` quick start works from a clean checkout.
- `malt-eval run --plan examples/eval-smoke-plan.json` writes a manifest and
  summaries.
- Portable `auth/verifier` tests cover proof tampering and root, query, target,
  and range-result mismatches without runtime or storage access.
- Import-boundary tests keep ArcTable and concrete runtime/storage dependencies
  outside the trusted auth kernel.
- A generic relation-only map test passes without `@payload`, while UnixFS
  payload-binding tests still pass.
- An external consumer compiles against the root `malt` facade.
- Release notes state `v0alpha1` compatibility limits and known ProofList
  verifier limits.
- `/verify` still works through the portable verifier adapter.
- `malt resolve` returns ProofList evidence by default.
- `malt verify` verifies a resolve output or bare ProofList.
- Large-file byte-range body binding is documented through
  `layout/unixfs.VerifyRangeBody`.
- The writer core/compat split is documented in `docs/spec/writer-receipts.md`.
- Public docs avoid claims that are not implemented in the current code.
- Repository docs distinguish MALT core/reference evaluation surfaces from
  managed gateway product behavior in `DeWebProtocol/gateway`.

Tag the validated candidate as exactly `v0.0.3-rc.1`. After candidate and
external-consumer validation, tag the final source release as exactly
`v0.0.3`; do not use `v0.0.3-core-boundary` as the final version.

The current release checklist lives in
[`docs/releases/v0.0.3.md`](docs/releases/v0.0.3.md).
