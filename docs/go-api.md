# Go API and CLI contracts

This document describes the pre-release integration surface owned by the MALT
local runtime. Its Go module still uses the
`github.com/dewebprotocol/malt-client` namespace during the initial refactor.
MALT protocol and ProofList schemas remain defined by the MALT Core repository.

## Public gateway transport

Transport-neutral ports live in
`github.com/dewebprotocol/malt-client/transport/capability`. They identify a
logical dataset and branch without a URL and return explicitly untrusted typed
values:

```go
var native capability.Native = remote
var blocks capability.CAS = remote
var mutations capability.Mutations = remote
var dataset capability.DatasetBranch = remote

binding := dataset.DatasetBinding()
observed, err := dataset.ObserveHead(ctx)
result, err := dataset.ApplyCandidate(ctx, capability.ApplyRequest{
    OperationID: "device-operation-id",
    BaseCommit: observed.CommitID, BaseRoot: observed.Root,
    BaseRevision: observed.Revision, CandidateRoot: candidate.String(),
})
```

These interfaces contain no HTTP route, URL, account DTO, or trust-store
method. `MutationResult`, `ObservedHead`, and `ApplyResult` cannot promote an
accepted root. `ValidateBinding`, `NormalizeApplyRequest`, and
`ValidateApplyResult` fail closed before or after an untrusted implementation.

The root `transport` package is currently the managed Gateway HTTP adapter.

Import `github.com/dewebprotocol/malt-client/transport` and construct a validated
transport:

```go
remote, err := transport.New(transport.Options{BaseURL: "https://gateway.example"})
```

Select authenticated managed-Bucket routes with:

```go
remote, err := transport.New(transport.Options{
    BaseURL:          "https://gateway.example",
    TenantBearerToken: os.Getenv("MALT_GATEWAY_API_KEY"),
    BucketID:          "bkt_...",
})
head, err := remote.BucketHead(ctx)
result, err := remote.PushBucket(ctx, transport.BucketPushRequest{
    PushID: "device-operation-id", BaseCommit: head.CommitID,
    BaseRoot: head.Root, BaseRevision: head.Revision,
    CandidateRoot: candidate.String(),
})
```

Interactive device login uses `Options.DeviceAuthorizer` instead. The
authorizer adds a fresh proof-of-possession signature after the request body
and URI are final; `TenantBearerToken` and `DeviceAuthorizer` are mutually
exclusive. `internal/deviceauth.FileProvider` is the current CLI-owned
owner-only software fallback. The exported interface allows a hardware-backed
signer to be composed later without exposing private-key storage through
transport.

`BaseRevision` records the main-ref generation observed alongside the captured
base. It is descriptive synchronization metadata, not a client-supplied CAS
token and not an ordering constraint on a replayed push result. Gateway performs
ref CAS against the generation it reads while handling the request.

HTTP `409` from a conflicting push is decoded as a valid `branched`
`BucketPushResult`; it contains the unchanged `main`, the preserved conflict
branch, and coordinates that could not be merged. Every push result explicitly
returns both the submitted `candidate` and the final `commit`: they are equal
for fast-forward and branched results, while an automatic merge returns a
distinct final commit whose head and parent bindings are checked before the
result is exposed. Commit IDs are opaque version identifiers; callers must not
infer semantics from their text format.

`Options` also exposes independent JSON, blob, and error-response byte limits.
Defaults are 96 MiB, 64 MiB, and 1 MiB respectively. Limits apply after HTTP
decompression; oversized and trailing JSON responses are rejected before their
contents are trusted or returned.

The transport exposes generic `Resolve` and `Read`, immutable CAS ingest
`Put`, bounded `PutBatch`/`HasBatch`, root creation, semantic mutation, typed
diagnostic metrics, diagnostic verifier calls, and two fixed Merkle DAG
compatibility methods. Single-value CAS `Get`/`Has` are available only when a
managed Bucket is selected, and fail locally instead of attempting the removed
public raw-CAS read route. It intentionally has no arbitrary profile-route
method. Transport methods validate wire shape and CAS bytes, but generic
resolve/read results remain untrusted until locally verified against caller
inputs.

