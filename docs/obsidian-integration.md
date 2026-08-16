# Obsidian Integration Design

Status: proposed design for an initial desktop implementation. The new v0
content-policy, pairing, and naming choices require maintainer review; the IPC
details remain a versioned draft until the first daemon and plugin conformance
tests pass.

This document defines the product and ownership boundary for integrating an
Obsidian Vault with MALT. It does not define a MALT protocol, proof, CID, or
wire-format change. The private local API is specified separately in
[Workspace Adapter IPC v0](./workspace-adapter-ipc-v0.md).

## Decisions

The initial integration uses this process boundary:

```text
Obsidian desktop plugin
        |
        | private workspace-adapter IPC
        v
malt-client daemon
        |
        | authenticated Gateway transport
        v
managed Gateway
```

The established boundaries and proposed remaining choices for the desktop
alpha are:

- The plugin identifies the open Vault, reports file events as dirty hints,
  presents synchronization and conflict state, and applies daemon-prepared
  changes with Obsidian APIs.
- The daemon owns complete scans, plaintext content fingerprints, encryption
  and keys, accepted/candidate roots, Bucket synchronization, local
  ProofList/CID verification, three-way merge, journals, and crash recovery.
- The daemon never loads plugin code. The plugin does not embed or reimplement
  the MALT client, Gateway transport, encryption profile, trust store, or merge
  engine.
- A file event is never synchronization truth. The daemon performs a complete
  policy-filtered scan before publishing local state and after every inbound
  apply.
- Verified remote content is decrypted into owner-only staging outside the
  Vault. The daemon does not rename or replace the bound Vault directory. The
  plugin applies an immutable operation set through Obsidian APIs, and the
  daemon advances the local baseline only after a confirming scan.
- The alpha is desktop-only. The daemon adds a dedicated user-owned
  workspace-adapter Unix socket on Unix-like systems and a dedicated
  owner-and-SYSTEM Windows named pipe. Its mux contains no general CLI control
  routes. It does not open loopback TCP or a browser-accessible local HTTP
  port.
- The plugin repository will be `DeWebProtocol/malt-obsidian`, with manifest ID
  `malt-sync` and display name `MALT Sync`, subject only to repository and
  Community-directory reservation at creation time. The display name avoids
  putting `Obsidian` in the plugin name.
- Mobile is outside the v0 contract. A WASM client and a native bridge remain
  alternatives to evaluate after the desktop state machine and application
  semantics stabilize.

## Current implementation boundary

The existing encrypted Backup Plan implementation provides most of the trusted
engine:

- `application/backup.Plan` and `Binding` identify a Bucket branch and local
  directories;
- `PlanService.Backup` freezes, fingerprints, encrypts, and rechecks changed
  local bindings before any remote observation; an initialized workspace then
  materializes and pushes its candidate against the known base, while an
  uninitialized workspace must first pull the base after preserving that local
  snapshot;
- `PlanService.SyncWithOptions` preserves the local content before pulling and
  owns accepted-root and three-way-merge policy;
- verified restore fetches authenticated ciphertext, verifies its CIDs, and
  decrypts it to client-owned staging; and
- installation journals recover interrupted filesystem replacement.

The final item cannot be reused for a live Obsidian Vault. Today,
`installBindings` prepares a complete tree and `installPrepared` swaps each
Binding directory with filesystem renames. The workspace-adapter path must
split synchronization into two independently journaled phases:

1. prepare an authenticated plaintext target and immutable apply session; and
2. let the bound adapter apply that session, then confirm the resulting tree
   with a daemon scan.

The general Backup Plan path remains valid for ordinary directories. An
adapter-backed Binding selects the new installation strategy; it does not fork
backup, trust, encryption, Bucket, or merge semantics.

For v0, one adapter workspace owns a dedicated Plan containing exactly one
adapter-backed Binding. Pairing MUST NOT attach a Vault to a mixed or
multi-Binding Plan. This restriction avoids a false cross-Binding transaction
in which an ordinary directory can be atomically installed but a live Vault
must wait for plugin acknowledgement and a confirming scan. A future contract
may coordinate multiple installers explicitly; it must not imply atomicity
that Obsidian cannot provide.

The adapter registry is also a publication and installation guard. Generic
`malt backup`, scheduled backup, generic `malt sync`, foreground sync,
keep-remote, and manual-conflict paths MUST refuse an adapter-backed Plan. Only
the workspace service may snapshot or publish it, after an authenticated live
plugin request supplies the current approved `Vault.configDir`. Filesystem
installation remains forbidden. The rejected commands return an actionable
instruction to connect the adapter; no alternate CLI or timer path may bypass
that guard.

