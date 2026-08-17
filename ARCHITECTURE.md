# MALT Local Runtime Architecture

## Boundary

This repository is the user-controlled local data runtime, not the
graph-authentication SDK and not a managed Gateway service. A daemon is its
primary long-running process shape, while the CLI, foreground mode, future GUI,
and future local API are adapters over the same runtime services.

It owns:

- the `malt` CLI and local daemon lifecycle;
- trusted/candidate root state and explicit acceptance;
- managed-Bucket base/remote/stash state and stash-before-fetch synchronization;
- semantic remote capabilities with a current Gateway HTTP implementation;
- UnixFS path, manifest, fixed-list payload, import, and range-body semantics;
- IPFS-compatible Merkle DAG UnixFS import as an alternative runtime target;
- local replay verification for gateway Merkle DAG compatibility reads;
- application-level payload-byte verification.
- runtime-side encrypted snapshot creation, restore, and automatic scheduling.

Runtime-specific benchmark process adapters also live in this repository under
`tools/evaluation`. They exercise this implementation at a pinned commit, but
do not own benchmark plans, suites, comparison policy, result schemas, or
result provenance; those belong in `malt-evaluation`.

It depends on `github.com/dewebprotocol/malt-core` for canonical graph types,
resolve/read/mutation protocols, ProofList verification, CID rules, and
commitment implementations. It must not copy or redefine those contracts.

The current implementation uses a Gateway for remote ArcTable materialization,
CAS persistence, proof generation, and mutation execution. Gateway is an
optional untrusted executor in the target architecture, not a trust authority
or a permanent prerequisite: local-CAS, peer, and hybrid transports must be
able to implement the same semantic capability boundary without changing the
application, trust, sync, or filesystem layers.

## Data flow

```text
UnixFS path / local files
          |
          v
  MALT local runtime application adapter
          |
          | canonical segments, generic resolve/read/mutation/CAS requests
          v
  semantic transport capability
          |
          v
 current Gateway / future Peer or Local CAS
          |
          | result + ProofList + payload bytes
          v
  local MALT core verification
          |
          v
accepted application result or candidate root
```

Application separators are parsed here. MALT core receives typed segment
arrays and resolves canonical arcs; HTTP uses JSON arrays rather than assigning
core semantics to `/`, `.`, or `[]`.

The current native UnixFS materializer is `hybrid`: each directory becomes an
authenticated map root, and ancestor maps also retain descendant root-relative
path bindings. Pure flat and pure hierarchical materializers are possible
future runtime strategies, not aliases for the current implementation.

For `malt add --target merkle-dag`, the runtime uses Boxo to construct explicit
dag-pb UnixFS blocks and writes those immutable blocks through the same untrusted
gateway CAS. That path returns a Merkle DAG CID and does not invoke MALT
resolve/read/proof semantics. Supporting both targets is a runtime feature, not
an indication that Merkle DAG semantics belong in core.

The gateway may execute Merkle DAG traversal as a compatibility service. In
that flow it returns every touched CID-bound block. The local runtime hashes
each block and independently replays UnixFS traversal from the caller-selected
Merkle DAG root. This is Merkle DAG authentication, not MALT authentication,
and uses `merkledag.resolve/v0alpha1` and `merkledag.read/v0alpha1`, never a
ProofList. Resolve replay also follows DAG-CBOR/DAG-JSON map/list coordinates that
terminate at CID links; successful read replay still requires a UnixFS file.
The compatibility wire contract carries coordinates as a typed `segments`
array. Each segment is opaque UTF-8 data rather than a URL or filesystem path
component, so values such as `.`, `..`, `a/b`, the empty string, and U+0000 are
looked up exactly. Only the CLI's optional UnixFS string path applies `/`
splitting and portable path restrictions.

## Application, transport, and trust policy

The dependency direction is deliberate:

```text
cmd/malt and internal/daemon -> application use cases
application -> unixfs / merkledag / trust narrow ports
application/backup -> add / bucketsync / unixfs narrow ports
unixfs     -> MALT core verifier + narrow transport ports
merkledag  -> CID/link replay + fixed profile transport
trust      -> accepted/candidate root persistence
transport  -> HTTP only; never imports unixfs, merkledag, or trust
bucketsync -> transport + independent durable synchronization metadata
tools/evaluation/cmd -> internal/evaluation + public runtime capabilities
```

