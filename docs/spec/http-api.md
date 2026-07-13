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
import `server` or reproduce the local runtime routes. The operation-specific
`/v1/resolve` and `/v1/read` routes are the transport projection for new
gateway, reference-executor, and SDK integrations. Their schemas are defined
in [Resolve and read contracts](./resolve-read-contracts.md).

## Core Routes

| Route | Method | Purpose |
| --- | --- | --- |
| `/health` | `GET` | Runtime health check. |
| `/_lifecycle/identity` | `GET` | Local managed reference-executor identity check. |
| `/metrics` | `GET` | Runtime evaluation counters. |
| `/metrics:reset` | `POST` | Reset runtime evaluation counters. |
| `/verify` | `POST` | Diagnostic/conformance verification of a ProofList. |
| `/v1/resolve` | `POST` | Resolve a segment array and return `malt.resolve/v0alpha1` target plus ProofList. |
| `/v1/read` | `POST` | Execute one `malt.read/v0alpha1` primitive map/list query. |
| `/v1/verify/resolve` | `POST` | Diagnostic verification of one resolve request/result pair. |
| `/v1/verify/read` | `POST` | Diagnostic verification of one read request/result pair. |
| `/v1/artifacts/resolve`, `/prove`, `/verify` | `POST` | Frozen `malt.artifact/v0alpha2` compatibility routes. |
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

The operation-specific resolve result always includes proof evidence:

```json
{
  "profile": "malt.resolve/v0alpha1",
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

The operation-specific serialized DTOs live in `protocol/`. Legacy content,
stat, mutation, and compatibility DTOs live in `api/http/types.go`.

### ResolveRequest / ResolveResult

| JSON field | Meaning |
| --- | --- |
| `profile` | Exact `malt.resolve/v0alpha1` discriminator. |
| `root` | Caller-selected trusted root in the request. |
| `segments` | Canonical segment array in the request. |
| `target` | Resolved target CID string. |
| `prooflist` | Required ProofList evidence in the result. |

`[]` means root identity. Payload selection is explicit: append `@payload` to
the segment array. The legacy GET `/resolve/{root}/{path...}` materializes
UnixFS/map payloads and is not the generic core resolve contract.

### ReadRequest / ReadResult

`malt.read/v0alpha1` carries one `map_key`, `list_index`, or `list_range` query.
The result contains `target`, optional `range_segments`, and required
`prooflist`. “Read” replaces the misleading generic operation name “prove”:
the proof is evidence for the read result, not a separate semantic operation.

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
boolean `valid` field. All remote verify routes set
`X-Malt-Verification-Role: diagnostic`.

Schema validation is not proof verification. JSON shape checks can reject
malformed payloads, but semantic and cryptographic verification for an
acceptance decision must run locally through `sdk/verifier`/`auth/verifier`.
The `graph/verifier` package and remote routes are compatibility adapters.

## CORS Boundary

Browser CORS, when configured, exposes the resolve/read contracts, their
diagnostic verify routes, legacy read/proof surfaces, and UnixFS browser write
routes. Admin and semantic-mutation routes are not exposed through browser
CORS by default.

## Related Proposals

- [MIP-1002](../mips/mip-1002-writer-receipt-accounting.md) tracks receipt
  accounting decisions.
- [MIP-1004](../mips/mip-1004-resolve-prooflist-artifact-schema.md) records the
  frozen v0.0.4 artifact compatibility profile.
- [MIP-1013](../mips/mip-1013-client-gateway-core-boundary.md) defines the
  current operation-specific client/gateway/core boundary.
- [MIP-1011](../mips/mip-1011-arc-authentication-core-contract.md) defines the
  transport-independent typed read and verification boundary.
