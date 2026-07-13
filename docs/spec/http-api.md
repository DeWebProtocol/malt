# HTTP API

This document describes the root-relative reference/evaluation HTTP surface and DTO
boundaries. It is a reference for implementation docs, not a stable public API
contract.

## Status

Experimental and implementation-bound. Breaking route or DTO changes are
allowed before a stable release, but they should update tests and docs in the
same PR.

Most of this API is a reference transport projection. The application-neutral
core contract is the module-root `malt` facade; managed gateways do not need to
import `server` or reproduce the local runtime routes. The three
`/v1/artifacts/*` routes are the stable transport projection for new gateway,
reference-executor, and SDK integrations. Their schemas are defined in
[Artifacts and schemas](./artifacts.md).

## Core Routes

| Route | Method | Purpose |
| --- | --- | --- |
| `/health` | `GET` | Runtime health check. |
| `/_lifecycle/identity` | `GET` | Local managed reference-executor identity check. |
| `/metrics` | `GET` | Runtime evaluation counters. |
| `/metrics:reset` | `POST` | Reset runtime evaluation counters. |
| `/verify` | `POST` | Diagnostic/conformance verification of a ProofList. |
| `/v1/artifacts/resolve` | `POST` | Resolve a segment array and return a profiled proof-carrying artifact. |
| `/v1/artifacts/prove` | `POST` | Prove one primitive typed map/list query. |
| `/v1/artifacts/verify` | `POST` | Diagnostic/conformance verification of a complete artifact. |
| `/{root}/_mutate` | `POST` | Apply a root-relative semantic mutation. |
| `/_unixfs?path=...` | `POST` | Create a new UnixFS-style root from uploaded payload data. |
| `/resolve/{root}` and `/resolve/{root}/{path...}` | `GET` | Resolve a target CID and optional ProofList. |
| `/{root}` and `/{root}/{path...}` | `GET` | Read content or directory JSON with optional proof headers. |
| `/{root}` and `/{root}/{path...}` | `HEAD` | Return stat headers without proof metadata. |
| `/{root}/{path...}` | `POST` | UnixFS application-adapter convenience write. |
| `/_` | `POST` | Create a low-level structure from an arc set. |

Removed lineage and batch-update public routes intentionally return removed or
not-found behavior and should not be documented as active API.

## Proof Transport

Resolve responses include proof evidence by default:

```json
{
  "target": "cid-string",
  "prooflist": {}
}
```

Content responses place proof evidence in headers:

- `X-Malt-ProofList`
- `X-Malt-ProofList-Encoding: base64url-json`
- `Vary: X-Malt-Proof`

Clients can omit default proof generation with either `?proof=false` or
`X-Malt-Proof: omit`. `HEAD` responses are stat-only and do not include proof
headers.

Range content reads use standard byte range headers where supported:

- request: `Range: bytes=start-end`
- response: `Accept-Ranges: bytes`
- partial response: `Content-Range: bytes start-end/total`

See [ProofList format](./prooflist-format.md) for proof semantics. For
large-file range responses, clients authenticate ProofList metadata and segment
CIDs locally through `sdk/verifier`, then call `sdk/unixfs.VerifyRangeBody` or
an equivalent segment-byte binding check. `/verify` is retained only as a
diagnostic/conformance endpoint and must not supply the client's trust decision.

## Main DTOs

Current DTOs live in `api/http/types.go`.

### ResolveResponse

| JSON field | Meaning |
| --- | --- |
| `target` | Resolved target CID string. |
| `prooflist` | Optional ProofList evidence. |

### SemanticMutationResponse

| JSON field | Meaning |
| --- | --- |
| `base_root` | Caller-supplied root for the mutation. |
| `new_root` | Resulting root after writer application. |
| `result_root` | Optional application-level result root. |
| `delta_count` | Number of semantic deltas applied. |
| `arc_count` | Number of canonical arc changes applied. |
| `malt_object_count` | Optional layout count of produced MALT objects. |
| `map_count` | Optional layout count of map objects. |
| `list_count` | Optional layout count of list objects. |

Receipt counts are operational accounting, not correctness proofs. See
[Writer receipts](./writer-receipts.md).

### PathStatResponse

| JSON field | Meaning |
| --- | --- |
| `kind` | `file` or `dir`. |
| `storage_kind` | `raw`, `list`, or `map`. |
| `key` | Terminal key CID. |
| `payload` | Optional directory payload CID. |
| `size` | File size when known. |
| `entries` | Directory entries when available. |

### VerifyRequest / VerifyResponse

`VerifyRequest` carries one `prooflist` field. `VerifyResponse` returns one
boolean `valid` field. Both remote verify routes set
`X-Malt-Verification-Role: diagnostic`.

Schema validation is not proof verification. JSON shape checks can reject
malformed artifacts, but semantic and cryptographic verification for an
acceptance decision must run locally through `sdk/verifier`/`auth/verifier`.
The `graph/verifier` package and remote routes are compatibility adapters.

## CORS Boundary

Browser CORS, when configured, exposes read/proof surfaces, `POST /verify`, and
UnixFS browser write routes. Admin and semantic-mutation routes are not exposed
through browser CORS by default.

## Related Proposals

- [MIP-1002](../mips/mip-1002-writer-receipt-accounting.md) tracks receipt
  accounting decisions.
- [MIP-1004](../mips/mip-1004-resolve-prooflist-artifact-schema.md) defines the
  profiled resolve/resolve_payload/prove/verify artifacts and named schemas.
- [MIP-1011](../mips/mip-1011-arc-authentication-core-contract.md) defines the
  transport-independent typed read and verification boundary.