The current `mergePlaintextTrees` also compares directory entries and
permission modes. It cannot be called unchanged for the adapter's file-only
logical tree. Phase 1 must factor merge over a policy-aware canonical entry
projection: generic Backup Plans retain current filesystem semantics, while
the Obsidian projection compares included relative file paths and exact bytes
only. This remains one daemon-owned merge engine with profile-specific input,
not a TypeScript reimplementation.

The encrypted Backup Plan manifest is a client-owned application schema, not a
MALT wire or Gateway schema. Its current version 1 identifies only a generic
Plan and its Bindings, so it cannot prove that another device created an
Obsidian adapter Plan or selected the same content and installer policy. Phase
1 therefore introduces manifest version 2 with this required marker:

```json
{
  "version": 2,
  "workspace_adapter": {
    "kind": "obsidian",
    "contract": "workspace-adapter/0",
    "content_policy": "obsidian-visible-v0",
    "installer": "vault-api"
  }
}
```

The remaining Plan and single Binding identity fields stay authenticated in
the same encrypted manifest. Adapter enrollment accepts only a version 2
manifest with this exact marker and exactly one Binding. It rejects version 1,
an absent or unknown marker, and a multi-Binding Plan. Existing clients already
reject an unknown manifest version; updated generic restore/sync code must also
refuse the `vault-api` installer and direct the user to the workspace adapter.
There is no implicit legacy-Plan migration in v0. Moving an existing generic
Backup Plan requires a later explicit migration after every participating
device understands the marker; the alpha should use a dedicated branch.

Adapter enrollment cannot safely ship in the same compatibility step that
introduces its guard. A transition release first makes the coordinator lease
mandatory for every Plan operation/writer while keeping adapter enrollment
hard-disabled. Its offline enable command MUST first acquire and continuously
hold the existing v1-compatible exclusive `plansPath + ".lock"` before it
examines processes or store state. It also prevents daemon restart, proves
that no pre-transition `malt` daemon or direct CLI process for this client root
already loaded v1 and remains in flight, and drains all coordinated
operations. A newly started old CLI cannot load v1: it blocks or times out on
that lock, and any retry after release opens v2 and fails closed. If the
platform cannot prove quiescence while the lock remains held, the command
releases it without migration and enrollment stays disabled.

Only after that barrier does the transition release atomically migrate the
entire local Plan store from `planStoreVersion` 1 to 2 and record the minimum
coordinator-capable client epoch. Every v2 Plan has an explicit installer
descriptor; v1 legacy Plans become `filesystem`, and no adapter Plan exists
until the v2 file and enable marker are durable. The command holds the same
exclusive compatibility lock through the durable v2 rename, directory sync,
enable-marker recovery point, and final process recheck. The current v1 reader
rejects an unknown top-level store version, so a newly started old binary fails
closed after lock release. No adapter descriptor is ever written into a v1
store. A subsequent adapter-enabled release requires that migration marker
before enrollment. This two-release quiet barrier prevents both an old process
that already loaded v1 and an old CLI starting between process census and
rename from overlapping activation; deliberate same-OS-user bypass remains
outside the stated isolation boundary just like direct Vault writes.

The separate adapter registry supplies path, capability, and live workspace
state, but is not the only evidence that a Plan is adapter-backed. A
descriptor/registry/manifest mismatch, missing registry state for an adapter
Plan, or registry recovery failure blocks publication and installation rather
than defaulting to the generic path.

## Ownership

### Plugin

The plugin owns only Obsidian-facing behavior:

- wait for `workspace.onLayoutReady` before treating Vault events as hints;
- obtain the desktop Vault base path after checking that the adapter is a
  `FileSystemAdapter`, and read the active configuration directory from
  `Vault.configDir` rather than assuming `.obsidian`;
- register `create`, `modify`, `delete`, and `rename` listeners and coalesce
  them into dirty notifications;
- request a status-only full daemon scan at the negotiated live-plugin
  reconciliation cadence even when no event arrives;
- render disconnected/stale, dirty, syncing, awaiting-acceptance, apply-ready,
  conflict, and error states;
- require a visible user action for root acceptance and destructive conflict
  choices;
- fetch immutable staged objects, verify their transport digest, check local
  per-file preconditions, and use `Vault`/`FileManager` APIs to create, modify,
  or trash Vault entries; and
- acknowledge what it attempted. An acknowledgement is not a successful scan
  or a trusted synchronization result.

