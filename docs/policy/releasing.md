# Releasing

MALT does not have a public release line yet. This document defines the release
process to use once maintainers decide that a commit is ready to tag.

## Versioning

Use semantic version tags once public releases begin:

```text
v0.1.0
v0.1.1
v0.2.0
```

Before `v1.0.0`, API and schema compatibility may still change. Release notes
must call out CLI, daemon API, ProofList, and evaluator schema changes.

## Pre-Release Checklist

Run from the repository root:

```bash
go test ./...
go vet ./...
mkdir -p bin
go build -buildvcs=false -o bin/malt ./cmd/malt
go build -buildvcs=false -o bin/malt-eval ./cmd/eval/malt-eval
bin/malt-eval run --plan examples/eval-smoke-plan.json --run-id release-smoke
```

Review:

- `README.md` quick start still matches the current CLI.
- `ARCHITECTURE.md` still matches package boundaries.
- `ROADMAP.md` separates implemented behavior from design work.
- `SECURITY.md` reporting path is still accurate.
- `cmd/eval/schemas` match current evaluator outputs.

For the current core-boundary release candidate, use
[`docs/releases/v0.0.3-core-boundary.md`](../releases/v0.0.3-core-boundary.md)
as the release-note and validation checklist. That candidate is still
experimental and should be tagged only after maintainer approval.

## Tagging

After checks pass:

```bash
git tag -a v0.1.0 -m "MALT v0.1.0"
git push origin v0.1.0
```

Create GitHub release notes with:

- summary of user-visible changes
- commit SHA
- validation commands and results
- known experimental limits
- schema or API compatibility notes
- security fixes, if any

## Artifacts

Until a release workflow is added, releases are source tags only. Users should
build from source with the Go toolchain. If binary artifacts are added later,
the release workflow should publish checksums and document target platforms.