The public transport does not expose evaluation instance tokens, bootstrap
control, unchecked raw-CAS reads, or the selective-CAR route. Those
capabilities are private to the pinned process adapters under
`tools/evaluation`; they are not a supported integration surface.

`Metrics` returns inexpensive monotonic counters. `MetricsWithStorage` also
requests Gateway's O(live KV entries) logical scan and should be used only by
controlled evaluation or operator tooling. Construct the transport with
`Options.OperatorBearerToken`; the token is attached only to the logical
storage-metrics request. A credentialed transport requires HTTPS unless its
Gateway host is loopback (localhost, 127.0.0.0/8, or ::1). Credentialed
requests reject redirects instead of forwarding bearer credentials or signed
requests to another URL.

No exported signature contains a type from `internal/`.

Package `bucketsync` provides the durable runtime workflow used by the CLI.
New code constructs it with `OpenRemote` or `OpenRemoteBranch` and a
`capability.DatasetBranch`. The former `Open`/`OpenBranch` Gateway-DTO surface
is retained as a deprecated compatibility adapter during the pre-release
migration.
Call `CurrentBase` before materializing local work, then `Stage` the candidate
against that captured commit/root/revision. `Push` refuses unstaged candidates,
calls `ObserveHead` without changing the stash, leaves it pending across network
failures, and submits the original base with a stable push ID. `Pull` updates
the observed remote head but does not replace a base while pending stashes
exist. The message and change-set fields are frozen before the first network
attempt and reused after response loss or process restart; a retry that
explicitly supplies different values fails locally. Malformed push responses
leave the durable stash pending. This metadata is distinct from the accepted
root policy in package `trust`.

Bucket workspace schema version 3 persists branch-qualified workspace keys and
`request_frozen` explicitly. When a version 1 file is opened, every pending
stash is conservatively treated as
possibly sent: its original base, push ID, message, and change-set are frozen
and atomically rewritten as version 3 before retry is possible. Version 1 had
no durable sent/not-sent distinction, so this rule also freezes a legacy stash
that in fact never reached Gateway. A version 2 file must contain an explicit
boolean `request_frozen` for every stash before its main-only key is migrated;
incomplete v2 records are rejected rather than interpreted as unsent work.

## Reusable application use cases

Package `application` is the composition layer shared by CLI and daemon
adapters:

```go
roots, err := application.NewRoots(policy)
files, err := application.NewUnixFS(reader, writer, roots)
result, err := files.ReadFile(ctx, "accepted-alias", "docs/readme.md")
candidate, err := files.RemovePath(ctx, "accepted-alias", "old.txt")
```

`Roots.Select` treats a CID-shaped positional selector as an explicit CID.
Callers with an explicitly typed alias input must use `Roots.LookupAlias`,
which always consults accepted-root policy even when the alias itself is valid
CID text. `application/add` uses that strict path for `Request.Alias` and the
CLI's `--alias` flag. `AddFile`, streaming add, directory add, and remove
return independently checked candidates. When an alias was selected, the use
case records the candidate against that exact accepted base, but never
promotes it. Promotion requires `Roots.AcceptCandidate`.

`application.MerkleDAG` similarly exposes fixed verified `Resolve`/`Read`
operations and reusable IPFS-compatible `ImportPath`, without exposing HTTP
routes or representing CID/link evidence as a ProofList.

Bulk local-input import is reusable through `application/add`. `add.Run` owns
option normalization, ignore and symlink policy, native layout-aware staging,
Merkle DAG import, accepted-alias selection, and unaccepted candidate
recording. The caller injects a narrow `add.Materializer` graph/fixed-list
capability and CAS; Cobra and concrete Gateway DTOs are not part of this
package. `GraphGateway`, `Gateway`, and `NewGateway` remain deprecated aliases
for one pre-release migration window.

`application/backup` composes encrypted snapshot creation with those add and
Bucket synchronization ports. `PlanStore` persists complete Branch plans and
their disjoint local bindings. `PlanService.Backup` snapshots every changed
binding before remote observation, stages before push, and returns the exact
fast-forward, merge, or conflict-branch outcome. `PlanService.Sync` then
verifies and atomically installs every binding from the final remote root.
`PlanService.RestoreTo` restores the complete encrypted manifest; it does not
accept a remote subpath selector. `RestoreBranchTo` reconstructs a Plan from
that encrypted manifest for a new device. The old single-snapshot Service,
Restore, Job, and Scheduler model has been removed rather than retained as a
second compatibility path.

