# Commitment Model

MALT uses vector-commitment (VC) backends to authenticate typed graph arcs.
The semantic layer chooses the coordinate and value representation; the
backend commits, proves, and verifies already-positioned values. Payload bytes
remain ordinary CAS objects and are not stored inside the VC backend.

## Status

Experimental and implementation-bound. Current in-tree backends are KZG and
IPA, with KZG used by the default prototype profile.

## Backend Boundary

Commitment backends own:

- commitment byte generation
- proof generation
- proof verification
- update primitives when a backend supports efficient local updates

They do not own:

- map key semantics
- list index or range semantics
- path resolution
- application models and client adapters
- ArcTable materialization
- head publication or freshness policy
- payload storage or retrieval

The current public backend interfaces live under `auth/commitment`.
Semantic-facing list and map contracts live under `auth/semantic/list` and
`auth/semantic/mapping`. Portable ProofList orchestration lives under
`auth/verifier`; it selects verification-only backends from typed MALT roots
without consulting runtime state.

## Map Binding-CID Slot Model

The storage-free map commitment primitive commits canonical binding CID slots.
Each committed slot is derived from:

- the fixed domain prefix `malt:map:binding:v1:`
- canonical path key bytes
- target CID bytes

This binds map labels and values together. A verifier must not accept a bare
value proof as a map binding proof unless the committed cell also encodes the
expected key.

This model is distinct from the runtime map layout.
`runtime/semantic/mapping/radix` uses digest-keyed radix traversal and bucket
materialization, but composes that runtime traversal from the same single-step
slot proof primitive.

## Typed Roots

Commitment outputs are carried in typed MALT root CIDs. See
[CID and wire format](./cid-and-wire-format.md) for codec values and commitment
byte-size rules.

## Related Proposals

- [MIP-1005](../mips/mip-1005-kzg-map-label-domain.md) records the accepted
  binding-CID slot decision.
- [MIP-1010](../mips/mip-1010-data-authentication-core-boundary.md) records the
  historical package-boundary decision that keeps commitment primitives inside
  the data-authentication core.
- [MIP-1011](../mips/mip-1011-arc-authentication-core-contract.md) defines the
  VC-backed portable arc-authentication boundary.
