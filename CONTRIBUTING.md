# Contributing to MALT

MALT is a research prototype for authenticated mutable structure over immutable
CAS payloads. Contributions should keep that boundary clear: payload storage is
ordinary CAS, while MALT owns authenticated structure, proof generation, and
verification.

## Start Here

Before opening a pull request:

1. Read [README.md](./README.md) for the project shape.
2. Read [ARCHITECTURE.md](./ARCHITECTURE.md) for current package boundaries.
3. Read [CODING_STANDARDS.md](./CODING_STANDARDS.md) for Go conventions.
4. Check [ROADMAP.md](./ROADMAP.md) before starting larger work.

For design-level changes, open an issue first. Examples include new semantic
operations, ProofList schema changes, new ArcTable modes, root publication
policy, evaluator schema changes, and new application layouts.

## Development Setup

Use Go 1.24.6 or newer.

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
- update [docs/evaluation.md](./docs/evaluation.md)
- keep durable results under `result/<run_id>`
- keep disposable workspace data under `output/<run_id>`
- add focused tests for schema and normalization behavior

## Maintainer Automation

The repository uses GitHub Actions for Go tests and vetting. Once the repository
is public, maintainers may also use issue triage, PR review, dependency-update,
and release automation to reduce maintenance load. Automation should make review
easier, not replace design judgment for semantic or proof changes.
