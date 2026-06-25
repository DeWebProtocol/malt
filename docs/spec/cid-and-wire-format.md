# CID and Wire Format

MALT uses typed CIDv1 roots for authenticated map and list structure. Payload
objects remain ordinary CAS CIDs.

## Status

Experimental and implementation-bound. The current codec values are locked for
the prototype but are not yet a stable release contract.

## MALT Root Codecs

MALT uses the multicodec Private Use Area `0x300000-0x3FFFFF` for typed
structure roots:

| Name | Value | Semantic kind | Backend |
| --- | ---: | --- | --- |
| `malt-map-kzg` | `0x300001` | map | KZG |
| `malt-list-kzg` | `0x300002` | list | KZG |
| `malt-map-ipa` | `0x300003` | map | IPA |
| `malt-list-ipa` | `0x300004` | list | IPA |

The implementation owner is `wire/maltcid`.

## Commitment Encoding

Typed MALT root CIDs use CIDv1 with an identity multihash carrying the raw
commitment bytes.

Current commitment byte sizes:

| Backend | Size |
| --- | ---: |
| KZG | 48 bytes |
| IPA | 32 bytes |

Code that creates or validates typed roots must reject commitment byte lengths
that do not match the selected backend.

## Root Classification

`wire/maltcid` exposes helpers for:

- detecting whether a CID is a MALT structure root
- extracting semantic kind: `map`, `list`, or `unknown`
- extracting backend kind: `kzg`, `ipa`, or `unknown`
- extracting raw commitment bytes from a typed MALT root CID
- comparing commitment bytes across typed roots

Verifiers should reject roots or proof steps whose semantic kind, backend kind,
or expected target type does not match the query and evidence.

## Payload CIDs

Immutable file bytes, chunks, manifests, and other payload objects keep their
ordinary CAS CIDs. MALT authenticates bindings to those payload CIDs; it does
not redefine the payload CID format.

## Related Documents

- [Commitment model](./commitment.md)
- [ProofList format](./prooflist-format.md)
- [Compatibility policy](../policy/compatibility.md)