The archive uses XChaCha20 without an AEAD tag because authenticated
MALT/CID commitments are the integrity layer for this profile. The direct
archive-decryption helper is package-private, so the exported restore path
accepts only untrusted transport/CAS capabilities and constructs the standard
local verifier internally; callers cannot inject a pretrusted reader or a
permissive verifier to bypass trusted-root and verified-range checks.
`FingerprintSource` provides local-only plaintext change detection, while
`History` implements durable exact-candidate push retries without importing
configuration or transport implementations.

## Verified native UnixFS

Construct a reader using narrow ports:

```go
reader, err := unixfs.NewReader(unixfs.ReaderOptions{
    Remote: remote,
    Blocks: remote,
})
result, err := reader.ReadFile(ctx, trustedRoot, "docs/readme.md")
```

For writing through any semantic mutation implementation, adapt its typed
candidate result inside the UnixFS application boundary:

```go
lists, err := unixfs.NewMutationAdapter(remote)
writer, err := unixfs.NewWriter(unixfs.WriterOptions{
    Remote: remote,
    Blocks: remote,
    Roots:  remote,
    Lists:  lists,
    Layout:  layout,
})
```

`NewGatewayMutationAdapter` and its legacy DTO port remain as a deprecated
compatibility adapter. The canonical `MutationAdapter` does not import Gateway
HTTP response DTOs.

`unixfs.NewLayout(unixfs.LayoutFlatV1)` and
`unixfs.NewLayout(unixfs.LayoutHybridV1)` return implementations of the
application-level `unixfs.Layout` interface. A nil `WriterOptions.Layout`
preserves the historical `hybrid-v1` behavior. Managed Bucket callers should
pass the Bucket's persisted layout explicitly; the layout is not encoded in a
MALT root CID and does not alter Core proof or commitment contracts.

`unixfs.NewStagedPathStatter` constructs the locally verified, lightweight
projection used when rebuilding an existing staged tree. It verifies Resolve
proofs and parent directory manifests but does not fetch retained raw file
payloads or issue List metadata reads.

The public `unixfs.MutationTransport` returns
`unixfs.CandidateRootReceipt`, not a Gateway HTTP response type. Fixed-list base
creation and mutation-result decoding live in `GatewayMutationAdapter`, not in
generic transport.

The public operations are:

- `Resolve(ctx, trustedRoot, path)`;
- `Stat(ctx, trustedRoot, path)`;
- `ReadFile(ctx, trustedRoot, path)`;
- `ReadFileRange(ctx, trustedRoot, path, offset, length)`;
- `ReadListPayloadRange(ctx, trustedListRoot, offset, length)`; and
- `EmptyDirectory`, `AddDirectory`, `AddFile`, `AddFileStream`,
  `AddFileSized`, and `RemovePath` on a writer.

Read results retain their resolve and primitive-read evidence. Raw file and
directory-manifest bytes are rehashed against authenticated CIDs. Measured-list
reads locally verify the exact list-range ProofList, every segment CID, the
resolve-to-read root transition, and the assembled byte body.

`Stat.Entries` contains the parent-authenticated `name` and `type` (`dir` or
`file`) for immediate children. `Stat.StorageKind` continues to describe the
resolved node's MALT/CAS kind for compatibility, while `Stat.PayloadKind`
describes the actual raw or measured-list payload. These fields are
independent: in particular, a manifest may project a Map-backed node as a file.
`Resolve`, `Stat`, and file reads validate every UnixFS path segment against
the relevant parent manifest and refuse to traverse through an entry projected
as a file.

`RemovePath` rematerializes immutable changed directories and verifies the new
root for internal consistency. Its result always contains `accepted: false`.
Only an explicit trust-store action or independent publication policy can
accept the candidate. The trust store binds every candidate to the accepted
base root and rejects recording or accepting it after that base becomes stale.

`AddFileSized` streams directly into chunk materialization and checks the
declared length. `AddFileStream` accepts an unknown length by spooling to the
writer's configured temporary directory, then uses the same sized path. Both
return an independently checked candidate root with `accepted: false`; they do
not update trusted-root policy.