`transport.Client` is one reusable HTTP connection, but consumers depend on
the narrow `Native`, `Mutations`, `CAS`, or `Diagnostics` interfaces rather
than a single mega-interface. Merkle DAG compatibility is exposed only through
the fixed `PostMerkleDAGResolve` and `PostMerkleDAGRead` capabilities; there is
no arbitrary profile-route escape hatch. Transport results remain untrusted.
The public transport also does not expose evaluation instance credentials,
bootstrap control, unchecked raw-CAS reads, or the selective-CAR route. Those
disposable-Gateway capabilities live under `internal/evaluation` and can only
be composed by evaluator adapters.
The `application` layer supplies the caller-selected root, composes verified
UnixFS or Merkle DAG reads, records mutation results as candidates, and exposes
explicit candidate acceptance for both CLI and daemon adapters. The `trust`
package alone persists accepted roots and performs promotion.

`bucketsync` is separate from `trust`. The caller captures the exact Bucket
commit/root/revision before materialization and stages the candidate against
that base before any remote fetch. Push refuses candidates without this durable
binding. Fetch updates observed remote metadata without changing that stash. A
subsequent push can therefore be fast-forwarded, merged, or preserved by the
Gateway without silently replacing local work. Neither pull nor push promotes
a Gateway head into trusted-root policy.

An explicit CID is parsed before any accepted-root alias lookup. Consequently,
explicit-CID resolve/read/write operations do not require the alias store to
exist or be readable; only alias selection opens that local state. Generic
transport exposes canonical root-structure creation, while the UnixFS gateway
adapter owns the `@payload` empty-root binding needed by fixed-list
materialization. The same adapter binds mutation receipts to the gateway's
returned base root and rejects a response for any other requested root.

## Verified UnixFS facade

`unixfs` owns the transport-neutral native reader/writer facade. Its remote
port contains only generic MALT resolve/read operations; CAS and root creation
are separate narrow capabilities. The facade:

1. parses `/` as UnixFS application syntax;
2. constructs requests from a caller-selected trusted root;
3. verifies every resolve/read result locally;
4. requires primitive list reads to start at the verified resolve target;
5. checks raw blocks and directory manifests against authenticated CIDs;
6. checks list-range segments and the assembled body; and
7. returns removal output only as an unaccepted candidate root.

`Stat` uses a bounded one-byte measured-list query to authenticate size and
chunk metadata without returning an O(file-size) segment list. Actual range
reads carry their own exact range proof.

## Daemon

The daemon is a local control plane for trusted-root state. It listens only on
a user-owned Unix socket with mode `0600`, or an owner/system-only Windows
named pipe derived from the state path. It does not expose a public proof
verification endpoint and does not make a gateway-generated root trusted. A
managed background daemon is bound to its state file by a random lifecycle
instance token; `stop` and `restart` signal a PID only after the private
identity endpoint authenticates that same instance. Daemon API calls and
foreground CLI commands share a cross-process trust-store lock and reload the
latest state before every read or mutation, so neither can overwrite a newer
explicit trust decision with a stale in-memory snapshot. Candidate creation
and acceptance also carry the accepted base root: if that root has advanced,
the operation fails as stale instead of applying a sibling transition.

The same private HTTP-over-socket control plane accepts manual encrypted backup
requests and runs configured interval jobs. Manual and scheduled executions
share one serialized runner. The scheduler reloads configuration and persists
its last check, attempt, success, error, and candidate result in a `0600` local
history file. A cross-process operation lock also excludes the advanced
foreground bypass from a concurrent daemon backup. CLI restore remains a
foreground operation because the caller must explicitly select a trusted root
and a destination filesystem path.

Before staging, backup persists a local publication journal containing the
candidate, original base, message, source, and scheduled-job identity. A
timeout or lost Gateway response therefore restages idempotently and retries
the frozen Bucket push rather than producing another encrypted snapshot.

No browser-facing loopback TCP service is opened. A future Web or native GUI
may use the private management API, but it must not own backup, trust, or key
policy.

## Encrypted backup boundary

The backup application stores one opaque snapshot at
`malt-backup/snapshot`. It tar/gzip encodes the source tree before applying
XChaCha20 with a fresh 192-bit nonce. The format contains a small cleartext
version/epoch/nonce header and no AEAD tag.

This construction is valid only under the enforced restore sequence:

```text
caller-selected accepted/explicit root
    -> locally verified path ProofList
    -> authenticated target/list and chunk CIDs
    -> CID-verified encrypted archive bytes
    -> XChaCha20 decryption
    -> safe archive extraction
```

Consequently the stream cipher does not claim to authenticate remote bytes:
MALT proofs and CIDs do that. Archive decoding detects an unavailable/wrong
epoch as an operational error. An untrusted Gateway head, unchecked raw-CAS
read, or decryption before CID verification must never enter this path.

The local keyring stores one master key per epoch with mode `0600`. A
domain-separated HMAC-SHA-256 derivation produces a per-Bucket key in memory.
Plaintext tree fingerprints used for automatic change detection remain in
local history and are never uploaded. Original source names are inside the
encrypted archive, although ciphertext size, update timing, the fixed remote
path, and access patterns remain visible. This is a backup application profile;
it does not silently change the existing plaintext semantics of `malt add`.

