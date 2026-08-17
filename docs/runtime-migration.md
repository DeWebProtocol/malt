# MALT Local Runtime Migration

Status: implementation plan and current-state audit, updated 2026-08-17.

This document records the repository migration and the incremental conversion
of this repository, renamed from `malt-client` to `malt`, into the MALT local
data runtime. It distinguishes current executable behavior from target
architecture. It is not a wire-format specification; normative Core contracts
remain in `malt-core`.

## Product boundary

MALT is a user-controlled local data runtime. It runs on the user's device,
accesses local files, owns keys and accepted roots, performs local verification,
projects authenticated data into application-facing interfaces, and uses
replaceable transport capabilities. The daemon is the primary long-running
process shape, not the whole product. Gateway, peer, local-only, and hybrid are
network/storage topologies, not product identities.

## Current-state map

| Repository | Current executable responsibility | Current Core dependency |
| --- | --- | --- |
| `malt-core` | Canonical values, Map/List authentication, commitments, ProofLists, Resolve/Read/mutation contracts, client-root Writer, conformance corpora, and reference WASM | none |
| `gateway` | Optional untrusted hosted executor, persistent ArcTable/KV/CAS adapters, Bucket/Branch/account policy, proof production, managed Console, and product E2E | `malt-core v0.0.7` |
| `malt` (formerly `malt-client`; this repository) | `malt` CLI, daemon/local IPC, local trust and key state, non-authoritative cache, operation journal, platform-neutral local dirty staging, verified write-back orchestration and flat/hybrid UnixFS client-root planning, daemon-managed Linux read-only FUSE mounts over the verified filesystem service, UnixFS application semantics, encrypted backup/sync/restore, conflict workspaces, Gateway HTTP transport, and Merkle-DAG compatibility | exact `malt-core v0.0.7`; module path intentionally remains `github.com/dewebprotocol/malt-client` |
| `malt-evaluation` | Reproducible benchmark plans, adapters, result schemas, and provenance | exact `malt-core v0.0.7`, checksum, and release source commit |
| `malt-web` | Public explanation, tutorials, and browser verification tools | browser verifier and provenance rebuilt from exact `malt-core v0.0.7` |

The current repository dependency direction is:

```text
cmd/malt and internal/daemon
              |
              v
       internal/runtime
       (composition root)
              |
              v
 application / application/add / application/backup.BatchRunner
                    / application/writeback
      |              |                 |
      v              v                 v
    trust          unixfs          bucketsync
                      \               /
                       v             v
                  transport/capability
                              ^
                              |
                    transport.Client (Gateway HTTP)
                              |
                              v
                           gateway

application/clientroot and unixfs verification
                              |
                              v
                         malt-core
```

Current boundary strengths:

- `trust` alone persists structured observed, candidate, and accepted root
  states and requires a distinct explicit promotion action for each untrusted
  state class. Legacy v1 trust files retain an owner-only exact-byte recovery
  artifact before they are atomically migrated to v2.
- `transport` does not import `application`, `trust`, or `unixfs`.
- `transport/capability` contains no HTTP, URL, account, trust, or Gateway DTO;
  Gateway HTTP is one adapter implementing the same Native, CAS, Mutations, and
  DatasetBranch ports reserved for local, peer, and hybrid implementations.
- `unixfs` verifies ProofLists, resolve-to-read continuity, payload CIDs, and
  range bodies before exposing bytes.
- Bucket workspaces separately persist base, observed remote head, and local
  stashes; a remote head is not promoted into the trust store.
- `cache` binds every entry to dataset, branch, selected root, revision,
  payload CID, and encryption epoch. Verified reads recompute the CID and invoke
  a local cached-proof verifier; dirty, pending, conflict, offline-only, and
  stale bodies cannot enter that path.
- `journal` durably orders local write/mkdir/rename/unlink intent and freezes a
  retry identity before upload. It can retain a candidate result but has no
  accepted-root mutation capability.
- `filesystem/service` pins every operation to dataset, branch, selected root,
  revision, and encryption epoch. It exposes stat/readdir/open/full/range read
  without transport DTOs; payload-lazy UnixFS lookup lets raw cache hits
  revalidate both the current projection and stored Resolve proof without first
  fetching the remote payload, while List files use authenticated lazy range
  reads and are not misrepresented as raw CID-bound cache bodies.
