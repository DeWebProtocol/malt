# Concepts

This folder gives reader-facing background for MALT. These documents explain
why the project exists and how to compare it with hash, Merkle tree, and
Merkle-DAG authentication models.

Use these documents for orientation. Implementation-bound behavior, wire
formats, proof fields, HTTP headers, and compatibility rules remain in
[`docs/spec`](../spec/README.md) and [`docs/policy`](../policy/README.md).

## Start Here

- [Data authentication background](./data-authentication.md) explains how hash,
  Merkle tree, and Merkle-DAG authentication work, then introduces MALT's data
  authentication model.
- [Merkle DAG vs MALT](./merkle-dag-vs-malt.md) compares object-chain proofs,
  direct `root + path` reads, HTTP proof transport, and rewrite amplification.

## Mechanism References

After the conceptual overview, use the implementation-bound specs for exact
mechanics:

- [Semantic model](../spec/semantic.md) for map/list semantics, roots, payloads,
  resolver, writer, and ArcTable boundaries.
- [ProofList format](../spec/prooflist-format.md) for proof steps, ordering,
  HTTP proof headers, and range evidence.
- [HTTP API](../spec/http-api.md) for resolve, content, verify, and mutation
  routes.
- [Commitment model](../spec/commitment.md) for backend proof assumptions.
- [CID and wire format](../spec/cid-and-wire-format.md) for root encoding.
