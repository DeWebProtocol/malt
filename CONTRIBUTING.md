# Contributing to MALT

MALT is an experimental reference implementation for authenticating structured
data whose relationships can be normalized into graph-shaped nodes and
relations. It is runnable end to end, but public APIs, ProofList schemas, wire
formats, and deployment policies may change. MALT is not production-ready.

Contributions should keep the core boundary clear: MALT owns authenticated
structure, proof generation, and verification, while immutable payload storage
can remain ordinary CAS.

## Start Here

Before opening a pull request:

1. Read [README.md](./README.md) for the project shape.
2. Read [ARCHITECTURE.md](./ARCHITECTURE.md) for current package boundaries.
3. Check [ROADMAP.md](./ROADMAP.md) before starting larger work.

For design-level changes, open an issue first. Examples include new semantic
operations, ProofList schema changes, new ArcTable modes, root publication
policy, evaluator schema changes, and new application layouts.

Changes that affect public or verifier-facing surfaces should update
documentation and tests in the same PR. This includes:

- public daemon APIs or CLI behavior
- ProofList schemas and proof-step semantics
- root or CID encodings
- wire formats and serialized request/response shapes
- compatibility policy
- evaluator schemas
- application layout behavior

When serialized or verifier-facing formats change, include focused tests and,
where practical, migration notes or test vectors.

## Development Setup

Use Go 1.25.7 or newer.

```bash
go test ./...
go vet ./...
```

Build local binaries when you need to exercise the daemon lifecycle:

```bash
mkdir -p bin
go build -buildvcs=false -o bin/malt ./cmd/malt
go build -buildvcs=false -o bin/malt-eval ./cmd/eval/malt-eval
```

The managed daemon uses the current executable path, so `bin/malt start` is the
recommended local workflow for daemon testing.

## Pull Requests

Keep PRs focused. A good PR usually changes one boundary or behavior and carries
the tests needed to prove it.

Include:

- what changed
- why it changed
- how you tested it
- any follow-up design questions

For Go code changes, run:

```bash
go test ./...
go vet ./...
```

Documentation-only changes do not require the full Go suite, but run a targeted
command when the documentation claims a command, schema, or workflow.

## Coding Expectations

- Run `gofmt` on Go code before committing.
- Name tests around behavior, and prefer table-driven tests when covering
  several cases of the same behavior.
- Keep benchmark files named `*_benchmark_test.go`.
- Prefer semantic names over storage-mechanism names.
- Keep `list` and `map` as semantic abstractions.
- Keep resolver and writer as graph ports over those semantics.
- Keep `layout/unixfs` as an application layout, not the core definition of
  MALT.
- Treat `@payload` as a reserved map semantic binding.
- Keep compatibility and benchmark code under `cmd/eval/internal` when it is not
  part of the product runtime.

## Evaluation Changes

Evaluator outputs are part of the public research surface. When changing
`malt-eval` record shapes:

- update the matching schema under `cmd/eval/schemas`
- update [docs/evaluation/README.md](./docs/evaluation/README.md)
- keep durable results under `result/<run_id>`
- keep disposable workspace data under `output/<run_id>`
- add focused tests for schema and normalization behavior

## Maintainer Automation

The repository uses GitHub Actions for Go tests and vetting. Maintainers may
also use issue triage, PR review, dependency-update, and release automation to
reduce maintenance load. Automation should make review easier, not replace
design judgment for semantic or proof changes.
