# Specification

This folder holds implementation-bound protocol and schema documents.

These documents stay aligned with code, tests, and MIPs in this repository.
They are the reference layer for current behavior; MIPs propose or record
changes to this layer.

For reader-facing background on hash authentication, Merkle DAGs, and MALT's
positioning, start with [Concepts](../concepts/README.md). Keep normative
behavior, wire formats, and proof semantics in this folder.

The current transport-neutral contracts are `malt.resolve/v0alpha1` and
`malt.read/v0alpha1`. They carry operation-specific results plus ProofList
evidence. The v0.0.4 `malt.artifact/v0alpha2` profile remains frozen for
compatibility. See [MIP-1012](../mips/mip-1012-segment-path-resolution.md) and
[MIP-1013](../mips/mip-1013-client-gateway-core-boundary.md).

## Documents

- [Semantic model](./semantic.md)
- [ProofList format](./prooflist-format.md)
- [Writer receipts](./writer-receipts.md)
- [Resolve and read contracts](./resolve-read-contracts.md)
- [Frozen artifact compatibility profile](./artifacts.md)
- [Segment paths and resolution](./segment-paths.md)
- [Commitment model](./commitment.md)
- [Commitment and proof encoding](./commitment-proof-encoding.md)
- [CID and wire format](./cid-and-wire-format.md)
- [Resolve/Read conformance corpus v1](./resolve-read-conformance-v1.md)

## Notes

- `mips/` remains the proposal, decision, and process bucket. It should link to
  reference specs instead of duplicating long schema definitions.
- `policy/` holds compatibility, release, and threat-model policy.
