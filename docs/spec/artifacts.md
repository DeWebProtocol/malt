# Resolve, Prove, And Verify Artifacts

This document defines the transport-neutral artifact contract introduced in
MALT `v0.0.4`.

## Profile And Ownership

The serialized profile is `malt.artifact/v0alpha2`. The Go package is the
unversioned `github.com/dewebprotocol/malt/artifact`; version identifiers live
in artifact envelopes and schema identifiers rather than package names.

The profile is stable by explicit discriminator: consumers must reject an
unknown `profile` instead of guessing compatibility. It remains pre-`v1` and a
future incompatible shape will use a different profile value.

The checked-in JSON Schemas live in `artifact/schemas/`, are embedded in the Go
package, and are available through `artifact.Schema` and
`artifact.SchemaNames`.

## Operations

| Operation | Request | Result | Meaning |
| --- | --- | --- | --- |
| `resolve` | trusted root plus `segments` | artifact with `query.kind=path` | Authenticate one complete derivation for the supplied segment path. |
| `prove` | trusted root plus one primitive typed query | artifact with `query.kind=map_key`, `list_index`, or `list_range` | Produce evidence for one primitive semantic query. |
| `verify` | one complete artifact | local success/error, or diagnostic `{profile, valid}` | Check envelope bindings and all portable proof evidence on the client. |

The reference HTTP projection is:

```text
POST /v1/artifacts/resolve
POST /v1/artifacts/prove
POST /v1/artifacts/verify
```

HTTP is only one projection. RPC and SDK integrations should carry the same
typed fields directly and should not reconstruct path meaning from a URL.
The remote verify route is diagnostic/conformance only. The authoritative
acceptance path is local `sdk/verifier.Verifier.Verify` or a conforming browser,
mobile, or language SDK implementation.

The browser/WASM and cross-language local-verifier boundary carries a separate
trusted root and caller expectation so an untrusted artifact cannot select its
own trust anchor or substitute another authenticated request under that root:

```json
{
  "profile": "malt.artifact/v0alpha2",
  "trusted_root": "b...",
  "expected": {
    "operation": "resolve",
    "query": {"kind": "path", "segments": ["a", "b"]}
  },
  "artifact": {"profile": "malt.artifact/v0alpha2", "operation": "resolve"}
}
```

The complete artifact is required. The abbreviated value above only shows the
envelope. `trusted_root`, `expected.operation`, `expected.query`, and an
optional `expected.target` are caller-selected inputs. They must match the
artifact before any proof is accepted.

## Resolve Request

```json
{
  "profile": "malt.artifact/v0alpha2",
  "root": "b...",
  "segments": ["a", "b", "c", "d"]
}
```

`segments` is the canonical interface. The slash form `a/b/c/d` is only the
MALT textual projection. See [Segment paths and resolution](./segment-paths.md).

The resulting artifact binds the request, target, and ProofList:

```json
{
  "profile": "malt.artifact/v0alpha2",
  "operation": "resolve",
  "root": "b...",
  "query": {"kind": "path", "segments": ["a", "b", "c", "d"]},
  "target": "b...",
  "prooflist": {"root": {"/": "b..."}, "query": "a/b/c/d", "steps": []}
}
```

The abbreviated `steps` value above is not a real non-empty-path proof. A real
artifact carries every selected proof step. Only a zero-segment root-identity
resolve has no steps, in which case `target` must equal `root`.

The outer artifact uses CID strings for SDK-friendly request/result fields.
The nested ProofList preserves the existing `go-cid` DAG-JSON link form
`{"/":"cid"}` for its root, step endpoints, and range segment CIDs.

## Primitive Prove Request

Map key:

```json
{
  "profile": "malt.artifact/v0alpha2",
  "root": "b...",
  "query": {"kind": "map_key", "segments": ["account", "name"]}
}
```

List index and measured range queries use `index`, or `start` plus optional
`end`, respectively. A prove artifact may also carry `range_segments` for the
ordered CIDs authenticated by a measured-list range proof.

`prove` does not accept `query.kind=path`; multi-step composition belongs to
`resolve`. Conversely, `resolve` does not erase primitive query kinds.

## Verification Contract

Verification checks all of the following:

- request and artifact profiles are recognized;
- local caller expectations match the artifact operation, query, and optional
  target;
- root and target are valid CIDs;
- the artifact root equals the ProofList root;
- the final ProofList target equals the artifact target;
- a resolve ProofList query equals the canonical projection of its segments;
- a prove query and optional range segments match the proof evidence; and
- the portable verifier accepts every cryptographic/semantic proof step.

Schema validation is not proof verification. The authoritative client path must
pass `sdk/verifier`, the published browser WASM verifier, or an equivalent
implementation that binds caller-selected expectations before cryptographic
verification. Lower-level `artifact.Verify` checks artifact-internal bindings
only; callers using it directly must first bind an independently selected root,
operation, query, and optional target.

Resolution proofs are existential. They prove the ordered authenticated
derivation returned by the execution engine. They do not prove that the
derivation was unique, globally shortest, globally longest, or preferred by an
application. Candidate choice can affect behavior and availability, but an
accepted artifact always authenticates the returned path-to-target binding.

## Legacy Transport Surfaces

The legacy `malt resolve` DTO, bare ProofList input, `/verify`, and proof headers
remain available as reference/compatibility surfaces. New gateway,
reference-executor, and SDK integrations should use the profiled artifact
contract so the root, query, target, and evidence travel together. A remote
`valid` boolean never replaces the client's local trust decision.

## Schemas

- `artifact.schema.json`
- `local-verify-request.schema.json`
- `local-verify-result.schema.json`
- `resolve-request.schema.json`
- `prove-request.schema.json`
- `verify-request.schema.json`
- `verify-result.schema.json`

Schema `$id` values are rooted at
`https://deweb.world/schemas/malt/artifact/v0alpha2/`. The repository copy and
embedded package data are authoritative. Stable root-identity examples,
including the local verifier's separate trust and request expectation, live
under `artifact/testdata/v0alpha2/`.
