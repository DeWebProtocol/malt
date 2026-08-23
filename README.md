# MALT Local Data Runtime

MALT is a user-controlled local data runtime that runs on the user's device.
It accesses local files, maintains local trust and keys, exposes authenticated
data through local application interfaces, and communicates through replaceable
transport capabilities built on the
[MALT Core SDK](https://github.com/DeWebProtocol/malt-core).

This repository is now named `malt`. The product and binary are MALT; the
background process is the MALT daemon. Its Go module deliberately remains
`github.com/dewebprotocol/malt-client` during the initial runtime refactor so a
repository rename cannot silently reuse Core's historical module namespace.

The runtime owns concerns that must not be part of the application-neutral
authentication core:

- separate observed-head, candidate-root, and accepted-root policy;
- application-path parsing and UnixFS materialization;
- optional IPFS-compatible Merkle DAG UnixFS import;
- transport-mediated access to remote or local storage (currently Gateway
  HTTP);
- durable managed-Bucket base/remote/stash synchronization state;
- non-authoritative CID-bound cache metadata with explicit verified, dirty,
  pending, candidate, conflicted, offline-only, and stale states;
- a durable ordered local-operation journal with frozen retry identities;
- a crash-recoverable local dirty overlay with read-your-writes behavior and
  durable whole-file/offset writes and truncate plus an explicit
  local-journal-only `fsync` result; the current whole-file implementation has
  a configurable 256 MiB default staged-file ceiling pending chunked staging;
- transport-neutral verified write-back orchestration that plans before
  publication, uploads only final CID-bound staged bodies referenced by the
  normalized intent, locally computes and verifies a MALT candidate root, and
  records it without accepting it;
- flat-v1 and hybrid-v1 UnixFS client-root planning from a verified complete
  view, with locally CID-verified manifest reads and writes;
- a platform-neutral read-only filesystem service pinned to an exact accepted
  dataset view, with verified lazy List ranges and proof-revalidated raw cache
  hits;
- a crash-recoverable mount registry and daemon/local-API lifecycle contract
  that keeps platform drivers outside trust and transport code;
- daemon-managed Linux FUSE mounts selected from local accepted roots, with
  lazy verified Gateway reads, a non-authoritative local cache, and an
  explicitly selected write-back binding that durably journals locally,
  computes the candidate locally, verifies remote persistence, and never
  promotes it implicitly;
- local verification of resolve/read proofs and returned payload bytes;
- a user-owned daemon control plane over a private Unix socket or Windows
  named pipe.

The gateway is an untrusted proof producer. A successful gateway response does
not update an accepted root automatically: remote heads are observations,
while locally computed or strictly verified writes are candidates. Each has a
separate explicit acceptance path.

## Status

This is an experimental, pre-v1 local runtime. It currently provides the
`malt` CLI, a local trusted-root daemon, encrypted backup/sync/restore, a
UnixFS application adapter, a platform-neutral verified filesystem service,
and daemon-managed Linux FUSE mounts controlled by `malt mount` and
`malt unmount`. Mounts remain read-only by default. An explicit `write_back`
policy composes per-dataset/branch staging, the flat or hybrid UnixFS planner,
MALT Core client-root computation, the untrusted Gateway remote, and local
candidate recording. A successful remote write never accepts the candidate;
promotion still requires the separate local root policy. The current remote
transport is Gateway HTTP; WinFsp, local-CAS,
P2P, and hybrid transports are staged follow-up work and are not claimed as
implemented here. The historical `v0.0.1` tag was published for MALT Client
before the repository rename and before the staging/write-back APIs existed.
Those current experimental Go interfaces have not appeared in any tag and may
still make explicitly documented source-breaking changes before a tagged
release includes them; build the current runtime from a pinned commit. These
changes do not imply a Core or Gateway wire-format migration. An opt-in
write-back mount creates an additive owner-private `layout.json` beside its
per-dataset/branch journal and refuses to reuse that state with a different
flat/hybrid profile; read-only state and older runtime data remain unchanged.
The checked-in `go.mod` is the dependency source of truth; evaluation campaigns
must record the exact runtime and dependency revisions they build.

## Build

Go 1.25.7 or newer is required.

```bash
go test ./...
go vet ./...
go build -o bin/malt ./cmd/malt
```

## Quick start

Start a compatible gateway, then initialize the local runtime:

```bash
./bin/malt init
./bin/malt login
./bin/malt daemon start
./bin/malt daemon status
```

`malt init` also creates a runtime-owned `0600` backup keyring. Existing
installations can initialize only that missing keyring without replacing their
configuration:

```bash
./bin/malt backup key-init
```

`malt login` generates a dedicated Ed25519 device key locally and opens the
Gateway's browser approval page. The browser session may be established with a
Passkey. Approval registers only the public key; the Gateway never returns a
long-lived device bearer secret. Every later account request proves possession
by signing its method, host, URI, body digest, timestamp, and a durable replay
counter. Login authorizes the account only; it does not select a Bucket.

The current portable signer stores its private key in the owner-only device
credential file and identifies itself as `software-file`. The transport uses a
signer interface so TPM, Secure Enclave, and Windows CNG providers can replace
that fallback without changing the request protocol, but those hardware
providers are not implemented in this version. Passkeys remain the interactive
browser account login; they are not exported as an unattended daemon signing
key.

Create and inspect Buckets through the authenticated account:

```bash
./bin/malt bucket list
./bin/malt bucket create documents
```

The lower-level staged-root workflow remains available when `gateway.bucket`
is configured explicitly:

```bash
./bin/malt bucket pull
./bin/malt bucket status
./bin/malt add ./local-change --root <observed-head-root>
./bin/malt bucket push <candidate-root-cid> -m "update docs"
./bin/malt bucket branches
```

In Bucket mode, native `malt add` and `malt rm` capture the current base before
materialization and stage the resulting candidate in
`~/.malt-client/buckets.json`. `bucket push` refuses an unstaged candidate,
fetches the remote head without changing the recorded base, and reuses the
same push ID across retries. For candidates created by another tool, use
`bucket stage <candidate> --base-commit ... --base-root ... --base-revision ...`
with the base captured before materialization. The Gateway may fast-forward,
auto-merge independent map changes, or return a preserved conflict branch.
Bucket heads remain untrusted observations and are never promoted in
`roots.json` by this workflow.

On Linux, an already accepted UnixFS root can be exposed as a read-only mount,
or as an explicitly opt-in write-back mount with a fixed layout:

```bash
./bin/malt mount add docs <bucket-id> /mnt/docs \
  --trust-alias docs --branch main
./bin/malt mount add working <bucket-id> /mnt/working \
  --trust-alias docs --branch main \
  --write-policy write_back --layout hybrid-v1
./bin/malt mount list
./bin/malt unmount working
```

Every successful filesystem mutation is first durable in the local journal.
`fsync` then attempts the complete verified client-root write-back. Network or
conflict failures are reported to the application while the exact local batch
remains recoverable for retry. Remote success records a candidate root only;
candidate recording and journal completion are fenced against concurrent
accepted-root promotion. The mounted accepted view does not advance until an
explicit local acceptance decision and remount. The dataset/branch state also
freezes the selected layout, so changing flat-v1 to hybrid-v1 (or vice versa)
fails closed until an explicit state migration exists. The current whole-file
staging limit defaults to 256 MiB and is configurable with
`filesystem.max_staged_file_bytes`.

## Encrypted backup and restore

A backup plan is one complete Bucket branch restore unit. Bind a local
directory by Bucket name or ID; a second binding on the same branch requires
the explicit `--merge` choice, while another branch creates an independent
plan:

```bash
./bin/malt backup bind ~/Documents --bucket documents
./bin/malt backup bind ~/Pictures --bucket documents --branch photos --create-branch
./bin/malt backup bind ~/Projects --bucket documents --merge
./bin/malt backup list
./bin/malt daemon start
./bin/malt backup
./bin/malt backup documents
./bin/malt sync
```

`malt backup` snapshots every changed binding before observing a newer remote
head and then pushes all selected plans. Each binding is a separate encrypted
object under an opaque random ID. The encrypted manifest and binding layout
let the Gateway auto-merge changes to different bindings; concurrent changes
to the same binding produce a preserved conflict branch and an actionable
error. One failed plan does not prevent other selected plans from completing.

`malt sync` first performs that same local snapshot and push workflow, then
pulls the final latest branch and atomically installs every binding in the
plan. A fast-forward or Gateway merge still requires exact local acceptance of
the observed final root. In an interactive CLI, `sync` displays the exact CID
and asks before accepting the already recorded observation; the daemon records
the observation without accepting it and
returns an actionable conflict.

When the Gateway preserves a same-binding conflict, interactive `sync` asks
whether to attempt a conservative plaintext three-way merge. Independent files
and one-sided changes merge automatically. Concurrent edits to the same path
produce an owner-only conflict workspace with complete `base`, `local`,
`remote`, and editable `merged` trees for each binding. The workspace is
separate from every binding, so filenames and suffixes are never rewritten:

```bash
./bin/malt conflict list
# edit the reported .../<binding-id>/merged trees
./bin/malt conflict resolve documents --manual
# or choose --keep-local / --keep-remote
```

There is no `bucket/path` restore selector. `malt restore <plan> <destination>`
restores the entire branch plan under its manifest archive names. On a new
device, the encrypted manifest can reconstruct the Plan without a local plan
record:

```bash
./bin/malt restore documents ./restored-documents
./bin/malt restore --bucket documents --branch main ./restored-documents
```

Original file and directory names, binding display names, and Plan display name
exist only inside encrypted archives. The Gateway necessarily sees the account
and Bucket display name, Bucket ID, selected branch, ACL and membership
metadata, fixed application prefixes, opaque binding IDs, ciphertext sizes,
timing, and access patterns. It cannot read archive plaintext without the local
keyring.

The initial archive profile preserves regular files, directories, permission
bits, modification times, and safe relative symlinks. It does not yet preserve
xattrs, ACLs, hard-link identity, sparse-file layout, or device nodes.

This encryption profile applies to `malt backup`; the general-purpose
`malt add` command retains its existing plaintext UnixFS semantics.

The archive intentionally has no AEAD authentication tag. Restore first
verifies the caller-selected MALT root and path ProofList, then verifies every
returned ciphertext block against its authenticated CID, and only then
decrypts. The CID is calculated normally over the encrypted bytes. Archive
decoding failure under a wrong key or epoch is an operational validity check,
not a replacement for MALT/CID integrity.

`malt backup --foreground [plan...]` and `malt sync --foreground [plan...]`
are embedded bypasses when the daemon is
not running. Daemon and foreground operations share a cross-process lock, so
they cannot race against the same plan workspace. Automatic plans are
configured without a GUI and reloaded by the daemon without restart:

```bash
./bin/malt backup schedule set documents --every 6h
./bin/malt backup schedule list
./bin/malt backup schedule remove documents
```

Automatic plans compute local plaintext SHA-256 tree fingerprints and skip
unchanged bindings. Fingerprints and source paths stay in local owner-only
state. The history journals the exact candidate, original base, and frozen
push identity so a lost response is retried without encrypting another
snapshot.

The keyring keeps one random master key per epoch and derives per-Bucket keys
in memory, so many Buckets do not require many persisted keys. Export an
XChaCha20-Poly1305 recovery bundle protected by scrypt and a strong passphrase,
store it separately from the remote ciphertext, and import it before a
cross-device branch restore:

```bash
./bin/malt recovery export ./malt-recovery.json
./bin/malt recovery import ./malt-recovery.json
```

Use `--passphrase-file` for unattended recovery tooling; passphrases are never
accepted as command-line arguments. A recovery bundle contains all retained
epochs and therefore grants decryption of every Bucket owned by this keyring.
Import adds missing epochs to an existing keyring only when every overlapping
epoch contains byte-identical key material; it never replaces conflicting
keys. This permits another device to import later rotations safely. Losing both
the keyring and recovery bundle makes the encrypted backups unrecoverable.
`malt backup key-rotate`
activates a new epoch for future snapshots while retaining old restore keys; it
does not re-encrypt existing archives. The Gateway never receives these keys.

This version supports one account across multiple authorized devices, but does
not implement cryptographic sharing of a Bucket with another user. A Gateway
ACL grant alone does not grant decryption, and proxy re-encryption, per-member
key envelopes, and shared-Bucket revocation are intentionally out of scope.

Runtime state and portable credential-provider files use owner-only Unix permissions or a protected
owner-and-SYSTEM Windows DACL. Custom config, keyring, workspace, trust, and
Plan-history paths must still live in an owner-only directory: protecting a
file cannot prevent another principal with parent-directory replacement rights
from deleting and replacing it. Do not place these paths in a shared
directory.

If the configured staging directory is inside the selected backup source, the
runtime uses the system temporary directory instead. It rejects the operation
when no staging root exists outside the source, and the archive writer
independently refuses to archive its own output, including through a symlinked
staging path. Backup sources containing the configured MALT keyring, config,
workspace, trust store, backup history, or daemon endpoint are rejected rather
than silently omitted or self-encrypted. Keep device enrollment/recovery
copies through the separate secure channel described above.

The old single-directory `backup.jobs` model and its history schema are not
migrated. This Plan-only implementation rejects that configuration explicitly
instead of silently preserving two backup models.

The daemon exposes HTTP semantics over a private user-owned Unix socket or
Windows named pipe. It does not open a loopback TCP configuration website. A
future Web or native GUI should be a thin client of that private management
API; backup, scheduling, trust, and key policy remain reusable application
services rather than UI logic.

Trust an independently obtained root, resolve a UnixFS path, and add a local
tree:

```bash
./bin/malt root trust my-data <root-cid>
./bin/malt resolve my-data docs/readme.txt
./bin/malt add ./my-data --alias my-data
./bin/malt root list
./bin/malt root state my-data
./bin/malt root accept my-data <candidate-root-cid>

# A remote head must already be recorded as an observation.
./bin/malt root accept-observed my-data <observed-root-cid>
```

Read verified native UnixFS content and materialize a removal candidate:

```bash
./bin/malt stat my-data docs/readme.txt
./bin/malt cat my-data docs/readme.txt
./bin/malt cat my-data media/video.bin --offset 1048576 --length 262144 > part.bin
./bin/malt rm my-data docs/obsolete.txt
./bin/malt root accept my-data <candidate-root-from-rm>
```

`stat` emits JSON including locally verified resolve/read evidence. `cat`
writes only verified file bytes to stdout. `rm` never changes the accepted root:
it emits `accepted: false` and, when given an alias, records the result as a
candidate for a later explicit `root accept` command.

The native MALT target exposes two versioned UnixFS application layouts:

- `flat-v1` stores the root manifest, every directory manifest, and every file
  target as root-relative bindings in one authenticated MALT Map. A write
  updates one semantic Map object even when several ancestor manifests change.
- `hybrid-v1` keeps one authenticated Map per directory and also retains
  descendant root-relative bindings in ancestor Maps.

`malt add` retains `hybrid` as its compatibility default outside managed
Bucket mode and accepts it as an alias of `hybrid-v1`. When a Bucket is
selected, `malt add` and `malt rm` fetch and strictly validate its persisted
layout before materialization; an explicit `--layout` must match that fixed
value. Select the flat implementation explicitly for non-Bucket roots with
`--layout flat-v1`. The removed bare `flat` and `hierarchical` pre-release
aliases remain invalid. Flat bulk add rejects followed directory symlinks
before uploading any blocks because an opaque nested Map root would violate
the single-Map layout invariant.

UnixFS file/directory projection is authenticated by each parent directory's
typed V2 manifest rather than inferred from a child's MALT semantic kind. A
Map-backed object can therefore be a UnixFS file with an `@payload` arc and
other application arcs such as `@comments`; UnixFS path traversal still stops
at that file. Historical V1 manifests remain readable with their locked
Map-to-directory and List/CAS-to-file fallback. See
[the UnixFS manifest format](./docs/unixfs-manifest.md) for the wire and
compatibility rules.

The same runtime can materialize one local file or directory as an
IPFS-compatible Merkle DAG while reusing the gateway CAS:

```bash
./bin/malt add --target merkle-dag \
  --file-layout balanced --dir-layout adaptive ./my-data
```

This returns a Merkle DAG root CID. It does not create a MALT root, ProofList,
or trusted-root candidate, so `--root` and `--alias` are intentionally rejected
for this target.

The public Go API is importable as:

```go
import (
    "github.com/dewebprotocol/malt-client/application"
	"github.com/dewebprotocol/malt-client/bucketsync"
    "github.com/dewebprotocol/malt-client/merkledag"
    "github.com/dewebprotocol/malt-client/transport"
    "github.com/dewebprotocol/malt-client/trust"
    "github.com/dewebprotocol/malt-client/unixfs"
)
```

Package `application` is the reusable use-case layer used by the CLI and local
daemon. It selects explicit or locally accepted roots, composes verified UnixFS
and Merkle DAG reads, records writer results as candidates, and exposes
candidate promotion only as an explicit call. Its `application/add` package
owns the CLI-independent ignore, symlink, staging, layout selection, and Merkle
DAG import workflow used by `malt add`. Package `unixfs` defines the
application-level `Layout` interface and the stable `flat-v1` and
`hybrid-v1` identifiers. Layout selection does not change MALT Core codecs,
proofs, commitments, or canonical graph semantics.

Explicit CIDs are selected without opening `roots.json`; the trust store is
required only for an alias. A missing, corrupt, or unwritable alias store
therefore cannot block an otherwise valid explicit-CID operation.
Explicitly typed alias inputs such as `malt add --alias` always perform alias
lookup, even when the alias text happens to be CID-shaped.

Package `transport` is an untrusted gateway transport. Package `trust` owns
separate observed/candidate/accepted root policy. Package `unixfs`
composes it into verified `Resolve`, `Stat`, `ReadFile`, `ReadFileRange`,
`ReadListPayloadRange`, `EmptyDirectory`, `AddDirectory`, `AddFile`, streaming
file writes, and `RemovePath` operations. The UnixFS facade requires
a caller-selected root, verifies ProofLists locally, enforces resolve-to-read
continuity, and verifies raw, manifest, and measured-list payload bytes.

With `transport.Options{TenantBearerToken: ..., BucketID: ...}`, native
MALT/CAS calls use the authenticated Bucket routes. Package `bucketsync`
persists base, observed remote head, and local stashes under a cross-process
lock and implements stash-before-fetch push ordering. It deliberately does not
import or mutate package `trust`.

Single-value CAS `Get`/`Has` require that authenticated Bucket selection. The
transport does not attempt the Gateway's removed public raw-CAS GET/HEAD route;
unscoped calls fail locally without sending a request.

Package `merkledag` owns the gateway's distinct compatibility profiles over a
narrow fixed-route profile transport. `ResolveMerkleDAGVerified` and
`ReadMerkleDAGVerified` recompute every
evidence block CID and replay the UnixFS link traversal locally. These results
are never represented as MALT ProofLists.

The transport exposes only fixed Merkle DAG resolve/read route capabilities;
applications cannot supply an arbitrary Gateway route and JSON body. When a
Bucket is configured, these calls use its authenticated compatibility routes
and cannot fall back to the public CAS namespace.

## Evaluation adapters

`cmd/` contains only the supported `malt` product binary. Runtime-specific
benchmark process adapters live under `tools/evaluation/cmd`, with shared
private support under `internal/evaluation`. They preserve their external
`malt-eval-*` executable and wire contracts, but are not a public Go API or
supported user CLI.

Evaluation instance authentication, bootstrap control, unchecked raw-CAS
fetches, and selective-CAR transport are deliberately confined to that private
boundary. The public `transport` package cannot invoke them. Benchmark suites,
campaign plans, comparison policy, result schemas, and provenance remain owned
by the separate
[malt-evaluation](https://github.com/DeWebProtocol/malt-evaluation) repository.
See [tools/evaluation/README.md](./tools/evaluation/README.md) for the adapter
inventory and build boundary.

The transport exposes bounded ordered CAS `PutBatch`/`HasBatch` and a
typed diagnostic metrics snapshot. Package `merkledag/ipld` restores the
generic CID-bound raw, DAG-PB, DAG-CBOR, DAG-JSON, and legacy JSON parser/link
toolkit for runtime-side compatibility code.

The CLI exposes the same fail-closed read path without consulting the MALT root
store:

```bash
./bin/malt merkledag resolve <merkle-dag-root-cid> docs/readme.txt
./bin/malt merkledag cat <merkle-dag-root-cid> docs/readme.txt
```

Run `malt <command> --help` for the exact flags and output contract.

Default state lives under `~/.malt-client/`. The generated configuration points
to `http://127.0.0.1:8080`; edit `gateway.base_url` to select another gateway.
Tenant bearer credentials and device proof-of-possession authentication require
HTTPS except for loopback development.

## Trust model

1. The caller chooses an already trusted root.
2. The runtime sends canonical segments to the gateway.
3. The gateway returns the result and ProofList.
4. The runtime verifies the proof against the caller-selected root using MALT
   core.
5. For payload reads, the runtime also verifies returned bytes against the
   authenticated CID.
6. A mutation's new root remains a candidate until explicit local acceptance
   or an independent publication mechanism establishes trust.

See [ARCHITECTURE.md](./ARCHITECTURE.md) for repository boundaries.
See [docs/go-api.md](./docs/go-api.md) for the public API and CLI contracts.
The [v0.0.5 migration matrix](./docs/v0.0.5-parity.md) records which former
core application capabilities moved here and which were deliberately re-homed.

## License

MIT. See [LICENSE](./LICENSE).