- `filesystem/staging` layers locally durable read-your-writes state over that
  verified base. It binds every operation and local payload to the same exact
  canonical View identity, exclusively leases the cache and journal paths,
  pins open local handles to a CID, reconciles cross-store crash edges on
  startup, labels fsync as local-journal durability only, and freezes exact
  retry batches with atomic completion/conflict classification. It has no
  transport or trust capability and is not composed into FUSE yet.
- `application/writeback` uploads exact CID-bound bodies through a narrow
  capability, obtains a bounded verified client-root view, normalizes a
  canonical planner result, computes the candidate locally, verifies the
  durable receipt, and records only a candidate before completing the exact
  batch. Its root-policy port has no acceptance method.
- `unixfs/clientroot` rebuilds flat-v1/hybrid-v1 projection from the verified
  complete view, verifies old/new manifest CIDs, applies the complete ordered
  overlay, and emits output-free child-before-parent intent for KZG or IPA. It
  imports neither trust nor a concrete transport.
- `filesystem/mount` persists desired mounts and pending-unmount tombstones,
  excludes competing daemon managers with a process-held registry lease,
  restores records through an idempotent platform adapter contract, and gives
  that adapter only a read-only filesystem already bound to a locally accepted
  View. Unsupported lock targets fail closed before registry access. The
  private daemon API and `localapi` client delegate list/mount/unmount to the
  same manager. On Linux, `internal/runtime` selects only the local accepted
  UnixFS root, builds per-dataset Gateway verified readers, and composes the
  concrete FUSE adapter; observed heads only contribute matching revision
  metadata.
- CLI foreground execution, daemon IPC, and scheduled execution share the same
  `application/backup.BatchRunner`; `internal/runtime.Services` supplies one
  configuration snapshot and the same backup services, local locking, and
  candidate/acceptance policy to every adapter.
- Merkle-DAG compatibility evidence is isolated from MALT ProofLists.

Current implementation gaps that still encode concrete Gateway coupling:

- The root `transport.Client` still owns all concrete Gateway HTTP routes in
  one package; it now implements transport-neutral ports, but the concrete
  adapter has not yet moved under `transport/gateway`.
- `cmd/malt/content.go` and `cmd/malt/add.go` still directly compose concrete
  Gateway clients and UnixFS adapters. Backup/sync plan composition has moved
  to `internal/runtime`, but the remaining use cases still need the same
  container boundary.
- Root command handlers repeatedly open the trust store and construct
  `application.Roots`; read handlers repeatedly construct a concrete Gateway
  Gateway transport, UnixFS reader, selector, and application service.
- Configuration has one mandatory-looking `gateway` block rather than a
  transport/dataset binding model.
- WinFsp and platform write composition remain follow-up work; non-Linux
  daemons keep the mount API unavailable until a native adapter is added.
- There is no local-CAS, peer, or hybrid transport implementation yet.

No code in `malt-core` owns UnixFS, Bucket, account, daemon, key persistence,
mount, or Gateway routes. No current migration step changes the MALT wire
profile, Root/CID codec, commitment transcript, ProofList encoding, mutation
serialization, or receipt format.

## Target repository graph

```text
                         malt-core
                    (authentication SDK)
                     ^       ^       ^
                     |       |       |
                  malt    gateway   malt-web verifier
             (local runtime)  |
                ^      ^      |
                |      |      |
      malt-evaluation  +------+
      (external pinned processes and recorded provenance)
```

`malt-core` has no dependency on any product repository. `malt` may run with no
Gateway. Gateway and peers return untrusted observations, proofs, payloads, and
apply results. Only the local runtime verifies them and applies local acceptance
policy.

## Target runtime modules

```text
CLI / daemon / GUI / local API / platform mount adapters
                           |
                           v
                  application services
      backup | restore | sync | mount | root | conflict
             /             |                \
            v              v                 v
    application adapters  trust/state      filesystem service
       UnixFS/Merkle-DAG   keys/roots       projection/cache/journal
            \               |                 /
             +--------------+----------------+
                            |
                            v
                 semantic transport capabilities
               resolve/read | CAS | mutation | heads
                    /          |          \
                   v           v           v
             Gateway HTTP   Local CAS   future Peer/Hybrid

All proof verification, payload-CID verification, and candidate-root
computation/validation call malt-core and cannot be bypassed by a transport.
```

### Application boundary

Application services own user-level use cases and orchestration. CLI, daemon,
GUI, local API, and mount adapters must call these services instead of opening
trust/config state or constructing network clients independently. Composition
configuration belongs in a reusable runtime/service container outside Cobra
handlers.

