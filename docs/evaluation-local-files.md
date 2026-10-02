# Verified local-file evaluation leaf

`tools/evaluation/cmd/malt-eval-local-files` exercises the actual rooted-v1
UnixFS writer, payload CAS and verified client reader. It accepts one bounded
file fixture and reuses it for full reads, byte ranges and authenticated missing
paths. The process adapter is evaluator tooling, not a supported product command.
The request/result contract is maintained by `malt-evaluation` in
`docs/local-files-worker-v1.md`.

The fixture contains canonical regular-file paths and actual source bytes.
Every chunk and directory manifest is stored in a real in-memory CAS, while
authentication candidates are materialized through the public Core SDK.
Initialization reads every source file through the verified UnixFS facade and
compares its bytes with the independent fixture before measured queries begin.

Reads start from the caller-selected entry root and original path. The client
fetches, decodes and verifies serialized authentication results, then binds each
required CAS body to authenticated CIDs. Ranges include actual Positional
metadata and chunk verification, including partial terminal chunks. A missing
path succeeds only after the reader's locally verified absence result; corrupt
CAS bytes or missing backend objects cannot be classified as authenticated
absence. The process never promotes a root into the runtime trust store.

Counters describe actual calls to the authentication and payload capabilities.
Proof bytes count serialized `AuthenticationResult` envelopes; CAS bytes count
all returned block bodies, including manifests and repeated accesses. They are
not network wire bytes, persistent writes or internal materializer lookups.
The response separately identifies returned range/file length and its digest.
Fixture construction and complete readback remain outside read latency.

The fixture is bounded by 32 MiB of source payload and 100,000 regular files;
chunks are between one byte and 4 MiB. The JSONL line bound is 64 MiB. Canonical
paths, collisions, operation-specific fields, duplicate keys/IDs, response
identity and explicit terminal close are checked. stdout is JSONL only.
The fixture implements no modes, symlinks, mutations, durability, HTTP, CAR,
cold-reset, checkpoint or resource-budget capability.

Runtime tests cover KZG/IPA full reads, empty files, real side objects, shallow
and deep missing paths, ranges spanning chunks, partial terminal chunks, changed
entry roots, corrupted bodies and invalid protocol/lifecycle input. Linux
full-module test/vet/build and whitespace validation passed for this leaf.