The small SHA-256 check used as an apply precondition is a local compare-and-
swap guard. It is not the workspace fingerprint, MALT verification, or a
second synchronization engine.

### Daemon

The daemon owns all persistent and trusted behavior:

- canonicalize and validate the Vault path during pairing;
- bind one local workspace identity to one exact source path and
  Bucket/branch target, then resolve its Plan/Binding identity during
  enrollment;
- store capability hashes, pairing state, operation journals, staged content,
  scan generations, and adapter state in owner-only client storage;
- enforce the fixed v0 include/exclude policy before scanning, archiving,
  diffing, merging, or preparing operations;
- serialize scan, backup, pull, merge, prepare, apply confirmation, and
  conflict resolution per workspace;
- reject every generic filesystem installer entry point for an adapter-backed
  Plan, including foreground and conflict-resolution paths;
- preserve local changes before observing/pulling remote changes;
- expose only opaque staged object IDs, never an arbitrary staging filesystem
  path;
- reject an apply if the current full scan no longer matches its precondition;
  and
- rescan after the plugin completes or crashes, advancing history only when
  the scanned target exactly matches the prepared result.

### Gateway

No Obsidian-specific endpoint is required at the Gateway. It continues to own
authenticated accounts, Bucket branches, authorization, persistence, root
publication, and server-side independent-change merge. It receives ciphertext
and never receives Vault plaintext, archive keys, recovery keys, local source
paths, apply sessions, or Obsidian installation data.

Creating a brand-new adapter identity does require a generic Bucket branch
create-if-absent operation with a durable idempotency receipt and an exact
head-absent generation. The current explicit-branch API is not assumed to
provide that lost-response receipt. If it cannot, Phase 1 must add the generic
Bucket concurrency primitive in `gateway`; it is not an Obsidian route. Until
that primitive is available, v0 may import an existing marked manifest but
must refuse new Plan/Binding identity creation.

## Workspace identity and records

A Vault name is a display label, not an identity. A Bucket ID is remote
identity, not local-clone identity. The daemon creates a random `workspace_id`
for every approved local binding and stores a separate workspace-adapter
record containing:

- adapter kind and contract version (`obsidian`, `workspace-adapter/0`);
- the exact Bucket/branch target and, after enrollment, Plan ID and Binding ID;
- the canonical absolute source path approved by the user;
- the plugin installation ID and non-secret Vault display name;
- the approved single-component `Vault.configDir` value;
- the v0 ignore-policy version;
- capability-token hashes and revocation state; and
- the last confirmed scan/apply generations.

This record is local-only. The encrypted version 2 workspace-adapter Plan
manifest is the cross-device source for Plan, Binding, adapter, contract,
content-policy, and installer identity. Copying a Vault directory or its
plugin data to a second path does not copy authorization: the new path must
pair as a new local workspace.

The workspace-adapter registry must be separate from plugin `data.json` and
from the remote manifest. It cross-checks the installer descriptor persisted
in the Plan store, but must not make the plugin token a property of the Plan or
Bucket.

Installer selection is a required application-service dependency, not a CLI
handler check. Every `PlanService` publication/install operation and every
Plan-store writer acquires a guard generation from the same cross-process
coordinator used by enrollment and holds it through its complete transaction.
Generic bind rejects an adapter Plan by Plan ID or Bucket/branch target even
when its new source path does not overlap; generic import, schedule mutation,
and every future writer also resolve policy and reject adapter changes. Only
the enrollment journal may create/import the single Binding and jointly
activate the Plan descriptor plus registry generation.

Enrollment takes the exclusive side of that guard. A scheduler that loaded a
Plan earlier must reacquire and re-resolve the current policy before work; it
cannot continue from a stale in-memory Plan. The daemon does not start Plan
handlers, writers, or schedulers until Plan-store, registry, and journal
recovery agree, and any missing/corrupt/inconsistent policy state fails closed.

Creating the provisional record atomically reserves the canonical Vault path
under the same cross-process coordination used for Plan bindings and restore
validation. `malt backup bind`, generic restore, another approval, and every
foreground bypass MUST reject a source or destination that overlaps either a
provisional or initialized adapter workspace. Enrollment journals the
Plan-store import and adapter-record activation as one recoverable transition;
a crash cannot leave the path writable through both installer types.

An approved workspace may initially be `provisional`: it is bound to the
canonical Vault path plus exact Bucket/branch, but has no Plan or Binding ID
yet. This is required when another device may already have published the
encrypted Plan manifest. The daemon cannot safely invent a new Binding ID or
read that manifest before the observed root is explicitly accepted.

