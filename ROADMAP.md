# MALT Core Roadmap

## v0.0.6 boundary

v0.0.6 turns this repository into an SDK-only core:

- remove CLI, daemon, HTTP server, CAS/KV, ArcTable implementations, UnixFS,
  and evaluation applications;
- retain canonical resolve/read/mutation/ProofList semantics and schemas;
- retain reference map/list/resolver/writer algorithms over an injected ArcSet
  materializer capability;
- keep portable Go/WASM verification independent of execution state;
- move ArcTable/KV/CAS execution to `gateway`;
- move trusted roots, CLI/daemon, UnixFS, and payload-byte binding to
  `malt-client` and Web.

## Next core work

1. Expand resolve/read conformance vectors across KZG and IPA: root identity,
   multi-hop resolution, segment grouping, map reads, list reads, measured
   ranges, malformed evidence, cross-root splicing, and payload bindings.
2. Publish the same vectors as language-neutral JSON so Go, WASM, and future
   TypeScript/Rust clients can prove behavioral parity.
3. Define verifiable mutation transition semantics before introducing a new
   mutation artifact/profile. Current receipts remain operational only.
4. Harden variable-size measured-list evidence and native multi-open proofs.
5. Stabilize the minimal materializer capability without standardizing any
   ArcTable persistence format.

## Product integration work outside core

- `gateway`: identity/authorization, persistent ArcTable/KV/CAS, root
  publication, cache/quota policy, backend availability, and product E2E.
- `malt-client`: UnixFS CLI/daemon, accepted roots, candidate acceptance,
  local proof verification, and payload-byte validation.
- `web`: browser application, local WASM verification, and generic
  resolve/read/CAS composition.
- future `malt-ts`: TypeScript object syntax and client ergonomics after core
  conformance is stable.

## Not core scope

- a filesystem, object store, or Merkle-DAG replacement API;
- HTTP service routes or managed gateway policy;
- authoritative latest-head or multi-writer policy;
- ArcTable/KV/CAS implementations;
- UnixFS or language-object syntax;
- billing, quota, pinning, GC, abuse control, or deployment secrets.
