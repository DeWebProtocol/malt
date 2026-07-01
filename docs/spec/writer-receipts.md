# Writer Receipts

Writer receipts report the operational outcome of applying semantic mutations.
They help clients, APIs, and evaluations account for work, but they are not
correctness proofs.

## Status

Experimental and implementation-bound. Field names may still change before a
stable release.

## Library Receipt

`graph/writer.WriteReceipt` records:

| Field | Meaning |
| --- | --- |
| `BaseRoot` | Caller-supplied root for the mutation. |
| `NewRoot` | Root produced after applying semantic deltas. |
| `DeltaCount` | Number of semantic deltas applied. |
| `ArcCount` | Number of canonical arc changes applied. |

The writer does not publish authoritative heads, choose freshness, or merge
concurrent roots. Applications decide whether to publish or select a produced result root.

## Writer Ports

`graph.MutationWriter` is the stable core mutation boundary. It exposes
`Apply(ctx, namespace, writer.SemanticMutation)` and returns a
`writer.WriteReceipt` with the caller-supplied base root and produced result
root.

`graph.CompatWriter` groups reference-runtime helper methods such as
`CreateStructure`, `UpdateArc`, `BatchUpdateArcs`, `GetArc`, and
`GetSnapshot`. These helpers remain available to the local reference runtime,
CLI, and tests, but they are not the gateway product API. New integrations
should prefer semantic mutations through `MutationWriter` unless they
intentionally need reference compatibility helpers.

## HTTP Projection

`api/http.SemanticMutationResponse` exposes:

| JSON field | Meaning |
| --- | --- |
| `base_root` | Request base root. |
| `new_root` | Root produced by writer application. |
| `result_root` | Optional application-level result root. |
| `delta_count` | Semantic delta count. |
| `arc_count` | Canonical arc change count. |
| `malt_object_count` | Optional layout-produced object count. |
| `map_count` | Optional layout-produced map object count. |
| `list_count` | Optional layout-produced list object count. |

Layout-level counts are diagnostics and evaluation aids. They should not be
treated as verifier evidence unless separately tied to a ProofList or
commitment proof.

## Accounting Boundary

Receipts can support:

- client progress reporting
- API diagnostics
- benchmark write accounting
- storage or indexing estimates when paired with explicit metrics

Receipts do not prove:

- payload availability
- freshness of the selected root
- correctness of an unverified read
- publication or merge policy

## Related Proposals

- [MIP-1002](../mips/mip-1002-writer-receipt-accounting.md) tracks whether the
  current receipt fields should become a stable API and evaluation contract.
- [Benchmark reporting](../evaluation/README.md#benchmark-reporting) describes
  how receipts are interpreted in evaluator output.