## Trusted-root policy

Package `trust` owns three durable, disjoint states:

- `ObservedHead` is an untrusted source/dataset/branch observation;
- `CandidateRoot` is a locally computed or strictly verified update rooted at
  one accepted base; and
- `AcceptedRootState` alone is authoritative for reads.

Trust-store schema v2 persists those states separately. Opening schema v1
first retains an owner-only, exact-byte recovery artifact at
`<trust-store>.v1-recovery`, then atomically migrates its flattened accepted
root and candidates to v2. The runtime never deletes that rollback artifact
automatically. The compatibility `Record` API remains available and exposes
only aliases that already have an accepted root; observation-only aliases are
visible through `GetState` and `ListStates`. `AcceptedRoot` never falls back
to response data, an observation, or a candidate. Mutation and UnixFS writer
results remain candidates until `AcceptCandidate` is called explicitly.
Remote backup heads use `Roots.ObserveHead`; they cannot pass the candidate
acceptance route. A user may explicitly promote only a recorded observation
with `Roots.AcceptObserved` or `malt root accept-observed`.
Use `malt root state [alias]`, `Store.GetState`, or `Store.ListStates` to
inspect the structured plane without changing the compatibility shape of
`Record`. The private local API exposes the same read model at
`GET /v1/trust-states[/{alias}]` and keeps candidate and observation acceptance
on separate routes.

Transport does not import or mutate this package.

## Local cache and operation journal

Package `cache` is an additive, non-authoritative local payload cache. A
`cache.Binding` includes dataset, branch, caller-selected MALT root, revision,
payload CID, and encryption epoch. `PutVerified` independently binds bytes to
the CID, but `ReadVerified` still requires a non-nil `ProofVerifier`: every hit
recomputes the payload CID and revalidates the stored opaque proof evidence
against the exact binding before returning bytes. A wrong root, revision, or
epoch is a miss/mismatch, while a missing body, corrupt body, or rejected proof
marks the entry stale. Dirty, pending-upload, candidate, conflicted,
offline-only, stale,
and unmaterialized entries cannot pass the verified-read API. `PutLocal` may
create only dirty or offline-only state; pending/conflict changes use explicit
transitions, and neither `PutLocal` nor `PutVerified` may overwrite an existing
pending or conflict record. Persisted identities and evidence profiles must be
valid UTF-8. Blob deletion happens before its metadata reference is committed
away; failed new metadata commits remove their body immediately, and restart
reconciliation removes crash-orphaned blob/temporary files.

`cache.StateCandidate` marks a locally materialized body associated with a
verified-but-unaccepted candidate. `ReconcileLocalState` is reserved for
cross-store crash repair: under the cache lock it rereads the body, verifies
its size and exact CID, clears remote proof evidence, and permits only local
body states. It cannot create `StateVerifiedClean`.

Package `journal` is an additive ordered local-operation journal for future
filesystem write-back. It records canonical write, mkdir, rename, and unlink
intent with the locally selected base root/revision, payload CID when relevant,
encryption epoch, and immutable operation/retry identities. A request becomes
`pending_upload` before transport I/O and cannot be demoted to `offline_only`,
because an interrupted request may already have reached its destination.
`Replayable` excludes unresolved conflicts; `Unfinished` exposes them for
inspection. Conflict resolution atomically supersedes the old audit record with
a replacement intent that has new operation and retry identities. Completion
is a separate durable transition; it may retain a canonical candidate result
root but cannot accept it. Completed records and superseded ancestors remain
until their finished chain is explicitly removed with `PruneCompleted`.
Pruning retains durable tombstones for every operation and retry identity, so a
frozen idempotency key can never be rebound to new intent, including after a
restart. Journal identities and paths must be valid UTF-8, and persisted
superseded graphs must remain a single-predecessor chain with the original
conflict identity intact.

`FreezeBatchForUpload`, `MarkBatchConflicted`, and `CompleteBatch` validate the
entire requested identity set before mutating any record and commit one atomic
store replacement. Exact repeated transitions are idempotent; partial or
substituted candidate/conflict outcomes fail without reclassifying the batch.

