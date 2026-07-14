# Releasing

MALT core uses source tags for experimental releases.

## Validation

Run from the repository root:

```bash
git diff --check
test -z "$(gofmt -l $(find . -name '*.go' -not -path './vendor/*'))"
go test ./...
go vet ./...
go build -buildvcs=false ./...
scripts/build-verifier-wasm.sh dist/verifier
```

Also compile a temporary external Go module against the candidate tag or
commit. It should import only the intended public packages, at minimum:

- module-root `malt`;
- `protocol`;
- `sdk/verifier`;
- `auth/arcset/materializer` when exercising executor composition.

Review README, architecture, roadmap, schemas, compatibility policy, threat
model, and release notes. If Web publishes the WASM build, its provenance must
identify the exact MALT commit, Go toolchain, and SHA-256 checksum.

## Tag and release

Tag only the exact validated commit:

```bash
git tag -a vX.Y.Z -m "MALT vX.Y.Z"
git push origin vX.Y.Z
```

The GitHub release must include:

- user-visible and source-breaking changes;
- commit SHA;
- validation commands/results;
- profile/schema compatibility notes;
- known experimental limits.

Source tags are authoritative. Native binaries are build-from-source until a
separate workflow publishes signed platform artifacts and checksums.
