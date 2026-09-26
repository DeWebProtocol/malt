# Typed authentication migration

The runtime uses the sole current Core path: `derivation`, `auth/tree`,
`engine`, explicit traversal, and `sdk/authentication`. Native HTTP queries
use `malt.authentication/3`; candidates use `malt.authentication/2`. Ordered
writes use `malt.authentication-batch/1` and exact
`malt.authentication-receipt/1` checks. No old Resolve/Read, Map-proof,
UpdateView, semantic intent, client-root bundle, or WASM fallback is used.

## Reads and application layouts

Flat-v1 uses one complete-path label and may target a payload or
manifest directly. Hybrid-v1 preserves its flattened relations plus directory
Roots. Rooted-v1 uses explicit directory-name steps. If the selected
target remains a Prefix Root, a content read explicitly authenticates its
ordinary `@payload` label. All directory layouts use public SHA256 derivation
(profile 4); ReaderOptions and filesystem service options carry the application
layout. Managed mounts obtain it from the selected Bucket metadata.
Positional chunk Roots expose authenticated geometry and byte ranges, with no
system payload binding.

`unixfs.AuthenticationAdapter` computes Roots locally and checks exact remote
candidate identity. The verified reader checks each caller-selected typed
query, payload CID, and range geometry before returning bytes. Directory
manifests accept only canonical V2; name-only V1/raw-manifest fallback and
semantic-kind-based file/directory inference are removed.

The encrypted application profile `malt.encrypted-unixfs/v1` retains its own
`list` storage tag for fixed ciphertext chunks. That field maps explicitly to
Positional authentication and is not the removed Core List adapter. Authenticated
chunk width must match the encryption manifest before range slicing/decryption.

## Writes, retries, and trust

The UnixFS planner imports bounded complete candidates along affected paths,
retains verified immutable Writers, checks original labels and opened manifest
CIDs, applies ordered intent, and computes children before parents. It returns
a local `writeplan.Plan`; manifest uploads occur only during persistence.
Shared directories support copy-on-write changes. Flat/hybrid/rooted
planning, unchanged-subtree reuse, batching, and no-change completion remain.
Only final staged payloads referenced by the resulting plan are uploaded.

Write-back submits the exact ordered batch and validates its receipt before
recording a candidate. A lost response preserves the exact transaction for
retry. Accepted-root races remain fenced; no receipt, successful network call,
or observed head promotes a locally trusted Root. `application/clientroot` and
old concrete Gateway mutation adapters are removed. Bucket synchronization uses
`OpenRemote`/`OpenRemoteBranch` and the transport-neutral dataset capability;
its unused legacy DTO/Open adapter is removed.

Evaluation workers separately import retained candidate graphs and report the
new versioned measurement fields. Source workspace validation is not release
publication. The runtime module namespace/tag gate and exact downstream
release pins remain separate from this source migration.

Native builds pin published Core `v0.0.10-rc.3` at commit
`6f16b13f3abe9cbd0992e83e1457663935e5a72f` for independent module builds.
New roots use the remote's advertised backend as an
untrusted creation hint; updates preserve the existing Root descriptor.