Package `filesystem/service` is the additive, platform-neutral read-only host
filesystem boundary. A `service.View` fixes dataset, branch, caller-selected
root, revision, and encryption epoch; it must be constructed from local trust
policy rather than an observed remote head. `Stat`, `ReadDir`, `Open`,
`ReadFile`, and `ReadFileRange` consume only the verified `unixfs.Reader`
contract plus its additive `unixfs.LookupReader` capability. `Lookup` proves
the path and parent-manifest projection without fetching the file payload, so
a raw cache hit does not first perform the remote block read it is intended to
avoid. Each read revalidates the current UnixFS projection. Raw payloads may
use the non-authoritative cache, but every hit rechecks the CID, locally
reverifies its stored Resolve proof, and matches the exact view. List-backed
files remain on the authenticated range path and are not cached as if their
reconstructed bytes were one raw block.

The filesystem service is composed into Linux read-only mounts. The journal is
not yet connected to remote write-back; existing CLI content reads remain
unchanged.

Package `filesystem/staging` is the additive platform-neutral dirty overlay.
It accepts the same immutable `service.View` and a verified read-only base,
then records canonical write, mkdir, rename, and unlink operations in the
durable journal. Write bodies are raw-CID bound in the local cache before the
journal acknowledges intent. `Stat`, `ReadDir`, `Open`, and range reads provide
read-your-writes behavior; an open local handle remains pinned to the payload
CID selected at open time. Operations are isolated by dataset, branch, base
root, revision, and encryption epoch, so selecting another local accepted View
cannot expose dirty bytes from the old View.

`staging.New` opens the configured cache directory and journal path only after
acquiring process-held exclusive leases for both. A second Service sharing
either store fails rather than racing the cache-to-journal acknowledgement
window; callers must invoke `Service.Close` to release the leases. Dataset and
branch identities must already be canonical UTF-8 without surrounding
whitespace or NUL.

`staging.Service.Fsync` returns profile `malt.local-journal-fsync/v1` and
confirms only that the selected View's intent and referenced local bytes are
durable and CID-valid. It always reports remote persistence and accepted-root
promotion as false. Restart reconciliation rejects unresolved journal writes
with missing or corrupt bodies and removes unreferenced local cache bodies.
This package has no transport, candidate-root, or trust-store capability and
is not yet composed into the read-only FUSE adapter.

`staging.Service.PrepareUpload` freezes all replayable operations for one exact
View before returning bytes, preserves completed operations needed to rebuild
the full overlay from the same accepted base, deduplicates raw-CID write
payloads, and derives a stable Core-compatible operation identity from the
complete intent snapshot. `CompleteUpload`, `CompleteNoChange`, and
`MarkUploadConflicted` require the exact pending snapshot and atomically
classify the whole batch. Completion uses the non-authoritative candidate cache
state; the no-change form records the verified base as its result identity
without creating a candidate. If the journal outcome was
committed but cache reconciliation or the response failed, repeating the exact
candidate/conflict outcome repairs cache state idempotently; shrinking or
substituting the pending set is rejected. None of these methods perform network
I/O, compute a root, or accept one.

## Verified filesystem write-back orchestration

Package `application/writeback` composes the staging queue with narrow payload,
client-root remote, canonical planner, and local root-policy capabilities.
`Service.Replay` checks that the selected View still equals the locally
accepted root before freezing a batch, locally validates all available staged
bodies, loads and verifies a bounded complete update view, and normalizes the
planner's semantic intent before publishing file payloads. Only staged raw CIDs
that survive as `After` bindings in that final intent are uploaded, and every
payload store result must equal its staged CID. Intermediate writes later
overwritten or deleted by the same frozen batch are never sent to the payload
store. The MALT Core client-root Writer then computes the candidate locally and
verifies the exact durable receipt before the service records a candidate and
completes the batch.

The root-policy port deliberately exposes accepted-root lookup and candidate
recording only. `writeback.Result.RootAccepted` is therefore always false. If
the accepted root advances after the remote receipt but before candidate
recording, the exact batch is preserved as a deterministic conflict. A planner
may explicitly report that the complete batch leaves the authenticated
projection unchanged. That exact batch is completed against its verified base
without uploading payloads, submitting a mutation, recording a candidate, or
claiming remote persistence. Completion executes under the same process and
cross-process fence as every accepted-root promotion; if the root already
advanced, the batch is preserved as a conflict. A remote success, payload
upload, or receipt alone never changes the accepted root. The generic
orchestrator and concrete UnixFS planner are implemented; FUSE write composition
remains a later phase.

