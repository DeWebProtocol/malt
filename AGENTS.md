# AGENTS.md

## Scope

This repository owns the user-controlled MALT local data runtime: CLI, local
daemon, accepted-root policy, UnixFS application semantics, Merkle DAG import
compatibility, pluggable transport capabilities, and payload-byte verification.
It also keeps the runtime-owned executable adapters needed to measure those exact behaviors;
the evaluator that plans and interprets campaigns lives in `malt-evaluation`.

## Boundaries

- Do not define MALT protocol, ProofList, commitment, CID, schema, or canonical
  graph semantics here; use the `malt-core` module.
- Do not add ArcTable/KV persistence or managed gateway policy here; those
  belong in `gateway`.
- Treat gateway responses as untrusted and verify against caller-selected
  roots locally.
- Never promote a mutation result to a trusted root without explicit local
  acceptance or an independent root-publication policy.
- Keep application separators and UnixFS path rules in this repository. Pass
  typed segment arrays to MALT core.
- Keep IPFS-style Merkle DAG import and compatibility policy here. A Merkle DAG
  root is a compatibility output, not a MALT-authenticated root or ProofList.
- Keep benchmark suites, run plans, comparison policy, result schemas, and
  result provenance in `malt-evaluation`. Code under `tools/evaluation` is a
  pinned process adapter, not a supported runtime command or an evaluator
  source of truth.

## Package Ownership

- `transport/capability` owns URL-free, route-free, trust-free semantic ports
  and untrusted dataset/mutation results shared by Gateway, local, peer, and
  hybrid implementations. `transport/capabilitytest` owns the reusable adapter
  contract, `transport/local` owns the bounded durable local CAS, and
  `transport/hybrid` owns Gateway-primary/read-through-CAS policy. Hybrid `Has`
  remains primary-authoritative and every cache body is CID-verified. Local
  CAS reads reject unsafe metadata without modifying it; runtime composition
  must shard by digest bytes, preserve owned local directory handles, report a
  failed release, and retry cleanup until a confirmed close. Mounts must hold
  one read-side View lease and release the last dataset/branch reference after
  detach and binding cleanup, including failed mounts. A release-pending route
  must reject new acquisitions rather than revive a partially closed service.
  `os.File.Close` errors are terminal diagnostics: discard the invalid handle
  and never attempt I/O through it. Do not automatically chmod or replace an
  unreadable shard directory; require explicit offline owner/`0700` repair so
  unrelated blocks remain intact. The root
  `transport` package is the current Gateway HTTP adapter plus compatibility
  DTOs. None may import `trust`, `unixfs`, or `merkledag`. Do not expose
  evaluation instance credentials, bootstrap controls, unchecked raw-CAS
  reads, or selective-CAR routes here.
- `trust/` is the only package that persists observed heads, locally verified
  candidates, and accepted roots. These are separate v2 state types:
  observations cannot enter the candidate path, and neither observations nor
  candidates promote without their own explicit local acceptance action. The
  package must not depend on network or application packages. Any local
  classification whose correctness depends on one accepted root must use the
  same process and cross-process promotion fence as the trust store.
- `cache/` owns non-authoritative payload-cache metadata and bodies. Every
  verified hit must bind dataset, branch, selected root, revision, payload CID,
  and encryption epoch, recheck the CID, and invoke a local proof verifier.
  Dirty-body recovery and range reads must preflight metadata and actual file
  size, then verify the CID over the same bounded or streamed bytes they
  classify or return. Cache state must never import or mutate trust, transport,
  or application policy.
- `journal/` owns transport-neutral ordered local operation intent, retry
  identity, offline/pending/conflict state, and crash replay metadata. It may
  record a candidate/result root but cannot accept it and must not import trust,
  transport, UnixFS, or application packages.
- `filesystem/service` owns platform-neutral stat, readdir, open, and read
  semantics over a caller-selected immutable dataset view. It may compose the
  verified UnixFS reader and non-authoritative cache, but it must not import a
  concrete transport, trust store, HTTP route, or platform mount driver.
- `filesystem/staging` owns the platform-neutral read-your-writes overlay and
  locally durable whole-file/offset write, truncate, mkdir, rename, unlink, and
  fsync intent for one exact immutable dataset view. It composes `cache`,
  `journal`, and the verified read-only filesystem port, but it must not import
  transport, trust, application, HTTP, or MALT Core packages. It must
  exclusively lease both state paths across processes before reconciling or
  acknowledging intent. Local fsync is not remote persistence, candidate-root
  verification, or accepted-root promotion. Until chunked/sparse staging is
  implemented, reject whole-file materialization above the configured limit
  before remote reads or allocations.
- `application/writeback` owns transport-neutral replay of a leased staging
  batch. It plans before payload publication, uploads only final staged raw
  bodies referenced by the normalized intent, invokes the MALT Core
  client-root workflow, verifies the durable receipt, records only a candidate,
  and then completes the exact batch under the accepted-root promotion fence or
  atomically conflicts it. It must not import a concrete Gateway transport or
  expose accepted-root promotion.