The first sync freezes a complete local scan before observing the branch. For
an existing branch it records the observed root as a candidate and waits for
exact user acceptance before verifying/decrypting the remote manifest. Only a
single-Binding version 2 manifest with the exact workspace-adapter marker
initializes the local Plan with its existing Plan/Binding IDs and the approved
Vault path. New IDs require an exact journaled create-if-absent branch receipt
obtained by explicit CLI approval; no observed/missing head is treated as
empty. A version 1 generic Plan, an existing branch with no valid adapter
manifest, and a remote multi-Binding manifest are unsupported in v0 and leave
the provisional workspace unchanged.
If both first-join trees are non-empty, the conservative merge uses the empty
logical tree as their enrollment base: distinct additions merge, byte-identical
additions at the same path converge, and differing additions at the same path
conflict. Existing-branch enrollment MUST fetch and verify/decrypt the remote
Binding tree and complete that empty-base merge before publishing anything:
conflicts publish no candidate, while a clean result publishes only the merged
candidate with the exact accepted remote root as its CAS base. A branch race
returns to observation and merge; it never falls back to the raw local tree.
Only a newly created branch with the exact create-if-absent receipt may publish
a local-only first candidate directly. Enrollment durably preserves the local
scan first and never installs remote content outside the adapter apply flow.

## Desktop v0 content policy

The first release synchronizes ordinary visible Vault files, including
Markdown, Canvas files, attachments, and arbitrary regular-file bytes. Paths
use normalized Vault-relative `/` separators. Absolute paths, `.`/`..`
segments, NUL, and platform-colliding names are rejected before an apply
session is exposed. v0 also rejects every raw segment that is not already
byte-for-byte Unicode NFC; it does not normalize a lone NFD disk/Obsidian path
into a possibly different Vault API lookup.

The adapter's logical tree commits relative file path, file kind, byte length,
and content digest. File mtime, permission mode, xattrs, ACLs, and empty
directories are not logical entries because Obsidian's Vault API cannot
portably reproduce them. The daemon may use mtime/size only as a scan cache
hint. Parent directories are derived from included file paths and created when
needed; v0 never trashes a folder. This also prevents a supposedly empty
visible `TFolder` from moving excluded hidden local content into trash.

The policy is deliberately fixed rather than locally configurable in v0 so
that two devices cannot silently scan different logical trees:

- exclude every path with a component beginning with `.`; this includes
  `.obsidian`, `.trash`, `.git`, `.malt`, plugin data, and macOS metadata;
- exclude Windows metadata names `Thumbs.db` and `desktop.ini`
  case-insensitively at every depth; and
- reject a non-excluded symlink or other special filesystem object as an
  actionable unsupported-content error rather than silently omitting it.

Obsidian permits a custom configuration directory, so the plugin reports the
actual `Vault.configDir` at pairing. v0 accepts it only when it is one portable
single path component beginning with `.`, records it, displays it during CLI
approval, and requires the plugin to recheck the exact value before every sync
and apply. A non-dot, nested, changed, or invalid configuration directory
blocks the workspace and requires re-pairing; the daemon never turns a
device-local custom path into a dynamic ignore rule. The fixed dot-component
rule therefore excludes both the usual `.obsidian` and an accepted custom
configuration directory identically on every device.

Excluding the active configuration directory is a v0 product decision, not a
permanent claim that settings should never synchronize. Obsidian's Vault API
exposes visible Vault content while hidden folders require lower-level adapter
access. Synchronizing plugin binaries, plugin secrets, device-local workspace
layout, caches, and the MALT capability itself would also create bootstrapping
and credential loops. A later settings profile must define an explicit
allowlist and safe Obsidian API for each setting class; it must not enable an
entire configuration directory.

Soft deletion uses the user's Obsidian trash preference through
`FileManager.trashFile`. Because dot-prefixed trash directories are excluded,
a deleted live path becomes logical absence without publishing the local trash
copy. No v0 apply calls `Vault.delete` for permanent deletion.

## Pairing and least authority

Socket ownership is the first security boundary, but it does not justify
giving a plugin the daemon's general root, backup, key, or lifecycle API. The
workspace adapter uses a separate endpoint, route mux, and scoped capability.
The current CLI control socket remains separate. Unix uses an owner-directory
platform path. Windows uses an unguessable endpoint descriptor generated into
owner-only daemon state; the user copies it from `malt workspace endpoint`
into SecretStorage before pairing so another principal cannot squat a public
pipe name.

Pairing is user-mediated:

