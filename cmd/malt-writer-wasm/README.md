# Browser Client-Root Writer

Build the browser writer artifacts from the repository root:

```bash
scripts/build-writer-wasm.sh dist/writer
```

The build emits separate `malt-writer-kzg.wasm` and
`malt-writer-ipa.wasm` artifacts plus one `wasm_exec.js`. Each artifact links
exactly one commitment backend; the backend cannot be selected through a
mutable JavaScript global.

After the Go runtime starts, the module registers the backward-compatible
stateless entry point:

```text
globalThis.maltComputeClientRootV1(
  operationIDUTF8,
  updateViewJSONUTF8,
  semanticIntentJSONUTF8
) -> Promise<resultJSON>
```

Long-lived clients should load one verified session and retain its semantic
materialization between writes:

```text
globalThis.maltWriterLoadSessionV1(
  updateViewJSONUTF8
) -> Promise<baseRoot>

globalThis.maltWriterPrepareSessionV1(
  operationIDUTF8,
  semanticIntentJSONUTF8
) -> Promise<resultJSON>

globalThis.maltWriterAcceptSessionReceiptV1(
  operationIDUTF8,
  materializationReceiptJSONUTF8
) -> Promise<newBaseRoot>

globalThis.maltWriterDiscardSessionCandidateV1(
  operationIDUTF8
) -> Promise<operationID>
```

All arguments are strict `Uint8Array` values. The operation ID contains up to
128 ASCII bytes; JSON arguments use the checked-in client-root wire profiles
and are bounded by the protocol's 64 MiB document limit.

Loading recomputes and verifies the complete update view once. Preparing a
mutation uses the retained logical vectors and semantic materialization, but
does not advance the session. Only an exact
`malt.materialization-receipt/v1` for the prepared bundle advances the
retained base. Accepting one candidate invalidates every other speculative
candidate; at most 64 candidates and 64 MiB of encoded prepared responses may
be retained at once. Only one prepare is admitted across the JS/WASM boundary
at a time. Discard and accept prune every materialized snapshot that is no
longer reachable from the accepted view or a remaining prepared candidate.

Both compute paths return `malt.writer-compute-result/v1`:

```json
{
  "profile": "malt.writer-compute-result/v1",
  "bundle": {},
  "next_view": {},
  "metrics": {}
}
```

The writer computes locally and does not contact a Gateway, publish a root, or
promote a candidate to trusted state.

Run the native tests, dependency-isolation checks, and both real WASM smoke
suites with:

```bash
scripts/test-writer-wasm.sh
```
