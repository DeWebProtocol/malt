# Browser Client-Root Writer

Build the browser writer bundle from the repository root:

```bash
scripts/build-writer-wasm.sh dist/writer
```

Before starting the Go runtime, a browser may select `kzg`, `ipa`, or `all`
through `globalThis.maltWriterBackend`. The default is `all`. After the runtime
starts, the module registers:

```text
globalThis.maltComputeClientRootV1(
  operationIDUTF8,
  updateViewJSONUTF8,
  semanticIntentJSONUTF8
) -> Promise<resultJSON>
```

All three arguments are `Uint8Array` values. The operation ID contains up to
128 ASCII bytes; the update view and semantic intent use the checked-in
`malt.update-view/v1` and `malt.semantic-intent/v1` schemas. The writer strictly
decodes them from `Uint8Array` values containing UTF-8 JSON. Inputs are rejected
before copying if the operation ID exceeds 128 bytes or either JSON byte array
exceeds the protocol's 64 MiB document limit. The writer independently
recomputes every old root, computes all changed commitments locally, and
returns:

```json
{
  "profile": "malt.writer-compute-result/v1",
  "bundle": {},
  "next_view": {},
  "metrics": {}
}
```

`bundle` is a canonical `malt.client-root-bundle/v1` value. `next_view` is the
complete candidate view that a long-lived client may retain only after the
service returns an exact durable receipt. The operation does not publish or
trust the candidate and does not contact a Gateway.