1. The plugin generates the random 256-bit workspace capability, stores it
   immediately as pending in Obsidian `SecretStorage`, and sends only its
   SHA-256 digest with the installation ID, Vault name, desktop base path, and
   exact `Vault.configDir` value over the private endpoint.
2. The daemon returns a short-lived pairing request and a human approval code.
   The plugin shows the exact CLI approval command.
3. `malt workspace approve <code> --bucket <selector> [--branch <name>]`
   displays the canonical Vault path, content policy, Bucket, branch, and
   requested scopes. It requires explicit terminal confirmation before
   creating a provisional workspace record. The first sync imports an existing
   marked version 2 single-Binding adapter manifest. New Plan/Binding IDs are
   permitted only when approval explicitly requested `--create-branch` and
   journaled the exact generic branch create-if-absent CAS receipt.
4. The plugin polls the opaque pairing resource. Approval atomically activates
   the already-committed capability digest and returns the workspace ID and
   scopes, never the secret.
5. The plugin promotes its pending SecretStorage entry to active. It never puts
   the capability in plugin `data.json`; the daemon persists only its digest.

Pairings expire, are single-use, and are rate-limited. Re-pairing rotates the
capability. `malt workspace revoke <workspace>` immediately revokes all plugin
capabilities without deleting the Plan, local Vault, remote data, or accepted
root.

The capability is bound to one `workspace_id` and permits only status/event
reads, dirty hints, sync requests, exact candidate acceptance, conflict
choices, staged-object reads, and apply acknowledgements for that workspace.
It cannot select another source path, Bucket, branch, Plan, Binding, root, key,
or Gateway account.

This capability boundary prevents accidental overreach and confused-deputy
bugs. It does not claim to isolate the daemon from fully malicious code already
running as the same OS user and able to inspect that user's processes or
storage or deliberately dial the separate CLI control endpoint.

## Synchronization state machine

Only one top-level owning mutation may be active per workspace. Candidate
acceptance, conflict resolution, apply begin/ack/complete, and the confirming
scan are continuations of that operation while it is waiting, not competing
mutations. Dirty hints, reads, and status polls remain available.

```text
provisional
  -> enrolling
  -> scanning-local
  -> backing-up/pushing
  -> observing-remote
  -> awaiting-root-acceptance | merging | preparing-apply | conflict
  -> apply-ready
  -> applying
  -> confirming-scan
  -> idle | stale | dirty | conflict | blocked
```

### Local changes

Vault events are coalesced and sent as hints. Plugin startup, resume, overflow
recovery, every explicit Sync, and a negotiated live-plugin cadence request a
complete daemon scan while supplying the current approved `Vault.configDir`.
The cadence scan updates status only and never publishes. A silently lost event
is therefore discovered within the bounded interval; if the plugin is absent
past the declared threshold, the daemon reports `stale` rather than presenting
an indefinitely old scan as clean. v0 does not run offline periodic or
scheduled publication for an adapter Plan, avoiding a scan/publish based on a
stale configuration-directory assertion.

Every authoritative complete policy-filtered scan copies ordinary file bytes
into an owner-only immutable plaintext snapshot and derives the logical
fingerprint from those exact staged bytes. After the snapshot is complete, a
fresh full live scan must equal its fingerprint; otherwise the daemon
discards/retries the attempt and leaves the workspace dirty. Status-only scans
then discard their snapshot. Candidate encryption and archive creation consume
the same snapshot, never fresh reads from the live Vault. Metadata-before/
after checks are only optimizations and cannot establish stability: an A to B
to A edit or a partial read must not publish bytes that are inconsistent with
the staged fingerprint.

The daemon then stages/pushes the local candidate before fetching the current
remote branch, preserving the current Backup Plan concurrency invariant.

Enrollment is the only exception to pushing before remote observation because
the accepted encrypted manifest owns the Binding ID needed for publication.
The daemon still freezes and journals the complete local tree first. Once
identity is resolved, every ordinary sync returns to local-candidate-first
ordering.

### Remote changes

After a remote root is explicitly accepted under existing trust policy, the
daemon verifies the MALT path proof and every ciphertext CID, decrypts into an
owner-only staging directory outside the Vault, and computes the target tree.
It performs the existing three-way merge before creating an apply session.

The logical merge is not enough to declare an apply ready. During preparation
the daemon also records a non-content physical namespace projection. A target
file whose local path is a directory, a required parent path that is a file,
or another file/directory type obstruction moves the workspace to `blocked`
with bounded affected paths and `physical_namespace_obstruction`; it publishes
no apply session. In particular, an empty local folder `foo` blocks a remote
file `foo` even though empty folders are absent from the logical tree. The
daemon never deletes or coerces the obstruction. The user resolves it through
Obsidian or the filesystem and requests a new full scan, preventing an
unchanged logical fingerprint from generating the same impossible session in
a loop.

