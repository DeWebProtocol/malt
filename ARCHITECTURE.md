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
proof generation, and mutation execution. Immutable bytes may use Gateway, a
durable local CAS, or a Gateway-primary/local-cache hybrid policy. Gateway is an
optional untrusted executor in the target architecture, not a trust authority
or a permanent prerequisite. The local and hybrid CAS implementations already
use the same semantic boundary; future peer and local MALT executors must do so
without changing application, trust, sync, or filesystem layers.

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
 Gateway HTTP / Local CAS / Hybrid / future Peer
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
application/backup -> bucketsync / unixfs/encrypted / trusted-root narrow ports
unixfs     -> MALT core verifier + narrow transport ports
merkledag  -> CID/link replay + fixed profile transport
trust      -> accepted/candidate root persistence
transport  -> Gateway/local/hybrid adapters; never imports unixfs, merkledag, or trust
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

`transport/local` is a bounded, atomic, owner-private durable CAS and verifies
the complete body on both `Get` and `Has`. `transport/hybrid` is transport-level
policy: Gateway is the persistence primary, a local CAS is a non-authoritative
read-through cache, and primary bytes are independently CID-verified before
return. Cache presence can never satisfy primary `Has`. The default runtime
configuration remains `gateway`; `local` currently supports local-only
Merkle-DAG import, and managed native MALT operations reject it until a local
Native/Mutations executor exists. `transport/capabilitytest` runs the same CAS
contract against mock, Gateway HTTP, local, hybrid, and a peer-loopback adapter;
the loopback proves the port boundary without declaring a P2P wire profile.

The local CAS pins its boundary with descriptor/handle-relative no-follow
operations. Verified reads reject unsafe ownership, permissions, links, or
reparse metadata and unreadable or non-regular target objects without modifying
the inode; only an atomic protected `Put`
can repair an exact target. Block directories are selected from the first
multihash digest byte, not the shared multihash algorithm header. An unreadable
shard is corruption but is not automatically chmodded or replaced because it
may contain unrelated blocks; repair current-user ownership and `0700` mode
offline before retrying. Runtime CAS
creation confirms the boundary and walks descriptor-relative parent handles to
sync every ancestor directory entry outward to the filesystem root; retries
repeat that chain after any failed directory sync. Runtime CAS
composition preserves explicit resource ownership: plan services and CLI
operations close after use, writable bindings close on detach, and the read-side
router closes during mount-manager shutdown. Failed releases remain observable
and higher-level cleanup remains retryable. Platform `os.File.Close` calls are
terminal even when they report an error, so invalid handles are discarded and
never reused while the diagnostic is still surfaced. The first local-CAS
`Close` attempt also terminally disables all later I/O; a retry may only finish
cleanup ownership.
Each platform mount also owns one reference-counted read-side View lease. Normal
unmount, failed mount rollback, unexpected session exit, and shutdown release it
after detach/write-binding cleanup; the router closes the service at the last
reference and retains failed release ownership for retry.
While the last release is pending, new mounts fail rather than reviving a
partially closed service; after cleanup they open a fresh route.
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

## Encrypted filesystem backup boundary

The backup application stores the source tree directly as the runtime-owned
`malt.encrypted-unixfs/v1` application profile. Dataset, directory, and file
nodes are MALT Maps. Their `@payload` bindings authenticate encrypted
manifests; regular file roots additionally authenticate `@content`, which is a
raw ciphertext block or a MALT List of fixed-size ciphertext chunks. Directory
Map keys are HMAC-derived opaque tokens. Plaintext names live only in the
encrypted parent manifest, which is the shared `readdir` description for the
daemon, filesystem adapter, local API, and authorized browser code.

The enforced read sequence is:

```text
caller-selected accepted/explicit root
    -> locally verified path ProofList
    -> authenticated opaque-token target and payload CID
    -> CID-verified encrypted manifest or file chunk
    -> XChaCha20-Poly1305 decryption with bound context
    -> verified directory view, file range, or safe materialization
```

MALT proofs authenticate the selected relation, CIDs bind the returned
ciphertext bytes, and AEAD rejects a wrong key, nonce, context, or modified
ciphertext. An untrusted Gateway head, unchecked raw-CAS read, cache-presence
shortcut, or decryption before ProofList/CID verification must never enter this
path.

