# Releasing

MALT uses source tags for experimental releases. This document defines the
process for selecting and validating the exact commit to tag.

## Versioning

Use semantic version tags and standard prerelease identifiers:

```text
v0.0.3-rc.1
v0.0.3
v0.1.0
```

Before `v1.0.0`, API and schema compatibility may still change. Release notes
must call out CLI, daemon API, ProofList, and evaluator schema changes.

## Pre-Release Checklist

Run from the repository root:

```bash
go test ./...
go vet ./...
mkdir -p bin
go build -buildvcs=false -o bin/cas ./cmd/cas
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

For the current release candidate, use
[`docs/releases/v0.0.3.md`](../releases/v0.0.3.md) as the release-note and
validation checklist. It requires a portable-verifier smoke, a relation-only
map test, import-boundary checks, and an external-consumer compile test in
addition to the repository-wide commands.

## Tagging

For `v0.0.3`, tag the validated candidate first:

```bash
git tag -a v0.0.3-rc.1 -m "MALT v0.0.3-rc.1"
git push origin v0.0.3-rc.1
```

After candidate and external-consumer validation, tag the exact same approved
release commit as `v0.0.3`, or rerun validation if the commit changes. Do not
use `v0.0.3-core-boundary` as the final tag.

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
