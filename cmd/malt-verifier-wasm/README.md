# Browser Verifier

Build the deterministic browser verifier bundle from the repository root:

```bash
scripts/build-verifier-wasm.sh dist/verifier
```

After loading the matching Go `wasm_exec.js` and starting the Go runtime, the
module registers:

```text
globalThis.maltVerifyResolve(verificationJSON) -> resultJSON
globalThis.maltVerifyRead(verificationJSON) -> resultJSON
globalThis.maltVerifyArtifact(requestJSON) -> resultJSON  # v0.0.4 compatibility
```

The default module initializes both built-in commitment backends. Browser
clients that isolate initialization may set one of the following values before
calling `go.run`:

```js
globalThis.maltVerifierBackend = 'kzg' // KZG-only verifier
globalThis.maltVerifierBackend = 'ipa' // IPA-only verifier
globalThis.maltVerifierBackend = 'all' // default portable verifier
```

Backend selection still comes from each typed MALT root during verification.
A backend-specific instance fails closed when evidence requires a backend that
is not registered. Clients that need cross-backend ProofLists must use the
default `all` instance.

`maltVerifyResolve` accepts one `malt.resolve/v0alpha1` request/result pair;
`maltVerifyRead` accepts one `malt.read/v0alpha1` request/result pair. The
caller constructs the request from its trusted root and intended query before
passing the untrusted result to WASM. Schemas live in `protocol/schemas/`.

For example:

```json
{
  "request": {
    "profile": "malt.resolve/v0alpha1",
    "root": "<client-selected CID>",
    "segments": ["docs", "readme.md", "@payload"]
  },
  "result": {
    "profile": "malt.resolve/v0alpha1",
    "target": "<untrusted returned CID>",
    "prooflist": {}
  }
}
```

The abbreviated ProofList above is not valid evidence. Verification fails
closed unless the request/result profile, root, exact query, target, ordering,
and all cryptographic bindings validate locally. `error` is diagnostic and
`valid` is the acceptance boolean.

`maltVerifyArtifact` remains available solely for the frozen
`malt.artifact/v0alpha2` v0.0.4 compatibility envelope.