An apply session is immutable and contains:

- the workspace and scan generations it depends on;
- accepted/base/local/remote/target root metadata needed for status;
- ordered `mkdir`, `write_file`, and `trash_file` operations using
  Vault-relative paths;
- the expected local kind, byte length, and SHA-256 guard for each affected
  file;
- opaque staged object IDs, lengths, and SHA-256 transport digests; and
- an expiry and journal generation.

The plugin never receives a daemon staging path. It downloads content by
opaque object ID, verifies the digest, rechecks the current Vault entry, and
applies operations through Obsidian APIs. Directory creation is
shallowest-first, followed by writes and file trashing. Directories left empty
after file trashing are harmless non-entries in the adapter tree. Renames are
represented as a write followed by a trash because the remote snapshot
contains path state, not authenticated rename provenance.

The plugin forwards events observed during apply. It may label events that
match the active operation for UI de-noising, but neither side discards them as
proof of self-origin. The confirming daemon scan resolves whether the target
was actually installed.

### Confirmation and races

The plugin's completion call starts a new daemon scan; it does not commit the
apply. The daemon records the new baseline only if the complete filtered tree
equals the immutable target fingerprint. Otherwise it preserves staging and
returns to dirty/conflict state.

Obsidian does not provide a Vault-wide or OS-level transaction. Text
modifications use `Vault.process` to serialize Obsidian-coordinated writes and
recheck exact expected bytes inside its callback, but an external filesystem
writer can still change the file after that callback check and before Obsidian
persists the target. Binary modification has the analogous residual race
between the plugin's precondition check and `modifyBinary`. Moving a remote-
deleted file with
`FileManager.trashFile` has the same read-check-to-trash race, and the trash
destination is excluded from the confirming logical scan. Every `trash_file`
therefore requires an explicit user confirmation, rechecks its exact expected
digest immediately before trashing, and reports the residual race; it is never
scheduled as an unattended apply action. Replacing an existing binary file is
also explicitly confirmed in alpha, including conversion from non-UTF-8 bytes
to a UTF-8 target, but confirmation does not remove its race. Text mode is
allowed for an existing file only when both expected and target bytes strictly
round-trip as UTF-8 and the `Vault.process` callback rechecks the expected byte
digest before returning the target. Every existing-text replacement is also
explicitly confirmed in alpha because the callback is not an OS-level CAS.

The plugin must abort on a mismatched precondition or an unexpected same-path
event, never force the prepared action over it. The final scan is authoritative
for the visible tree, but it cannot retroactively recover a binary edit
overwritten inside that residual race or prove what bytes entered trash. Alpha
testing must therefore include concurrent editor and external-writer
adversarial cases. A per-path native lock, compare-and-swap primitive, or
recoverable adapter quarantine with explicit evidence is a stable-release gate
for every write and every deletion, whether the expected entry is missing or
existing, text or binary, attended or unattended. The Obsidian Vault API does
not promise an OS-level create-if-absent transaction against an arbitrary
external writer, so a missing-file precheck does not make creation safe for
stable mode. Until a supported primitive proves create-if-absent, replacement,
and deletion behavior, every apply containing `write_file` or `trash_file`
is alpha-only for disposable/test Vaults and blocks Community-directory stable
submission. Stable rejection is session-wide before the first Vault API call,
so it cannot leave an otherwise harmless preceding `mkdir` behind.

## Conflicts

Gateway conflicts and local plaintext conflicts retain their existing full
`base`, `local`, `remote`, and `merged` evidence in owner-only daemon storage.
The plugin receives affected paths, roots, kinds, lengths, and content digests,
not plaintext conflict objects or direct checkout paths in v0.

The UI may offer:

- keep local;
- keep remote, after showing the exact accepted remote root;
- accept the daemon's conflict-free automatic merge; or
- edit a manual merged version through a temporary Vault-visible file flow
  defined by a later contract revision.

The desktop alpha should implement status plus keep-local/keep-remote and
automatic conflict-free merge first. It must not expose the existing filesystem
checkout path as though editing it inside Obsidian were safe. Manual per-file
conflict editing remains a release gate for beta, not a reason to move merge
logic into TypeScript.

## Crash recovery

Pairing, sync operations, apply sessions, operation acknowledgements, and final
confirmation are journaled before their visible transition. Request IDs and
acknowledgements are idempotent.