- `unixfs/clientroot` owns the concrete flat-v1/hybrid-v1 projection from a
  verified complete update view plus durable filesystem operations into an
  output-free semantic intent. It verifies old and newly stored manifest CIDs
  and must not import trust, filesystem, application, HTTP, or a concrete
  transport.
- `filesystem/mount` owns durable desired/pending-unmount state and the
  daemon-managed lifecycle contract. One process-held registry lease excludes
  competing managers on supported Linux/macOS/BSD and Windows targets; other
  targets must fail closed before opening the registry. Platform adapters
  receive a filesystem already bound to a locally selected View. The default
  capability is read-only; an explicitly validated write-back Spec may receive
  one session-owned writable binding. Only one write-back mount may reserve a
  dataset/branch at a time. Failed platform detach or binding Close must retain
  retryable cleanup ownership and the relevant leases; confirmed detach must
  become cleanup-only before Close. Adapter Session values must reject typed
  nil and capture one stable completion channel. Adapters must not resolve
  trust aliases, observe heads, call transports directly, or own the binding
  lifetime.
- `filesystem/platform/fuse` is the outermost Linux syscall adapter. It may
  import go-fuse and `filesystem/mount`, but not trust, transport, cache,
  application, or Gateway packages. Recovery unmount must verify the exact
  MALT-owned mount identity, remain usable for disconnected FUSE roots, and
  refuse foreign or ambiguous stacked filesystems. Read-only Specs must keep
  the kernel mount read-only even if a wider dynamic capability is supplied.
  Write syscalls require both an explicit write-back Spec and a matching
  `WritableFilesystem`; their handles use the current overlay rather than a
  pinned pre-write read handle. Mount-local logical inode paths must update
  atomically with rename, and an orphaned inode must never fall back to a path
  that can be reused. A forgotten node cannot be revived by a new operation,
  while an already-open handle remains registered until release and follows
  atomic renames; unlink or overwrite-rename of an open target fails with
  `EBUSY` until object-identity-based staging exists. `Flush` cannot claim
  remote persistence, and `Fsync` succeeds only after `Sync` confirms local
  durability without accepted-root promotion.
- `localapi/` owns reusable clients for the private daemon control plane. CLI
  and future GUI adapters may use it, but it must not import trust, transport,
  cache, UnixFS, application, or Gateway packages.
- `application/` owns reusable accepted-root selection, observation/candidate recording,
  explicit acceptance, UnixFS use cases, bulk local-input staging, and Merkle
  DAG import/read orchestration shared by CLI and daemon adapters. It depends
  on narrow ports, not Cobra or arbitrary transport routes.
- `application/backup.BatchRunner` owns plan selection, batch execution, and
  typed partial-failure aggregation. Foreground, daemon, and scheduled adapters
  must use that same runner contract.
- `bucketsync` production synchronization uses
  `transport/capability.DatasetBranch`; its concrete Gateway DTO adapter lives
  only in `gateway_compat.go` for the pre-release compatibility window.
- `internal/runtime` is the process-independent composition root. It may bind
  concrete local configuration, transport, trust, keyring, synchronization,
  UnixFS, verified filesystem, cache, and platform-mount capabilities into
  application services; command handlers must not duplicate that composition.
  An explicit write-back mount is bound here from one exact accepted View to
  per-dataset/branch staging, a declared UnixFS planner, an isolated Core
  Writer, untrusted transport capabilities, and candidate-only trust policy.
  It durably freezes the layout profile for that dataset/branch before replay;
  a changed layout must fail closed while the state is retained. Initialization
  after lease acquisition must return a cleanup-only partial binding on error so
  the mount manager can retry release. The binding must never infer remote
  persistence from cache/journal state or promote an accepted root.
- `unixfs/` owns the MALT-authenticated UnixFS facade, staging,
  materialization, and payload/range verification. Keep reusable UnixFS
  behavior here rather than under `cmd/malt`. Its semantic mutation adapter
  consumes `transport/capability.Mutations`; only `gateway_compat.go` may
  import legacy Gateway mutation DTOs during the compatibility window.
- `merkledag/` owns the compatibility adapter and local CID/link-evidence
  replay; `merkledag/importer` owns import construction. Do not represent this
  evidence as a MALT ProofList.
- `cmd/malt/` is the process/composition adapter. Command handlers should
  select configured runtime services, call `application/` use cases, and
  format results, not become a second UnixFS, trust, or plan-composition
  implementation. `cmd/` contains only the supported `malt` product binary.
- `tools/evaluation/cmd/` owns evaluator-launched runtime process adapters.
  Preserve their external binary names and wire contracts, but do not treat
  their Go packages as a compatibility surface.
- `internal/evaluation/` owns shared implementation for those adapters,
  including disposable-Gateway instance authentication, bootstrap, raw-CAS,
  and selective-CAR transport. Production packages and `cmd/malt` must not
  import it.
- Preserve `internal/architecture` dependency tests when adding packages or
  moving responsibilities.

## Validation

Run `gofmt`, `git diff --check`, `go test ./...`, `go vet ./...`, and
`go build -buildvcs=false ./...` before committing. Also run a Windows
cross-build for changes to daemon lifecycle, locking, filesystem, or CLI code.