The local keyring stores one master key per epoch with mode `0600`. A
domain-separated HMAC-SHA-256 derivation produces a per-Bucket key in memory.
Plaintext tree fingerprints used for automatic change detection remain in
local history and are never uploaded. Original source names are inside
encrypted manifests, although ciphertext size, update timing, opaque-token
relations, and access patterns remain visible. The epoch-1 namespace key keeps
tokens stable across later content-key rotations. This is a runtime application
profile; it does not change MALT Core or the existing plaintext semantics of
`malt add`.

## Packages

- `cmd/malt`: CLI and daemon process lifecycle.
- `tools/evaluation/cmd`: evaluator-launched process adapters. Their executable
  names and wire contracts are campaign inputs, not supported user CLI.
- `application`: reusable trusted-root, UnixFS, and Merkle DAG use cases shared
  by command and daemon adapters.
- `application/backup`: encrypted MALT-native filesystem, local fingerprint, Bucket
  publication, verified restore, history, and automatic scheduling use cases.
- `application/add`: reusable ignore-aware local-input staging, symlink policy,
  hybrid MALT materialization, Merkle DAG import, and candidate recording.
- `application/writeback`: transport-neutral replay over an exact leased
  staging batch. It verifies payload-store CIDs, obtains a bounded verified
  client-root view, delegates only canonical intent planning, verifies the
  exact durable receipt, records a candidate, and atomically completes or
  conflicts the batch. It has no accepted-root promotion method and imports no
  concrete transport.
- `transport`: untrusted Gateway HTTP adapter and narrow capability interfaces;
  `transport/local` provides durable local CAS, `transport/hybrid` owns
  Gateway-primary/read-through policy, and `transport/capabilitytest` provides
  adapter conformance.
- `bucketsync`: durable Bucket base/remote/stash state and push orchestration.
- `trust`: observed, candidate, and accepted root policy plus durable local
  persistence.
- `cache`: non-authoritative payload bodies and metadata bound to exact
  dataset/branch/root/revision/CID/encryption-epoch identity; verified hits
  recheck the CID and locally revalidate cached proof evidence. Candidate
  bodies remain local non-authoritative state and cannot enter verified reads.
  Dirty-body recovery preflights metadata and actual file size, streams CID
  verification, and supports bounded full/range reads without an unbounded
  `os.ReadFile` allocation.
- `journal`: ordered local filesystem-operation intent, immutable retry
  identity, and offline/pending/conflict/completed replay state; it has no root
  acceptance capability.
- `filesystem/service`: platform-neutral read-only filesystem semantics over
  an immutable caller-selected dataset/root/revision/encryption view. It
  uses the payload-lazy `unixfs.LookupReader` to revalidate projection on each
  read, uses the cache only for raw CID-bound payloads, and keeps List payloads
  on the authenticated range path.
- `filesystem/staging`: a platform-neutral local dirty overlay over the
  verified read-only service. It records whole-file/offset write, truncate,
  mkdir, rename, and unlink intent against an exact immutable View, pins local
  file handles to a payload CID, exclusively leases both cache and journal
  paths across processes, survives restart through those stores, and reports
  only local journal durability from `Fsync`. Its upload-batch methods freeze
  exact replay identities and atomically complete or conflict the batch, but it
  performs no network I/O, candidate-root computation, or trust mutation.
  The current whole-file overlay defaults to a 256 MiB staged-file ceiling and
  rejects a larger existing file, offset end, truncate size, or replacement
  body before remote or local body materialization. Restart reconciliation
  preserves an oversized dirty record but returns the typed limit error after
  metadata/file-size preflight and streaming verification. Chunked/sparse
  staging is a later implementation step rather than an implied unbounded allocation.
- `filesystem/mount`: durable desired/pending-unmount records, restart
  reconciliation, a process-held exclusive manager lease, immutable local-View
  selection, and narrow read-only plus opt-in writable platform contracts. A
  write-back Spec must select a UnixFS layout and preserve local conflicts, and
  receives a session-owned writable binding only when the composed application
  service implements it. The complete Spec and selected accepted View bind the
  layout, staging state, and trust identity; the registry reserves at most one
  writer per dataset/branch. An adapter that returns a partial session or a
  binding whose `Close` fails leaves a cleanup-only, non-active lifecycle entry;
  mount/unmount/shutdown retries retain ownership until detach and Close both
  succeed. The package does not implement trust or network access. Targets
  without a kernel-backed, process-released lease fail closed before opening
  the mount registry.