### Trust boundary

The trust plane owns three distinct values:

```text
observed remote head != candidate root != accepted root
```

Transport may return observations and results but cannot import the trust
package or mutate accepted state. Candidate and observation promotion are
distinct explicit local policy calls. The keyring, recovery keys, encryption
epochs, device credentials, and peer/Gateway observations remain local state
with separate persistence records.

### Transport capability boundary

Gateway, local, peer, and hybrid transports implement the same semantic ports
now defined in `transport/capability`:

```go
type Native interface { Resolve(context.Context, protocol.ResolveRequest) (*protocol.ResolveResult, error); Read(context.Context, protocol.ReadRequest) (*protocol.ReadResult, error) }
type CAS interface { Get(context.Context, cid.Cid) ([]byte, error); Put(context.Context, []byte) (cid.Cid, error) }
type Mutations interface { ApplyMutation(context.Context, mutation.SemanticMutation) (MutationResult, error) }
type DatasetBranch interface { DatasetBinding() DatasetBinding; ObserveHead(context.Context) (*ObservedHead, error); ApplyCandidate(context.Context, ApplyRequest) (*ApplyResult, error) }
```

The exact interfaces may stay split more narrowly than this sketch. They must
not contain URLs, HTTP headers, Gateway route names, account DTOs, or trust-store
methods. Dataset identity is separate from a Gateway URL. Capability results
are untrusted by type and naming.

### Filesystem boundary

The platform-neutral filesystem service owns lookup, verified stat/read, and
range reads; later phases extend the same boundary with staging, rename/unlink
semantics, dirty state, fsync policy, offline journal, and conflicts. FUSE/WinFsp/platform packages only translate OS
operations into this service. A read may return bytes only after path proof,
payload CID, version/root, and encryption-epoch checks.

The cache state machine distinguishes at least:

```text
verified clean | unmaterialized remote | local dirty | pending upload
candidate | conflicted | offline-only | stale
```

A cache hit is never authority. Cache keys bind CID, selected MALT root,
dataset/branch revision, encryption epoch, verification status, and dirty state.

### Control-plane adapters

```text
CLI -----\
daemon ---+--> application/runtime service container
GUI ------+             |
local API-/             +--> trust / sync / filesystem / transports
```

The GUI is a thin local control client. It cannot write root/key files or call
Gateway routes directly. Unix uses a private owner-only socket; Windows uses a
protected named pipe. A loopback HTTP adapter, if added, is a local interface
choice and does not define network topology.

## Repository and release checklist

- [x] Rename the Core GitHub repository from `malt` to `malt-core` without
  rewriting history.
- [x] Change the Core Go module to `github.com/dewebprotocol/malt-core`.
- [x] Preserve old Core tags/releases and document namespace migration.
- [x] Publish final `malt-core v0.0.7` from the merged Core source.
- [x] Bind Core release assets to tag, commit, module sums, conformance corpora,
  toolchain, and reproducible checksums.
- [x] Migrate Gateway Go imports, module pin, Console WASM, and provenance to
  exact `malt-core v0.0.7`.
- [x] Migrate this runtime's Core imports and pin exact `malt-core v0.0.7`.
- [x] Migrate `malt-evaluation` imports and record exact module/source
  provenance without relabeling historical results.
- [x] Migrate `malt-web` browser build metadata, links, and public terminology.
- [x] Update organization metadata, badges, CI, examples, and
  contribution links.
- [x] Prove no active dependency uses the old Core module path, except explicit
  historical/migration notices.
- [x] Run cross-repository verified read/write and malicious-response E2E.
- [x] Rename the GitHub repository `malt-client` to `malt` only after all Core
  consumers are migrated.
- [x] Keep `module github.com/dewebprotocol/malt-client` during the initial
  runtime refactor; continue enforcing this invariant until a separate cutover.
- [ ] Design the runtime module namespace cutover as a separate pre-v1 change;
  do not publish a runtime release under the old Core namespace accidentally.

## Staged pull-request plan