- If the daemon restarts before apply, it validates the staged session and
  republishes `apply-ready`.
- If the plugin restarts during apply, the daemon checks the immutable
  operations in order. It first revalidates every successfully acknowledged
  operation's exact postcondition, including physical `TFolder` creation; any
  drift invalidates the session before another side effect. It then extends the
  prefix across exact unacknowledged postconditions; the first exact
  precondition is the resume point. Any other or out-of-order state invalidates
  the session.
- If an operation was acknowledged but its postcondition is uncertain, the
  daemon scans that path and either retains it in the successful prefix or
  invalidates the session. Failed/precondition-failed acknowledgements never
  enter that prefix or permit later operations.
- If the Vault changed while the plugin was absent, the daemon abandons the
  stale session only after retaining sufficient diagnostic metadata, then
  starts a fresh scan/merge.
- No recovery path advances accepted root, scan baseline, or backup history
  from plugin acknowledgement alone.

## Delivery plan

### Phase 0: contract

- land this design and the v0 IPC draft in `malt-client`;
- keep all existing Backup Plan behavior unchanged; and
- create no plugin repository until route/type names have an implementation
  owner and conformance fixture.

### Phase 1a: compatibility fence

- ship mandatory coordinator leases for every current Plan operation and
  PlanStore writer while adapter approval/enrollment remains hard-disabled;
- add Linux, macOS, and Windows upgrade-fence providers that acquire and keep
  the v1 `plansPath + ".lock"`, stop and suppress daemon restart, reject any
  detected pre-transition daemon/direct CLI that already loaded v1, and wait
  for coordinated operations to drain;
- only while that compatibility lock and proven quiescence remain continuous,
  migrate PlanStore v1 to v2 atomically, write explicit `filesystem`
  descriptors plus the minimum-client enable epoch, and verify old binaries
  blocked during the fence reject the store after release; and
- release and validate this transition before any build can create an adapter
  record or manifest.

### Phase 1b: daemon foundation

- add a reusable workspace application service separate from `cmd/malt`;
- add the dedicated adapter socket/pipe and mux, owner-only adapter registry,
  pairing/approval CLI, capability validation, operation journal, and async
  status/events;
- version the encrypted client-owned Plan manifest and require the exact
  single-Binding workspace-adapter marker during enrollment;
- require the completed PlanStore v2 enable epoch before mounting adapter
  approval/enrollment routes;
- require a generic Bucket branch create-if-absent CAS receipt before creating
  new Plan/Binding IDs; no error or missing head is empty-branch evidence;
- refactor verified restore so it can prepare a plaintext target without
  calling `installPrepared`;
- add an immutable plaintext snapshot primitive whose scan fingerprint and
  encrypted archive are derived from the same staged bytes, followed by an
  exact fresh live scan;
- refactor three-way merge over a policy-aware canonical entry projection so
  adapter merge ignores mode and empty-directory differences without changing
  generic Backup Plan semantics;
- retain filesystem replacement as the generic Binding installer;
- add guards proving an adapter-backed Plan cannot reach generic/scheduled
  publication or the filesystem installer through backup, sync, or conflict
  commands; and
- add Linux/macOS socket and Windows named-pipe contract tests plus cross-build.

### Phase 2: plugin alpha

- create `DeWebProtocol/malt-obsidian` with npm, TypeScript, lock file,
  `manifest.json`, `versions.json`, release workflow, `isDesktopOnly: true`,
  and `minAppVersion` at least `1.11.4` because v0 requires
  `App.secretStorage`;
- implement pairing, SecretStorage, event hints, status, candidate acceptance,
  bounded status-only reconciliation scans, sync start, immutable object
  download, Vault API apply, and acknowledgement;
- test only against disposable Vaults; and
- publish GitHub prereleases consumable through BRAT.

### Phase 3: product E2E and beta

- run daemon + Gateway + real Obsidian desktop fixture tests for local-only,
  remote-only, independent merge, same-file conflict, restart at every journal
  phase, large attachments, case collisions, trash behavior, and concurrent
  edits;
- complete manual conflict UI without moving merge policy into the plugin; and
- document daemon installation, upgrades, recovery keys, disclosure, and
  support boundaries.

### Phase 4: stable directory submission

- require a supported recoverable or atomic primitive for create-if-absent,
  existing-file replacement, and deletion before enabling any Vault content
  mutation in stable;
- attach `main.js`, `manifest.json`, and optional `styles.css` to a GitHub
  Release whose tag equals the manifest version;
- pass the Obsidian plugin guidelines and self-critique checklist; and
- submit `malt-sync` to the Community plugins directory only after the daemon
  compatibility and rollback policy is stable.

