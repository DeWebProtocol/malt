# TypeScript Writer with Commitment-Only WASM

This branch is the independent comparison implementation of the browser
client-root writer. TypeScript owns:

- strict update-view normalization and semantic-closure checks;
- before-image validation and deterministic dependency ordering;
- retained logical vectors and receipt-gated session advancement;
- canonical view, intent, bundle, and next-view construction and SHA-256
  digests.

Go/WASM owns only backend-specific semantic commitment materialization and
incremental commitment updates. It does not decode an update view or semantic
intent and cannot construct or accept a client-root bundle.

Build the separate artifacts with:

```bash
scripts/build-writer-wasm.sh dist/writer
```

This emits:

```text
dist/writer/malt-commitment-kzg.wasm
dist/writer/malt-commitment-ipa.wasm
dist/writer/wasm_exec.js
```

Each WASM artifact links exactly one backend. The IPA build also excludes
`graph/runtime`'s default KZG fallback.

The backend registers four transport-local, versioned bridge functions:

```text
globalThis.maltCommitmentLoadObjectsV1(
  commitmentObjectsJSONUTF8
) -> Promise<loadResultJSON>

globalThis.maltCommitmentApplyDeltaV1(
  commitmentDeltaJSONUTF8
) -> Promise<commitmentResultJSON>

globalThis.maltCommitmentRetainRootsV1(
  retainedRootsJSONUTF8
) -> Promise<retainedRootsResultJSON>

globalThis.maltCommitmentDropSessionV1(
  sessionIDUTF8
) -> Promise<sessionID>
```

All accept strict `Uint8Array` values. Each TypeScript session owns an isolated
WASM engine identified by a random handle; at most 64 engines are retained.
`loadObjects` atomically creates or replaces that engine, recomputes every
declared object root, and seeds branch-preserving materialization. `applyDelta`
receives one already-normalized, dependency-resolved object delta and returns
only its new typed root plus commitment timing. `retainRoots` removes snapshots
that are no longer reachable from the accepted or prepared views, and
`dropSession` releases the engine.

The TypeScript adapter is
[`ts/writer-session.ts`](./ts/writer-session.ts). Its public session methods
mirror the stateful Go/WASM experiment:

```text
load(updateView)
prepare(operationID, semanticIntent)
prepareCompact(operationID, semanticIntent)
prepareCompactJSON(operationIDUTF8, semanticIntentJSONUTF8)
acceptReceipt(operationID, materializationReceipt)
discard(operationID)
close()
diagnostics()
```

`prepare` returns the canonical `malt.writer-compute-result/v1` JSON string.
Both compact methods perform and retain that same full computation but return a
`malt.writer-prepare-summary/v1` JSON string containing the candidate, every
transition root, and every payload CID. `prepareCompactJSON` accepts the same
two strict, bounded `Uint8Array` inputs as the stateful Go/WASM compact API so
the comparison runner can use identical raw input and output bytes.
Only an exact durable receipt advances the retained view; accepting one
candidate invalidates all other speculative candidates. A session retains at
most 64 candidates and 64 MiB of encoded prepared responses.

Fixed-list `uint64` fields are retained internally as `bigint`. Callers may
supply those fields as an exact safe `number`, a `bigint`, or a canonical
decimal string; results and WASM bridge requests emit the canonical unquoted
JSON integer required by the existing Go v1 wire profile.

Run canonical Go parity plus real KZG/IPA WASM smoke tests with:

```bash
scripts/test-writer-wasm.sh
```

Executable performance runners and result schemas remain owned by
`malt-evaluation`; this Core branch keeps only the canonical Go parity smoke
fixtures needed to validate the adapter.
