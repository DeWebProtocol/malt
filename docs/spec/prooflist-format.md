# ProofList Format

ProofList is the verifier-facing read artifact returned by MALT resolve and
content reads. It records the ordered proof path from a trusted root to a
resolved target.

## Status

Experimental and implementation-bound. The JSON shape is verifier-facing but
not yet a stable release contract.

## Envelope

The current Go shape is `auth/proof/prooflist.ProofList`:

| Field | JSON | Meaning |
| --- | --- | --- |
| `Root` | `root` | Trusted MALT structure root for this proof. |
| `Query` | `query` | Optional query/path string the proof was assembled for. |
| `Steps` | `steps` | Ordered proof steps. |

`ValidateShape(RequireSteps())` rejects an undefined root, an empty step list,
unknown step kinds, undefined `from` or `target` CIDs, and any step whose
`from` CID does not continue from the current target.

## Step Kinds

| Kind | Role |
| --- | --- |
| `map_step` | Authenticates a keyed map relation. |
| `payload_binding` | Authenticates the reserved terminal `@payload` map binding. |
| `list_index` | Authenticates one list index binding. |
| `list_range` | Authenticates measured-list byte range metadata and covered segments. |
| `blob_binding` | Binds a resolved structure target to an immutable blob target. |
| `implicit_block` | Compatibility evidence for implicit block traversal. |
| `legacy_unknown` | Legacy adapter marker retained for older evidence. |

List evidence is label-checked: `list_index` must carry
`evidence_kind="structure"` and `evidence_backend="list"`, while `list_range`
must carry `evidence_kind="structure"` and
`evidence_backend="measured_list"`.

## Step Fields

Each step has:

- `kind`
- `from`
- `target`
- optional query fields: `query`, `coordinate`, `path`, `index`
- optional range fields: `start`, `end`, `length`, `child_count`,
  `total_size`, `chunk_size`, `segments`
- optional proof labels: `evidence_kind`, `evidence_backend`
- opaque proof bytes: `evidence`, `proof`

The byte fields are JSON-encoded by Go's standard `encoding/json` rules for
`[]byte`, currently base64 strings.

## Ordering Rules

Steps form a linear chain unless they are terminal list evidence:

1. The first step starts at `root`.
2. Non-list traversal steps advance the current target to `step.target`.
3. List index or list range evidence may appear at the terminal phase.
4. No later traversal step may appear after list index or list range evidence.

This shape validation is structural. Cryptographic or semantic verification is
owned by the concrete backend that emitted each proof payload.

## HTTP Transport

`GET /resolve/{root}/{path}` returns JSON:

```json
{
  "target": "cid-string",
  "prooflist": { "root": "cid-string", "steps": [] }
}
```

`prooflist` is omitted when the caller uses `?proof=false` or
`X-Malt-Proof: omit`.

Default content reads, `GET /{root}/{path}`, return body bytes or directory
JSON and place proof evidence in headers:

- `X-Malt-ProofList: <base64url-json>`
- `X-Malt-ProofList-Encoding: base64url-json`
- `Vary: X-Malt-Proof`

`HEAD /{root}/{path}` is stat-only and does not return ProofList headers.

## Range Evidence

Large-file byte-range reads currently use:

1. map/path proof to the file object
2. terminal `@payload` proof to the list root
3. one measured-list `list_range` step over the requested byte interval

The `list_range` step carries fixed chunk metadata, covered segment CIDs, and
metadata/index proof bytes. Verifiers must reject range proofs that shift byte
boundaries, omit covered segment bindings, or mismatch the measured metadata.

The `list_range` step authenticates range metadata and segment CIDs, not raw
HTTP body bytes by itself. A verifier that accepts returned range body bytes
must:

1. verify the ProofList against the trusted root,
2. fetch or otherwise resolve each authenticated segment CID, and
3. call `layout/unixfs.VerifyRangeBody(pl, body, start, end, fetch)` or an
   equivalent byte-binding check before trusting the body.

`VerifyRangeBody` rejects shifted ranges, missing range evidence, segment CID
mismatches, short segment data, and tampered returned bytes.

## Related Proposals

- [MIP-1003](../mips/mip-1003-prooflist-verification-schema.md) tracks verifier-contract
  formalization and range-body helper integration.
- [MIP-1006](../mips/mip-1006-variable-size-measured-list-evidence.md) tracks a
  future variable-size measured-list model.
