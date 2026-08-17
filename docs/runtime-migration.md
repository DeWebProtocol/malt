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
| `malt` (formerly `malt-client`; this repository) | `malt` CLI, daemon/local IPC, local trust and key state, UnixFS application semantics, encrypted backup/sync/restore, conflict workspaces, Gateway HTTP transport, and Merkle-DAG compatibility | exact `malt-core v0.0.7`; module path intentionally remains `github.com/dewebprotocol/malt-client` |
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

- `trust` alone persists accepted/candidate roots and requires explicit
  promotion.
- `transport` does not import `application`, `trust`, or `unixfs`.
- `transport/capability` contains no HTTP, URL, account, trust, or Gateway DTO;
  Gateway HTTP is one adapter implementing the same Native, CAS, Mutations, and
  DatasetBranch ports reserved for local, peer, and hybrid implementations.
- `unixfs` verifies ProofLists, resolve-to-read continuity, payload CIDs, and
  range bodies before exposing bytes.
- Bucket workspaces separately persist base, observed remote head, and local
  stashes; a remote head is not promoted into the trust store.
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
- There is no platform-neutral filesystem service, mount registry, verified
  cache state machine, dirty journal, or platform mount adapter yet.
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
package or mutate accepted state. Candidate promotion is an explicit local
policy call. The keyring, recovery keys, encryption epochs, device credentials,
and peer/Gateway observations remain local state with separate persistence
records.

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

The platform-neutral filesystem service will own lookup, verified stat/read,
range reads, staging, rename/unlink semantics, dirty state, fsync policy,
offline journal, and conflicts. FUSE/WinFsp/platform packages only translate OS
operations into this service. A read may return bytes only after path proof,
payload CID, version/root, and encryption-epoch checks.

The cache state machine distinguishes at least:

```text
verified clean | unmaterialized remote | local dirty | pending upload
conflicted | offline-only | stale
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
| 5 | Make trust observations, candidates, and accepted roots explicit persisted types; forbid transport imports | additive state migration | no | state loss or implicit promotion | migration fixtures, stale-head and malicious-result tests, crash/restart tests | dual-read old state with write-new; restore prior state file |
| 6 | Introduce verified cache metadata and operation journal without mounting | new internal APIs | no | treating cache as authority | corruption, stale-version, wrong-epoch, dirty/pending state tests | disable cache/journal feature flag; remote verified path remains |
| 7 | Add platform-neutral read-only filesystem service over UnixFS and mock transport | new experimental API | no | unverified bytes escape | stat/readdir/open/read/range/cache-hit adversarial contract tests | service is additive and can be disabled |
| 8 | Add read-only platform mount adapter and daemon-managed lifecycle | new CLI/local API commands | no | mount leakage or lifecycle races | platform adapter tests, restart/remount, permissions, cancellation | unmount and disable adapter; core runtime remains |
| 9 | Add local dirty staging, write-back, fsync contract, rename/unlink, and offline journal | experimental filesystem API | no | acknowledged data loss or root promotion | dirty/crash/offline/fsync/conflict/malicious apply tests | read-only mode remains default; journal retained for replay |
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