## Packages

- `cmd/malt`: CLI and daemon process lifecycle.
- `tools/evaluation/cmd`: evaluator-launched process adapters. Their executable
  names and wire contracts are campaign inputs, not supported user CLI.
- `application`: reusable trusted-root, UnixFS, and Merkle DAG use cases shared
  by command and daemon adapters.
- `application/backup`: encrypted archive, local fingerprint, Bucket
  publication, verified restore, history, and automatic scheduling use cases.
- `application/add`: reusable ignore-aware local-input staging, symlink policy,
  hybrid MALT materialization, Merkle DAG import, and candidate recording.
- `transport`: untrusted native MALT/CAS HTTP transport and narrow capability
  interfaces.
- `bucketsync`: durable Bucket base/remote/stash state and push orchestration.
- `trust`: observed, candidate, and accepted root policy plus durable local
  persistence.
- `cache`: non-authoritative payload bodies and metadata bound to exact
  dataset/branch/root/revision/CID/encryption-epoch identity; verified hits
  recheck the CID and locally revalidate cached proof evidence.
- `journal`: ordered local filesystem-operation intent, immutable retry
  identity, and offline/pending/conflict/completed replay state; it has no root
  acceptance capability.
- `filesystem/service`: platform-neutral read-only filesystem semantics over
  an immutable caller-selected dataset/root/revision/encryption view. It
  uses the payload-lazy `unixfs.LookupReader` to revalidate projection on each
  read, uses the cache only for raw CID-bound payloads, and keeps List payloads
  on the authenticated range path.
- `filesystem/staging`: a platform-neutral local dirty overlay over the
  verified read-only service. It records write, mkdir, rename, and unlink
  intent against an exact immutable View, pins local file handles to a payload
  CID, survives restart through the cache and journal, and reports only local
  journal durability from `Fsync`. It performs no upload, candidate-root
  computation, or trust mutation.
- `filesystem/mount`: durable desired/pending-unmount records, restart
  reconciliation, a process-held exclusive manager lease, immutable local-View
  selection, and the narrow platform adapter/session contract. It does not
  implement trust or network access. Targets without a kernel-backed,
  process-released lease fail closed before opening the mount registry.
- `filesystem/platform/fuse`: Linux-only read-only FUSE syscall translation.
  It receives only the View-bound mount capability, rejects every namespace or
  data/metadata mutation, and verifies `/proc/self/mountinfo` ownership before
  recovering even a disconnected stale mount. It has no trust or transport
  access.
- `localapi`: reusable client for the private daemon control plane, shared by
  the CLI and future thin GUI adapters without direct trust-store mutation.
- `internal/runtime`: process-independent composition of local accepted-root
  selection, per-dataset Gateway verified readers, non-authoritative cache,
  durable mount lifecycle, and the outer platform adapter. Non-Linux targets
  leave mount control unconfigured until a native adapter exists.
- `merkledag`: isolated compatibility profile adapter and local CID/link replay.
- `merkledag/importer`: IPFS-compatible UnixFS DAG construction.
- `merkledag/ipld`: generic CID-validating IPLD parsing and link traversal for
  Merkle-DAG compatibility applications.
- `internal/daemon`: local Unix-socket/Windows-pipe runtime control API,
  including optional mount lifecycle routes backed by the shared manager.
- `internal/keyring`: runtime-owned backup epoch keys and per-Bucket derivation.
- `internal/durablefile`: platform-specific parent-directory synchronization
  after security-sensitive atomic state replacement.
- `internal/filelock`: bounded cross-process locks for runtime-owned state
  transitions.
- `internal/securefile`: owner-only Unix file modes and protected
  owner-and-SYSTEM Windows file DACLs.
- `internal/cas`: runtime-side CAS helpers and byte verification.
- `internal/evaluation`: private adapter support, including evaluation Gateway
  authentication/control transport and shared workload machinery.
- `unixfs/model`: UnixFS application values and path rules.
- `unixfs`: verified UnixFS reader/writer facade, staging, materialization,
  and payload verification.

The `internal` packages are not compatibility promises. The public
`application`, `bucketsync`, `cache`, `filesystem/service`,
`filesystem/staging`, `filesystem/mount`, `journal`, `localapi`, `transport`,
`trust`, `unixfs`, and
`merkledag` packages are the intended pre-release integration surface; their
profiles remain experimental until a release policy is published.
Architecture tests fail if production packages import evaluation support, if
`cmd/` gains a non-product binary, if public transport regains an evaluation
control-plane escape hatch, or if Merkle DAG compatibility begins to depend on
transport implementation types or MALT ProofList contracts.
