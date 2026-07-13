# Browser Verifier

Build the deterministic browser verifier bundle from the repository root:

```bash
scripts/build-verifier-wasm.sh dist/verifier
```

After loading the matching Go `wasm_exec.js` and starting the Go runtime, the
module registers:

```text
globalThis.maltVerifyArtifact(requestJSON) -> resultJSON
```

The request shape is the checked-in
`artifact/schemas/local-verify-request.schema.json` contract:

```json
{
  "profile": "malt.artifact/v0alpha2",
  "trusted_root": "<client-selected CID>",
  "expected": {
    "operation": "resolve",
    "query": {"kind": "path", "segments": ["docs", "readme.md"]}
  },
  "artifact": {"profile": "malt.artifact/v0alpha2", "operation": "resolve"}
}
```

The real artifact must be complete. Verification fails closed unless the
trusted root and caller-selected expectation match the artifact and all
envelope, query, target, ordering, and cryptographic proof bindings validate locally. The output follows
`local-verify-result.schema.json`; `error` is diagnostic and `valid` is the only
acceptance boolean.