| PR | Goal and packages | Public API | Wire format | Main risk | Verification | Rollback |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | Product language plus `malt-core v0.0.7` migration across imports, docs, architecture tests, and `go.mod` | import namespace only | no | missed old import/provenance | full Go test/vet/build, Windows build, downstream E2E | revert one namespace commit; old Core release remains available |
| 2 | **Completed 2026-08-17:** after every Core consumer migrated, rename repository to `malt`, update links/badges/CI, keep the old Go module path, and add a compatibility notice before runtime architecture refactors begin | repository URL only | no | redirect/module namespace confusion | clean clone, old/new URL checks, all dependent CI | GitHub repository rename is reversible; module stays unchanged |
| 3 | **Completed 2026-08-17:** move backup/sync composition into `internal/runtime.Services` and plan batch orchestration into `application/backup.BatchRunner`; foreground, daemon IPC, and scheduler use the same application contract | additive application constructors; command internals change | no | daemon/foreground behavior drift | table-driven batch semantics, direct-vs-daemon adapter equivalence, current CLI tests | revert command call sites and the two additive services; persisted state is unchanged |
| 4 | **Completed 2026-08-17:** define URL-free `transport/capability` Native/CAS/Mutations/DatasetBranch ports, adapt Gateway HTTP into typed untrusted values, move Bucket sync and UnixFS mutation consumers off concrete DTOs, and retain deprecated pre-release aliases | additive ports plus pre-v1 `PushOutcome.Result` type rename; deprecated constructors retained | no; persisted JSON tags unchanged | accidental trust mutation, request mismatch, or route leakage | binding/request/result adversarial contracts, Gateway adapter tests, JSON-field compatibility, architecture imports | select the deprecated Gateway adapters; no persisted-state migration is required |
| 5 | **Completed 2026-08-17:** persist `ObservedHead`, `CandidateRoot`, and `AcceptedRootState` separately in trust-store v2; preserve an exact owner-only v1 recovery artifact before atomic migration; record backup heads as observations; add distinct candidate/observation acceptance APIs | additive API plus trust-store v2 migration; flattened `Record` retained for accepted-root aliases | no | state loss, stale observation, or implicit promotion | v1 fixture migration and write/sync/secure fault injection, stale/same-revision conflict, malformed root, restart, wrong acceptance-route, and backup observation tests | stop the runtime and replace the v2 store with `<trust-store>.v1-recovery`; the runtime retains this exact v1 artifact until an operator removes it |
| 6 | **Completed 2026-08-17:** introduce non-authoritative verified-cache metadata/bodies and an ordered operation journal without mounting; require exact dataset/branch/root/revision/CID/epoch identity, fresh proof-evidence verification on every verified hit, CID recomputation, explicit cache transitions, crash-orphan reconciliation, frozen retry tombstones, non-replayable unresolved conflicts, and new identities for conflict replacements | additive pre-release `cache` and `journal` APIs | no | treating cache as authority, orphaning sensitive bytes, erasing pending/conflict state, or changing/reusing retry identity after an ambiguous upload | corrupt/missing body, invalid proof, stale root/revision/wrong epoch, invalid UTF-8, impossible persisted state, full transition bypass matrix, failed remove/metadata commit, crash reconciliation, lock-held remove/replace, concurrent fill, restart, ordered replay, identity/tombstone reuse, malformed superseded graph, unresolved-conflict exclusion, replacement/completion, CID canonicalization, and concurrent writer tests | do not compose the additive stores; remote verified reads remain the active path and journal records remain recoverable |
| 7 | **Completed 2026-08-17:** add `filesystem/service` over the verified UnixFS reader with immutable dataset/root/revision/epoch views, stat/readdir/open/full/range reads, raw CID cache proof revalidation, legacy directory projection, and List-range laziness | new additive pre-release API | no | unverified bytes escape or cache becomes authority | selected-root mismatch, stat/readdir/open/closed handle, raw CID corruption, invalid cached proof, wrong revision, lazy List range, cache-hit and architecture contract tests | do not compose the additive service into a platform mount; existing CLI read paths remain unchanged |
| 8a | **Completed 2026-08-17:** add durable `filesystem/mount` desired/pending-unmount state, an exclusive process-held manager lease on supported Linux/macOS/BSD and Windows targets, fail-closed rejection elsewhere, exact local-View selector, platform adapter/session contract, restart restore and graceful shutdown semantics, plus private daemon list/mount/unmount routes | additive pre-release manager and local API | no | leaked mount, competing daemon, observed-head selection, or lost unmount intent | persistence/permissions/required-field strict JSON, failed mount retry, tombstone non-revival, pending-unmount crash recovery, exclusive-manager lease, supported/unsupported platform builds, Unix/Windows replacement builds, restart/remount, expected/unexpected session exit, view mismatch, daemon adapter parity and race tests | leave the manager unconfigured; no host mount is created and desired records remain recoverable |
| 8b.1 | **Completed 2026-08-17:** add the outer Linux read-only FUSE adapter with exact `malt:<id>` mount ownership, fail-closed disconnected-root recovery, explicit `EROFS` mutation behavior, verified range-read handles, and an opt-in kernel smoke test | additive Linux adapter and pinned go-fuse dependency; not product-composed | no | foreign unmount, unverified byte exposure, unsafe mountpoint, or leaked session | node/handle/errno tests, invalid entry/kind rejection, exact/stacked mountinfo selection, disconnected-root recovery, identity-cleanup, cancellation/idempotence race test, and real `/dev/fuse` smoke | do not compose the adapter; no host mount is created by daemon or CLI |
| 8b.2 | **Completed 2026-08-17:** compose the Linux adapter, local accepted-View selector, per-dataset/branch Gateway verified filesystem router, protected cache/registry paths, daemon restore/shutdown, reusable `localapi`, and `malt mount/unmount` control commands; reject encrypted Views until local decryption exists and leave non-Linux mount routes unconfigured | new CLI/local API behavior and additive config fields | no | wrong dataset/root binding, observed-head promotion, startup mount leakage, or control-plane drift | accepted/candidate/observation selector tests, source/dataset/branch revision binding, encrypted-view rejection, router identity/cache, CLI/API parity, daemon/mount lifecycle, Windows build, Linux FUSE smoke | stop the daemon, unmount desired bindings, and leave the registry/cache recoverable; other daemon services remain available |
| 9a | **Completed 2026-08-17:** add `filesystem/staging` as a platform-neutral, crash-recoverable read-your-writes overlay; exclusively lease both state paths; stage raw-CID-bound write bodies plus mkdir/rename/unlink intent against an exact canonical immutable View; pin local handles; reconcile cache/journal crash edges; and define `malt.local-journal-fsync/v1` without remote-persistence or root-acceptance claims | additive experimental filesystem API; not mounted | no | acknowledged local data loss, cross-Service reconcile race, dirty-state leakage across roots, or false fsync claims | create/overwrite/range/pinned-handle, mkdir/rename/unlink/rmdir, concurrent/offline staging, exact-View/canonical-identity isolation, exclusive pair lease/release, restart, missing body, orphan body, failed append, cancellation, and architecture tests | close the staging service and do not compose the overlay into FUSE; current mounts remain read-only and the durable journal remains recoverable |
| 9b.1 | **Completed 2026-08-17:** add exact atomic upload batches plus transport-neutral verified write-back orchestration; upload locally CID-verified bodies, verify a bounded client-root view, normalize canonical intent, locally compute the candidate, verify the durable receipt, record only a candidate, and preserve accepted-root races as conflicts | additive experimental staging/write-back APIs and additive `candidate` cache state; on-disk schema version remains 1 | no | partial batch mutation, substituted payload/receipt, retry ambiguity, stale accepted root, or implicit root promotion | exact complete-set batch/retry/completion/conflict, post-journal cache-failure repair, crash reconciliation, local/remote payload substitution, malicious receipt, stale-root precheck/race, client-root candidate, and race tests | leave operations dirty/offline/pending/conflicted and keep accepted root unchanged; before downgrading to a binary that predates `candidate`, finish or reset candidate cache records |
| 9b.2 | **Completed 2026-08-17:** add the concrete flat-v1/hybrid-v1 UnixFS client-root planner; reconstruct exact manifest/authenticated-binding projection, replay the complete ordered overlay, verify old/new manifest CIDs, emit child-before-parent output-free intent for local KZG/IPA computation, and explicitly complete verified no-change batches without mutation or candidate recording | additive experimental `unixfs/clientroot` planner API and write-back planner result | no | incorrect ancestor projection, incomplete-view candidate, manifest CID substitution, backend/object-identity drift, or permanently frozen no-op intent | flat/hybrid write/mkdir/rename/unlink and new-directory combined replay, independently verified next views under KZG/IPA, equal-content and canceling namespace no-change, restart/exact retry, corrupt old manifest, malicious returned manifest CID, immutable-View and frozen-status rejection, and race tests | leave the planner and generic orchestrator uncomposed; existing journal intent remains recoverable |
| 9c | Add an explicit opt-in writable platform policy and map FUSE mutations/fsync onto staging plus verified write-back | experimental mount policy | no | host syscall acknowledged beyond configured durability | write/flush/fsync/rename/unlink/crash/remount kernel tests | read-only remains default; disable writable policy and retain journal |
| 10 | Add local-CAS transport and a peer-ready contract test implementation; reserve hybrid policy outside application code | additive transport implementation | no | backend-specific behavior leaks upward | same contract suite against mock/Gateway/local transports | select Gateway transport in config |
| 11 | Separate pre-v1 runtime module namespace decision and release line | breaking Go import change if approved | no | collision with historical Core path | external consumer build with isolated module cache | do not tag; keep old module until cutover is proven |

