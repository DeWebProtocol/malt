# ProofList Format

ProofList is the verifier-facing read artifact returned by MALT resolve and
content reads. It records the ordered proof path from a trusted root to a
resolved target.

## Status

The current contract profile is `v0alpha1`. It is experimental and
implementation-bound: the JSON shape is verifier-facing but not a stable
cross-release contract. The envelope does not yet carry an embedded version
field or have a stable named JSON Schema.

Portable verification is implemented by `auth/verifier`. It performs no
ArcTable, CAS, runtime, layout, server, daemon, or network lookup.

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
| `payload_binding` | Authenticates the optional reserved terminal `@payload` map binding when a layout uses it. |
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

This shape validation is structural. `auth/verifier` then selects a
verification-only backend from the typed root and checks the map/list evidence.
The verifier must reject unsupported or mismatched evidence labels rather than
falling back to runtime state.

## Typed Read Binding

The root `malt` facade binds a ProofList to a typed read before accepting it:

```text
ReadRequest{Root, Query}
ReadResult{Target, Segments, ProofList}
VerifyRead(request, result)
```

`VerifyRead` requires the ProofList root and query to match the request, exactly
one primitive proof step whose kind and coordinate match the typed query, the
last proof target to match `ReadResult.Target`, and range segments to match
`ReadResult.Segments`. This rejects cross-kind confusion such as satisfying a
list query with a valid map proof for a similarly named key. Only after those
bindings pass does it delegate cryptographic and semantic checks to
`auth/verifier`.

The `v0alpha1` `Query.Kind` values are `map_key`, `list_index`, and
`list_range`. Their current ProofList `query` labels are:

| Query kind | ProofList `query` label |
| --- | --- |
| `map_key` | canonical map key/path text |
| `list_index` | `list:<index>` |
| `list_range` | `range:<start>:<end>`; an empty end means authenticated EOF |

These encodings remain experimental and consumers must pin a MALT release.

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

UnixFS large-file byte-range reads currently use:

1. map/path proof to the file object
2. terminal `@payload` proof to the list root
3. one measured-list `list_range` step over the requested byte interval

The `list_range` step carries fixed chunk metadata, covered segment CIDs, and
metadata/index proof bytes. Verifiers must reject range proofs that shift byte
boundaries, omit covered segment bindings, or mismatch the measured metadata.

`@payload` is reserved but optional in generic map state. The UnixFS layout
requires it and therefore emits the terminal payload-binding step shown above;
a generic relation-only map ProofList does not need such a step.

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
- [MIP-1011](../mips/mip-1011-arc-authentication-core-contract.md) defines the
  typed read/result binding around this artifact.
