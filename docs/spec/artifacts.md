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
| `verify` | one complete artifact | `{profile, valid}` | Check envelope bindings and all portable proof evidence. |

The reference HTTP projection is:

```text
POST /v1/artifacts/resolve
POST /v1/artifacts/prove
POST /v1/artifacts/verify
```

HTTP is only one projection. RPC and SDK integrations should carry the same
typed fields directly and should not reconstruct path meaning from a URL.

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
- root and target are valid CIDs;
- the artifact root equals the ProofList root;
- the final ProofList target equals the artifact target;
- a resolve ProofList query equals the canonical projection of its segments;
- a prove query and optional range segments match the proof evidence; and
- the portable verifier accepts every cryptographic/semantic proof step.

Schema validation is not proof verification. A structurally valid JSON object
must still pass `artifact.Verify` or an equivalent conforming implementation.

Resolution proofs are existential. They prove the ordered authenticated
derivation returned by the execution engine. They do not prove that the
derivation was unique, globally shortest, globally longest, or preferred by an
application. Candidate choice can affect behavior and availability, but an
accepted artifact always authenticates the returned path-to-target binding.

## Legacy Transport Surfaces

The legacy `malt resolve` DTO, bare ProofList input, `/verify`, and proof headers
remain available as reference/compatibility surfaces in `v0.0.4`. New gateway,
daemon, and SDK integrations should use the profiled artifact contract so the
root, query, target, and evidence travel together.

## Schemas

- `artifact.schema.json`
- `resolve-request.schema.json`
- `prove-request.schema.json`
- `verify-request.schema.json`
- `verify-result.schema.json`

Schema `$id` values are rooted at
`https://deweb.world/schemas/malt/artifact/v0alpha2/`. The repository copy and
embedded package data are authoritative for this release.
