# Browser Client-Root Writer

Build the browser writer artifacts from the repository root:

```bash
scripts/build-writer-wasm.sh dist/writer
```

The build emits one `malt-writer.wasm`, one `wasm_exec.js`, and the
`malt-writer-workers.mjs` controller plus its `malt-writer-worker.mjs` Worker.
The unified WASM file contains both commitment backends. The controller
downloads and compiles it once, then transfers the compiled
`WebAssembly.Module` to two independent Workers. One instance initializes only
KZG and the other initializes only IPA.

```js
import { createMaltWriterWorkers } from "./malt-writer-workers.mjs";

const writers = await createMaltWriterWorkers();

// KZG normally becomes usable while IPA is still initializing.
await writers.kzgReady;
console.log(writers.status("ipa")); // { backend: "ipa", state: "initializing" }

const bootstrapViewJSON = await writers.bootstrap("kzg"); // new empty Bucket only
const baseRoot = await writers.load("kzg", updateViewJSONUTF8);
const candidateRoot = await writers.prepare(
  "kzg",
  operationIDUTF8,
  semanticIntentJSONUTF8,
);
const resultJSON = await writers.getPreparedResult("kzg", operationIDUTF8);
await writers.closeSession("kzg");

// Await IPA only when an IPA view needs it.
await writers.ipaReady;
```

`kzgReady`, `ipaReady`, `whenReady(backend)`, and `status(backend)` are
independent. IPA initialization cannot block the KZG Worker event loop, RPC
queue, session, or WASM memory. Closing a session releases its accepted view,
prepared candidates, and materialized snapshots while keeping that Worker ready
for another `load`. Call `terminateBackend(backend)` to release one Worker, or
`terminateAll()` when neither instance is needed. `terminate()` remains an alias
for `terminateAll()`.

The controller exposes the following backend-routed methods:

```text
compute(
  backend,
  operationIDUTF8,
  updateViewJSONUTF8,
  semanticIntentJSONUTF8
) -> Promise<resultJSON>

bootstrap(backend) -> Promise<bootstrapUpdateViewJSON>

load(backend, updateViewJSONUTF8) -> Promise<baseRoot>

prepare(
  backend,
  operationIDUTF8,
  semanticIntentJSONUTF8
) -> Promise<candidateRoot>

getPreparedResult(
  backend,
  operationIDUTF8
) -> Promise<resultJSON>

acceptReceipt(
  backend,
  operationIDUTF8,
  materializationReceiptJSONUTF8
) -> Promise<newBaseRoot>

discard(backend, operationIDUTF8) -> Promise<operationID>

closeSession(backend) -> Promise<void>

terminateBackend(backend) -> void
terminateAll() -> void
```

All byte arguments are strict `Uint8Array` values. The operation ID contains up
to 128 ASCII bytes; JSON arguments use the checked-in client-root wire profiles
and are bounded by the protocol's 64 MiB document limit.

Each Worker supplies exactly one immutable startup argument to its Go instance:
`--backend=kzg` or `--backend=ipa`. The runtime rejects missing, invalid, or
multiple backend arguments. There is no mutable JavaScript backend selector,
and a single instance never initializes both schemes.

## Session behavior

`bootstrap` creates, commits, and retains the canonical empty-map base in the
client Worker; its first prepared result carries a root-bound base witness so an
untrusted service can materialize that root without calling `Commit`. Loading
recomputes and verifies the complete update view once. Preparing a
mutation uses the retained logical vectors and semantic materialization, but
does not advance the session. `prepare` returns only the candidate root. The
caller requests the full result for a retained operation through
`getPreparedResult` only when it needs the bundle, next view, or metrics. Only
an exact
`malt.materialization-receipt/v1` for the prepared bundle advances the retained
base.

Accepting one candidate invalidates every other speculative candidate; at most
64 candidates and 64 MiB of encoded prepared responses may be retained at once.
Only one prepare is admitted in each backend instance across the JS/WASM
boundary at a time. Discard and accept prune every materialized snapshot that
is no longer reachable from the accepted view or a remaining prepared
candidate. KZG and IPA sessions are unrelated because they live in different
Workers.

The stateless compute path and `getPreparedResult` return the protocol-owned
`malt.writer-compute-result/v2`:

```json
{
  "profile": "malt.writer-compute-result/v2",
  "bundle": {},
  "materialization": {},
  "next_view": {},
  "metrics": {}
}
```

`materialization` is the root-bound radix proof-serving witness for every map
transition output. A service must validate it against the canonical bundle and
derived logical post-view through `radix.ValidateMaterialization` before import;
it must not treat the witness as trusted storage or a state-transition proof.

The writer computes locally and does not contact a Gateway, publish a root, or
promote a candidate to trusted state.

## Direct runtime API

The Worker wraps these globals registered inside one backend-specific WASM
instance:

```text
globalThis.maltComputeClientRootV1(...)
globalThis.maltWriterBootstrapSessionV1()
globalThis.maltWriterLoadSessionV1(...)
globalThis.maltWriterPrepareSessionV1(...)
globalThis.maltWriterGetPreparedResultV1(...)
globalThis.maltWriterAcceptSessionReceiptV1(...)
globalThis.maltWriterDiscardSessionCandidateV1(...)
globalThis.maltWriterCloseSessionV1()
```

Code that starts the Go runtime directly must set
`go.argv = ["malt-writer.wasm", "--backend=kzg"]` (or `ipa`) before
`go.run(instance)`. Browser applications should normally use the controller.

Run the native tests, compile-time dependency-isolation checks, direct real-WASM
smokes for both backends, and the dual-Worker independence smoke with:

```bash
scripts/test-writer-wasm.sh
```