Package `unixfs/clientroot` is the concrete planner for flat-v1 and hybrid-v1.
It first reconstructs the UnixFS tree only from the verified complete
`mutation.UpdateView` and CID-checked manifest blocks, and rejects any mismatch
between manifest projection and authenticated Map bindings. Ordered completed
plus pending journal operations are then replayed against that immutable base.

For flat-v1 the planner emits one exact top-Map transition. For hybrid-v1 it
emits new or changed directory Maps child-before-parent and uses semantic output
references wherever an ancestor consumes a locally computed child root,
including flattened descendant bindings. New directories inherit the accepted
top root's commitment backend; existing directories retain their verified
backend and object identity. Canonical V2 manifests are stored through a narrow
block capability, and a returned CID must equal the locally computed manifest
CID. The planner supports both KZG and IPA roots and does not call a Gateway,
record a candidate, or accept a root. Platform composition remains separate.

Package `filesystem/mount` owns the next outer lifecycle boundary. A durable
`mount.Spec` binds mount ID, dataset, branch, mountpoint, local trust alias,
cache policy, read-only policy, encryption epoch, and conflict policy. The
registry persists desired state before platform mount I/O and persists a
pending-unmount tombstone before unmount I/O. `Manager.Restore` first completes
those idempotent unmounts, then recreates desired sessions using a newly
selected local accepted `service.View`. Graceful `Shutdown` stops sessions but
preserves desired state for daemon restart. A process-held registry lease
excludes a second daemon manager until `Shutdown` or process exit, and a failed
unmount tombstone cannot be revived without first completing cleanup. Registry
replacement uses same-directory rename plus directory sync on Unix and native
replace-existing/write-through semantics on Windows. The lifecycle store is
enabled only on the supported Linux, macOS, BSD, and Windows lock targets;
other build targets return `mount.ErrUnsupportedPlatform` before opening state.
Platform adapters receive only a View-bound `ReadOnlyFilesystem`; they cannot
select roots or access transport.
The private daemon API exposes the same manager at `GET/POST /v1/mounts` and
`DELETE /v1/mounts/{id}` when a manager is configured.

Package `filesystem/platform/fuse` provides the first concrete outer adapter
on Linux, pinned to `github.com/hanwen/go-fuse/v2 v2.11.0`. It maps stat,
lookup, readdir, open, and range reads onto the View-bound read-only port,
returns `EROFS` for namespace, data, xattr, and metadata mutation, refuses
nonempty or symlinked mountpoints, and sets a per-mount `malt:<mount-id>` source
identity. Crash recovery parses exact `/proc/self/mountinfo` entries without
touching a possibly disconnected final FUSE root. If mounts are stacked, it
uses `/proc/self/fdinfo` to select the currently visible mount and otherwise
fails closed. `fusermount` runs only when both the FUSE type and exact MALT
source match. Unit tests exercise syscall mapping without a kernel mount, while
the following opt-in test performs a real read-only mount when the host has
FUSE:

```sh
MALT_FUSE_SMOKE=1 go test -run TestLinuxFUSESmoke ./filesystem/platform/fuse
```

`internal/runtime.NewMountManager` composes this adapter on Linux with the
owner-private mount registry and cache, a selector that reads only the local
accepted UnixFS root, and per-dataset/branch Gateway readers. Matching remote
observations may supply cache revision metadata but never replace the accepted
root. Nonzero encryption epochs fail closed until a local mount decryption
layer exists. The daemon restores desired mounts on startup, preserves them
through graceful shutdown, and serves the manager through the private local
API. Package `localapi` supplies the reusable control client used by:

```sh
malt mount add <id> <dataset-id> <mountpoint> \
  --branch main --trust-alias <accepted-root-alias>
malt mount list
malt unmount <id>
```

The configuration paths `filesystem.mounts_path` and
`filesystem.cache_dir` are runtime-owned protected local state. A desired
mount is persisted before platform I/O, so a failed mount remains visible and
retryable until explicit `malt unmount`. No WinFsp implementation is claimed;
on non-Linux targets the daemon keeps mount routes unconfigured and continues
serving its other local-runtime APIs.