No PR may combine a wire-profile change with repository/module renaming. Each
PR is independently revertible and preserves on-disk state through explicit
migration or additive schema versions.

## Test plan

### Core and dependency conformance

- Run all `malt-core` tests, 386-bit KZG/IPA suites, vet/build, verifier WASM,
  writer WASM, frozen Resolve/Read, Map-proof, and client-root corpora.
- Assert candidate-root computation matches frozen KZG and IPA vectors.
- Download `malt-core v0.0.7` through an isolated module cache with no local
  `replace` directive and verify module/go.mod sums and tagged source commit.

### Trust and verified I/O

- Verify Resolve and Read from a caller-selected accepted root.
- Reject invalid ProofLists, cross-root proofs, wrong keys/paths, stale remote
  heads, substituted mutation receipts, and server-side merge roots that do not
  recompute locally.
- Hash every returned raw, manifest, chunk, range, and ciphertext payload
  against the authenticated CID before exposing or decrypting it.
- Reject corrupted payloads, wrong encryption epochs/keys, non-canonical
  manifests, range gaps/overlaps, and proof/payload substitution.
- Prove observed head, candidate root, and accepted root remain distinct across
  success, retry, conflict, restart, and malicious responses.

### Transport contracts

- Run one semantic contract suite against mock, Gateway HTTP, and future local
  transports.
