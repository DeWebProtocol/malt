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

The completed `v0.0.3` validation record lives in
[`docs/releases/v0.0.3.md`](../releases/v0.0.3.md). It includes a
portable-verifier smoke, a relation-only map test, import-boundary checks, an
external-consumer compile test, evaluator smoke, and isolated CLI proof smoke.
Future releases should add a new release-note file with equivalent gates rather
than editing the historical `v0.0.3` record.

## Tagging

For a new release, tag the validated candidate first using a standard
prerelease suffix:

```bash
git tag -a vX.Y.Z-rc.1 -m "MALT vX.Y.Z-rc.1"
git push origin vX.Y.Z-rc.1
```

After candidate and external-consumer validation, tag the exact same approved
release commit as `vX.Y.Z`, or rerun validation if the commit changes. The
`v0.0.3` release followed this process with `v0.0.3-rc.1` and `v0.0.3`; the
historical `v0.0.3-core-boundary` milestone was not used as a tag.

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