## Merkle DAG compatibility

The public transport also supports:

- `merkledag.resolve/v0alpha1` at
  `POST /v1/compat/merkledag/resolve`; and
- `merkledag.read/v0alpha1` at `POST /v1/compat/merkledag/read`.

Both wire profiles carry `segments` as an array of opaque UTF-8 coordinates.
The transport and verifier do not split or reinterpret a segment, so
coordinates such as `"."`, `".."`, `"a/b"`, `""`, and `"\u0000"` remain
valid DAG-CBOR or DAG-JSON map keys. An empty array selects the root and is distinct from
an array containing one empty-string coordinate. The profile applies only
segment-count and per-segment byte limits; textual separator policy belongs to
the calling application.

Construct `merkledag.Client` over the shared transport and use
`ResolveMerkleDAGVerified` or `ReadMerkleDAGVerified` for the safe default:

```go
compatibility, err := merkledag.New(remote)
result, err := compatibility.ResolveMerkleDAGVerified(ctx, root, segments)
```

The corresponding `VerifyMerkleDAGResolve` and `VerifyMerkleDAGRead` helpers:

1. bind traversal to the caller-selected root and segment array;
2. recompute every returned block CID;
3. reject missing, duplicate, unreachable, or unsupported evidence blocks;
4. replay dag-pb/raw UnixFS traversal and DAG-CBOR/DAG-JSON CID-link traversal locally;
   and
5. reconstruct and compare requested file bytes and range metadata.

The verifier mirrors the gateway profile limits before allocating replay state:
at most 4,096 evidence blocks, 32 MiB of raw evidence bytes, and 16 MiB of file
data per read response.

Merkle DAG evidence is intentionally not converted into a MALT ProofList.
`VerifyMerkleDAGCARRead` remains a pure local verifier for already obtained
CARv1 evidence, but public transport does not provide the evaluator-only route
that obtains such a bundle.

For compatibility tools that need to inspect blocks outside UnixFS, import
`github.com/dewebprotocol/malt-client/merkledag/ipld`. Its parser verifies
bytes against the supplied CID before decoding and exposes `ParseBlock`,
`ResolveLink`, `GetAllLinks`, and `FollowLink`; applications may register
additional bounded codecs.

## CLI output

```text
malt stat <trusted-root|alias> [path]
malt cat <trusted-root|alias> [path]
malt cat <trusted-root|alias> [path] --offset N --length N
malt rm <trusted-root|alias> <path>
malt merkledag resolve <trusted-root-cid> [path]
malt merkledag cat <trusted-root-cid> [path] [--offset N --length N]
malt bucket list
malt bucket pull
malt bucket status
malt bucket stage <candidate-root-cid> --base-commit <commit> --base-root <root> --base-revision <revision>
malt bucket push <candidate-root-cid> [-m message]
malt bucket branches
malt bucket branch <name> [--from commit-id]
```

Native `malt add` and `malt rm` first read the selected Bucket's immutable
layout, materialize with that implementation, and stage their results
automatically. An explicit `malt add --layout` that disagrees with the Bucket
is rejected before payload upload. `bucket stage` is the explicit bridge for
candidates materialized by another tool; its base values must have been
captured before that materialization, and the external tool remains responsible
for using the Bucket's persisted layout.

- `stat` writes one JSON object containing verified metadata and evidence.
- `cat` writes exact verified bytes only; diagnostics and JSON are not mixed
  into stdout.
- `--offset` and `--length` must be supplied together. Ranges past EOF are
  clipped and a zero length returns an empty body.
- `rm` writes one JSON object with `base_root`, `candidate_root`, and
  `accepted: false`. When the first argument is an alias, the candidate is
  recorded but not promoted.
- `merkledag resolve` writes the locally replayed compatibility response as
  JSON; `merkledag cat` writes exact locally replayed bytes. Both require an
  explicit CID, never consult the MALT trust store, and never claim ProofList
  verification. Their optional CLI `path` is specifically a UnixFS string-path
  adapter: it splits on `/` and rejects empty, `.`, `..`, and NUL path parts.
  Go and JSON integrations that need arbitrary IPLD coordinates should call the
  typed `segments` API directly.
