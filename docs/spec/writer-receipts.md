# Writer Receipts

Writer receipts report the operational outcome of applying semantic mutations.
They help clients, APIs, and evaluations account for work, but they are not
correctness proofs.

## Status

Experimental and implementation-bound. Field names may still change before a
stable release.

## Library Receipt

`mutation.WriteReceipt` records:

| Field | Meaning |
| --- | --- |
| `BaseRoot` | Caller-supplied root for the mutation. |
| `NewRoot` | Root produced after applying semantic deltas. |
| `DeltaCount` | Number of semantic deltas applied. |
| `ArcCount` | Number of canonical arc changes applied. |

The writer does not publish authoritative heads, choose freshness, prove a
state transition, or merge concurrent roots. Applications decide whether to
accept, publish, or select a produced candidate root.

## Mutation And Execution Ports

Package `mutation` is the stable portable contract. It defines
`SemanticMutation`, `ArcSetDelta`, commit descriptors, validation, and
`WriteReceipt` without namespace or storage placement.

`execution.MutationApplier` is the untrusted execution port. It exposes
`Apply(ctx, namespace, mutation.SemanticMutation)` and returns a
`mutation.WriteReceipt` with the caller-supplied base root and produced result
root. `graph.MutationWriter` is the reference graph adapter over that contract.

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
| `malt_object_count` | Optional application-produced object count. |
| `map_count` | Optional application-produced map object count. |
| `list_count` | Optional application-produced list object count. |

Application-level counts are diagnostics and evaluation aids. They should not be
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
- that a delta correctly transformed the accepted base root into the returned
  candidate root

MALT does not currently define a delta/state-transition proof. Therefore write
receipts stay separate from the proof-bearing resolve/read contracts instead
of being placed in a generic artifact union.

## Related Proposals

- [MIP-1002](../mips/mip-1002-writer-receipt-accounting.md) tracks whether the
  current receipt fields should become a stable API and evaluation contract.
- [Evaluation](../evaluation.md) describes how receipts and accounting fields
  are interpreted in evaluator output.