## Desktop acceptance gates

The first implementation is not complete until tests demonstrate:

- a dropped or duplicated event is caught by the bounded live-plugin scan
  cadence, and an overdue scan changes `idle` to `stale`;
- local edits are snapshotted before remote observation, and ABA/partial-read
  races cannot make staged bytes diverge from the candidate fingerprint;
- the adapter endpoint exposes no general control routes, and the plugin cannot
  address another Binding or read arbitrary daemon files;
- provisional and initialized Vault paths are reserved against generic bind,
  restore, foreground, and second-adapter overlap;
- unauthenticated pairing cannot invoke sync, trust, apply, root, key, or
  lifecycle operations;
- remote bytes are proof/CID verified before decryption and staged outside the
  Vault;
- no daemon path swaps or directly writes the Vault Binding;
- an adapter-backed Plan cannot enter generic/scheduled backup publication or
  the generic sync, keep-remote, or manual filesystem installer, including
  through foreground CLI and timer bypasses;
- installer-policy recovery failure, descriptor/registry mismatch, concurrent
  enrollment, and a scheduler holding a stale Plan all fail closed for the
  entire transaction;
- the transition release keeps enrollment disabled until the platform upgrade
  fence holds the v1-compatible PlanStore lock continuously across process
  census, quiescence, durable v2 migration, and enablement; PlanStore v2 makes
  old CLIs blocked by that fence fail on reopen, and bind/import/schedule/future
  writers cannot mutate an adapter Plan or its Bucket/branch target;
- every plugin write/trash uses Obsidian APIs and checks its operation
  precondition;
- an acknowledgement without a matching full scan cannot advance history;
- restart recovery is idempotent at every apply operation, revalidates the
  entire successful prefix before resuming, and stops on every failed ack;
- provisional enrollment preserves the local scan, imports existing remote
  Binding identity only from the exact marked version 2 manifest after exact
  root acceptance, creates new IDs only from the exact journaled branch-create
  receipt, never publishes a raw local tree when joining an existing branch,
  publishes no candidate on first-join conflict, publishes only the clean
  empty-base merge against the exact accepted remote root, converges byte-
  identical same-path additions, and represents differing same-path additions
  as `both-added` with a missing logical base;
- v0 excludes dot-prefixed paths and rejects included symlinks identically on
  Linux, macOS, and Windows;
- pairing rejects a non-dot, nested, or changed `Vault.configDir` instead of
  creating a device-specific exclusion;
- the portable path fixture rejects Unicode/case/Windows alias collisions
  and every single non-NFC raw segment identically on every host;
- adapter confirmation ignores filesystem metadata and empty directories and
  never trashes a folder containing potentially excluded local content;
- a physical file/directory namespace obstruction becomes `blocked` before an
  apply session is published;
- stable mode rejects the whole apply before any Vault API call when it would
  create, replace, or delete a file, so preceding `mkdir` operations leave no
  side effect, until a supported recoverable or atomic primitive closes both
  missing-create and existing-entry races; and
- every mutation has a durable required idempotency key and survives replay
  across daemon restart for longer than the plugin retry window.

## Deferred mobile decision

Desktop v0 intentionally exposes application operations, not Go objects or
filesystem installer internals. That keeps both mobile options open:

- WASM could run a reduced trusted engine inside a mobile plugin, but would
  require a secure key store, durable background execution, networking, and a
  replacement for desktop IPC; or
- a native bridge could host the current Go engine and OS key facilities, but
  requires separate iOS/Android packaging, review, and lifecycle integration.

Choose only after measuring bundle size, startup/memory, background limits,
secure-key support, and Community-plugin policy. The desktop daemon remains
the reference semantic implementation either way; mobile must not create a
second merge or trust policy.

## External API constraints

- [Obsidian Vault API](https://docs.obsidian.md/Plugins/Vault)
- [Obsidian events and lifecycle](https://docs.obsidian.md/Plugins/Events)
- [Obsidian plugin guidelines](https://docs.obsidian.md/Plugins/Releasing/Plugin%20guidelines)
- [Obsidian plugin submission](https://docs.obsidian.md/Plugins/Releasing/Submit%20your%20plugin)
- [Obsidian manifest reference](https://docs.obsidian.md/Reference/Manifest)
- [Obsidian SecretStorage guide](https://docs.obsidian.md/plugins/guides/secret-storage)
- [Obsidian API type definitions](https://github.com/obsidianmd/obsidian-api/blob/master/obsidian.d.ts)