- Assert transport packages cannot import trust/application/filesystem.
- Assert filesystem/application packages cannot import Gateway HTTP DTOs or
  route constants.
- Assert a transport cannot mark a result accepted or mutate a trust store.
- Inject timeout, duplicate/reordered response, rollback head, malformed JSON,
  oversized response, invalid proof, corrupted block, and unavailable backend.

### Daemon and foreground equivalence

- Execute backup, sync, root observation, candidate recording, and explicit
  acceptance through daemon and foreground adapters using the same application
  fixtures.
- Compare resulting roots, state bytes (excluding timestamps/instance IDs),
  journals, errors, and conflict workspaces.
- Restart during every durable transition and verify idempotent recovery.

### Filesystem and cache

- Read-only mount: stat, readdir, open, sequential read, random/range read,
  empty directory, large file, missing path, cancellation, unmount, restart,
  and remount.
- Cache: verified clean hit, missing body, corrupted body, wrong root/revision,
  wrong epoch, stale entry, concurrent fill, eviction, and offline verified
  reads. A hit must repeat identity/root/CID checks.
- Writes: create, overwrite, mkdir, rename, unlink, fsync, close, write-back,
  pending upload, crash before/after upload, and retry identity.
- Offline journal: ordered replay, duplicate replay, partial replay, disk-full
  failure, corrupt record, and recovery without silently accepting a root.
- Conflict: same-path concurrent write, independent paths, three-way merge,
  conflict branch, keep-local/keep-remote/manual, and restart with unresolved
  conflict.
- `fsync` tests must distinguish local-journal durability from remote
  persistence and trusted-root acceptance.

### End-to-end and compatibility

- Build runtime and Gateway independently from pinned public revisions with no
  workspace `replace` directives.
- Run Gateway verified read/write, Bucket backup/sync/restore, range read,
  encrypted restore, malicious Gateway response, stale head, invalid proof,
  corrupted payload, wrong key, restart, and remount scenarios.
- Preserve existing on-disk `~/.malt-client` paths and daemon environment/header
  names until a separately tested state migration is introduced.
- Confirm old Core tags/releases remain downloadable and final `v0.0.7` wire
  behavior is unchanged from the last RC apart from the Go module namespace and
  newly frozen conformance inputs.

## Acceptance gates

The migration is complete only when Core is application-neutral, the renamed
`malt` repository is a local runtime, all adapters share application services,
Gateway is optional and untrusted, filesystem reads expose only verified bytes,
accepted/candidate/observed roots remain separate, cache and transports cannot
bypass trust, and a future peer transport can be added without changing UnixFS,
filesystem, backup, sync, or trust logic.
