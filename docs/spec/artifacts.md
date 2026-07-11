# Artifacts and Schemas

This document records the current artifact boundaries for CLI JSON, ProofList
JSON, content-proof headers, and evaluator schemas.

## Status

Experimental and implementation-bound. The typed read/result and ProofList
binding rules form the `v0alpha1` artifact profile introduced in `v0.0.3`. Only
evaluator schemas currently have machine-readable JSON Schema files in the
repository; resolve JSON and bare ProofList JSON remain documented Go DTO
artifacts rather than stable named JSON schemas.

## Current Artifact Surfaces

| Surface | Current owner | Stability |
| --- | --- | --- |
| `malt resolve` JSON | `api/http.ResolveResponse` | Experimental |
| typed core read | root `malt.ReadRequest` and `malt.ReadResult` | Experimental `v0alpha1` binding contract |
| bare ProofList JSON | `auth/proof/prooflist.ProofList` | Experimental `v0alpha1` verifier-facing artifact |
| content proof headers | `server` and `sdk/client` | Experimental |
| evaluator records | `cmd/eval/schemas` | Versioned where practical |

## Resolve JSON

`malt resolve` prints the daemon `ResolveResponse` shape:

```json
{
  "target": "cid-string",
  "prooflist": {}
}
```

The `prooflist` field is omitted when proof generation is disabled. The target
CID is not self-authenticating as a path result; callers need the root, query,
target, and ProofList to verify the binding.

The generic Go facade represents those inputs as `malt.ReadRequest` and
`malt.ReadResult`, then checks them with `Engine.VerifyRead` or package-level
`malt.VerifyRead`. The HTTP DTO remains a reference transport projection rather
than the generic core contract.

## Bare ProofList JSON

`malt verify --prooflist` accepts a bare ProofList JSON document and also accepts
resolve JSON containing a `prooflist` field. The structural shape is documented
in [ProofList format](./prooflist-format.md).

## Content Proof Headers

Default content reads return proof evidence in headers instead of the response
body:

- `X-Malt-ProofList: <base64url-json>`
- `X-Malt-ProofList-Encoding: base64url-json`

Clients must decode the header before running ProofList verification.

## Machine-Readable Schemas

Evaluator JSON schemas live under `cmd/eval/schemas` and are embedded in the
`malt-eval schema` command. Resolve JSON and bare ProofList JSON do not yet have
stable named schema files.

The `v0.0.3` decision is to keep that boundary: document the `v0alpha1` resolve,
typed read, and ProofList shapes, but avoid stable named schema files until the
project is ready to commit to artifact compatibility. JSON shape validation is
still separate from portable ProofList verification through `auth/verifier`.

If resolve or ProofList artifacts are promoted to stable named schemas, the
same change should:

- add the schema file in an implementation-owned path
- add schema listing or discovery if needed by CLI users
- update CLI help and examples
- add tests that validate representative artifacts
- keep schema validation separate from proof verification

## Related Proposals

[MIP-1004](../mips/mip-1004-resolve-prooflist-artifact-schema.md) tracks the
decision about whether resolve JSON, bare ProofList JSON, and content proof
metadata need stable named schemas.
