# Specification

This folder holds implementation-bound protocol and schema documents.

These documents stay aligned with code, tests, and MIPs in this repository.
They are the reference layer for current behavior; MIPs propose or record
changes to this layer.

For reader-facing background on hash authentication, Merkle DAGs, and MALT's
positioning, start with [Concepts](../concepts/README.md). Keep normative
behavior, wire formats, and proof semantics in this folder.

## Documents

- [Semantic model](./semantic.md)
- [ProofList format](./prooflist-format.md)
- [Writer receipts](./writer-receipts.md)
- [Artifacts and schemas](./artifacts.md)
- [Commitment model](./commitment.md)
- [CID and wire format](./cid-and-wire-format.md)
- [HTTP API](./http-api.md)

## Notes

- `mips/` remains the proposal, decision, and process bucket. It should link to
  reference specs instead of duplicating long schema definitions.
- `policy/` holds compatibility, release, and threat-model policy.
- `evaluation.md` holds evaluator-facing benchmark methods and artifact rules.