- `filesystem/platform/fuse`: Linux-only FUSE syscall translation. Read-only
  is the default and remains kernel-enforced. An explicit write-back mount with
  a matching View-bound capability maps create, offset write, truncate, mkdir,
  rename, unlink, rmdir, and local fsync while continuing to reject unsupported
  metadata, xattr, link, device, and allocation operations. Writable handles
  use direct I/O and re-open the current overlay for reads so write-after-read
  cannot expose a pinned pre-write body. Mount-local stable logical paths move
  atomically with rename; orphaned nodes never reuse a recreated path, and
  forgotten nodes cannot be revived by new operations. Existing open handles
  remain registered through release and follow atomic renames. Unlink or
  overwrite-rename returns `EBUSY` for an open target until the staging layer
  gains stable object handles. `Flush` adds no durability claim;
  `Fsync` requires local durability and rejects accepted-root claims. Exact
  `/proc/self/mountinfo` ownership is still verified before recovering even a
  disconnected stale mount. The adapter has no trust or transport access.
- `localapi`: reusable client for the private daemon control plane, shared by
  the CLI and future thin GUI adapters without direct trust-store mutation.
- `internal/runtime`: process-independent composition of local accepted-root
  selection, per-dataset Gateway verified readers, non-authoritative cache,
  durable mount lifecycle, and the outer platform adapter. For an explicit
  write-back Spec it also owns the concrete per-dataset/branch binding of
  staging state, flat/hybrid UnixFS planning, isolated MALT Core Writer state,
  the untrusted Gateway client-root remote, and candidate-only trust policy.
  Before replay it durably freezes the selected layout for that state directory;
  remounting the same dataset/branch with a different profile fails closed.
  Candidate recording and exact journal completion share the accepted-root
  promotion fence, and post-lease initialization errors return a cleanup-only
  binding to the manager. The binding keeps local journal durability, verified
  remote persistence, and accepted-root promotion separate. Non-Linux targets
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
  transitions, with stateful idempotent unlock/close release so an unlock error
  retains a retryable descriptor and a completed unlock is never repeated on an
  invalid handle.
- `internal/securefile`: owner-only Unix file modes and protected
  owner-and-SYSTEM Windows file DACLs.
- `internal/cas`: runtime-side CAS helpers and byte verification.
- `internal/evaluation`: private adapter support, including evaluation Gateway
  authentication/control transport and shared workload machinery.
- `unixfs/model`: UnixFS application values and path rules.
- `unixfs`: verified UnixFS reader/writer facade, staging, materialization,
  and payload verification.
- `unixfs/encrypted`: runtime-owned encrypted dataset/directory/file manifest
  profile, opaque namespace tokens, AEAD chunking, owner-local snapshot CAS,
  local KZG/IPA Map/List computation, exact remote-publication checks,
  verified full/range reads, and rooted plaintext materialization; it imports
  no trust or concrete transport package.
- `unixfs/clientroot`: flat-v1/hybrid-v1 filesystem-intent projection over a
  verified complete update view. It reconstructs and validates the existing
  manifest/tree projection, applies ordered journal intent, uploads canonical
  manifests with exact returned-CID checks, and emits child-before-parent
  output references for local Core computation. It has no trust, filesystem,
  HTTP, or concrete transport capability.

The `internal` packages are not compatibility promises. The public
`application`, `application/writeback`, `bucketsync`, `cache`, `filesystem/service`,
`filesystem/staging`, `filesystem/mount`, `journal`, `localapi`, `transport`,
`trust`, `unixfs`, `unixfs/encrypted`, `unixfs/clientroot`, and
`merkledag` packages are the intended pre-release integration surface; their
profiles remain experimental until a release policy is published.
Architecture tests fail if production packages import evaluation support, if
`cmd/` gains a non-product binary, if public transport regains an evaluation
control-plane escape hatch, or if Merkle DAG compatibility begins to depend on
transport implementation types or MALT ProofList contracts.
