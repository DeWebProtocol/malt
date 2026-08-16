# Workspace Adapter IPC v0

Status: implementation draft. This contract may change incompatibly until the
daemon and the first adapter publish matching conformance fixtures.

This document specifies a least-authority local control plane between a
desktop workspace adapter and the trusted `malt-client` daemon. Obsidian is the
first adapter. The design and product boundary are described in
[Obsidian Integration Design](./obsidian-integration.md).

This is a client-local application contract. It is not a Gateway API, MALT
protocol, proof format, CID format, or public network service.

## 1. Transport and discovery

The daemon serves HTTP/1.1 semantics over a dedicated workspace-adapter
endpoint:

- Unix: a configured user-owned Unix-domain socket distinct from the CLI
  control socket, whose directory is owner-only and whose socket mode is
  `0600`;
- Windows: a configured workspace-adapter named pipe, distinct from the CLI
  control pipe and restricted to the owning user and SYSTEM. Its endpoint
  descriptor contains at least 128 random bits generated into owner-only daemon
  state; it is not derived solely from a public path or account name.

The daemon MUST NOT bind these routes to TCP, including loopback TCP. A Unix
adapter uses a Node HTTP client with `socketPath`; a Windows adapter dials the
named-pipe path. The logical authority in request URLs is `malt.local` and has
no DNS meaning.

The adapter listener has its own HTTP mux. It contains only contract
discovery, pairing, and capability-authenticated workspace-adapter routes. It
MUST NOT mount the daemon's general roots, backup, lifecycle, key, recovery, or
Gateway control routes. The plugin never connects to the CLI control endpoint
during normal operation. On Unix, a platform-default adapter path may be
overridden by an explicit non-secret plugin setting. On Windows, the user
copies the random endpoint descriptor from `malt workspace endpoint` into
Obsidian SecretStorage before pairing. The daemon uses first-instance/create-
new pipe semantics and fails closed if that name already exists; it never lets
the plugin fall through to an existing server. Endpoint secrecy is defense
against another Windows principal pre-creating a public pipe name, not a
replacement for the scoped capability.

JSON requests and responses use:

```http
Content-Type: application/vnd.malt.workspace.v0+json
Accept: application/vnd.malt.workspace.v0+json
```

Staged objects use `application/octet-stream`. JSON request bodies are limited
to 1 MiB. Object response limits are declared per object and MAY support byte
ranges.

The adapter starts with:

```http
GET /v1/workspace-adapters/capabilities
```

Example response:

```json
{
  "daemon_role": "trusted-client",
  "transport": "private-local",
  "contracts": ["workspace-adapter/0"],
  "adapter_kinds": ["obsidian"],
  "max_json_bytes": 1048576,
  "supports_object_ranges": true,
  "reconcile_interval_seconds": 300,
  "idle_stale_after_seconds": 600,
  "max_retry_seconds": 86400,
  "min_idempotency_retention_seconds": 172800
}
```

This is an unauthenticated discovery route because endpoint ownership already
limits it to the local user and the dedicated mux exposes no general control
surface. It exposes no state path, account, Plan, Bucket, Binding, root, key,
credential, pairing list, or workspace list.

Every successful response includes:

```http
X-Malt-Workspace-Contract: workspace-adapter/0
X-Request-ID: <opaque daemon request ID>
```

An adapter MUST refuse an unsupported contract rather than trying a nearby
version. The future stable contract will receive a nonzero version; v0 has no
backward-compatibility promise.

## 2. Common values

All IDs are opaque, URL-safe, case-sensitive strings. Clients MUST NOT infer a
filesystem path, Bucket, root, or ordering from an ID.

Times are UTC RFC 3339 strings. Byte counts are non-negative JSON integers.
Content digests use lowercase hex SHA-256:

```text
sha256:<64 lowercase hexadecimal characters>
```

Vault paths are normalized relative paths with `/` separators. They MUST NOT:

- be empty where a file is required;
- begin with `/` or a drive/UNC prefix;
- contain an empty, `.`, or `..` segment;
- contain NUL or a name invalid under the portable v0 policy; or
- escape the paired canonical Vault root after platform resolution.

The daemon validates every path when creating a session. The adapter validates
again with Obsidian `normalizePath` and MUST require exact equality with the
contract path. Normalization is not authorization.

The portable v0 policy is identical on every host and is defined by revision 1
of [`obsidian-visible-v0-paths.json`](fixtures/obsidian-visible-v0-paths.json),
whose exact bytes have SHA-256
`0fddd4e451f00585718c14392dbd7bcf4e8f9e804f3ee9aec1f20ea65a3fb6dd`.
Every raw source, decrypted remote, and apply-path segment MUST be valid UTF-8
and already byte-for-byte Unicode 17.0.0 NFC; v0 rejects a lone non-NFC segment
rather than inventing a raw-to-logical alias.

The per-segment collision key is exactly
`NFC17(DefaultCaseFold17_CF(NFD17(segment)))`; path keys join those values
with `/`. Full, non-Turkic Default Case Folding uses only status C and F
mappings from `CaseFolding-17.0.0.txt`, never the simple S or Turkic T
alternatives. The policy pins:

- `CaseFolding-17.0.0.txt`: SHA-256
  `ff8d8fefbf123574205085d6714c36149eb946d717a0c585c27f0f4ef58c4183`;
- `NormalizationTest-17.0.0.txt`: SHA-256
  `5019ffd530751a741900c849c0e010332f142a3612234639bd200b82138a87db`.

The daemon rejects matching collision keys even on a case-sensitive
filesystem. On every OS it also rejects U+0000 through U+001F, `< > : " \\ |
? *`, a trailing ASCII space/dot, and the exact ASCII-case-insensitive device
stems `CON`, `PRN`, `AUX`, `NUL`, `COM1` through `COM9`,
`COM¹`/`COM²`/`COM³`, `LPT1` through `LPT9`, and
`LPT¹`/`LPT²`/`LPT³`. The device stem is the segment prefix before the
first dot, so extensions remain invalid. Native Go, JavaScript, OS, or
filesystem tables may be used only when they pass the complete pinned fixture
and upstream normalization test. Pairing, manifests, and branch writer
metadata bind the fixture digest; an absent or different digest is an
unsupported content policy, not a locally selected variant.

The v0 logical tree contains regular files by relative path, exact bytes,
length, and SHA-256 digest. Parent folders are derived containers. Empty
folders, mtime, permission mode, xattrs, and ACLs are not synchronized or
included in the confirmed tree fingerprint. The daemon may use metadata only
as a rehash optimization. v0 never issues a folder-delete operation because a
Vault-visible empty folder may contain excluded hidden local entries.

Tree fingerprints are durable daemon equality tokens represented with the
digest syntax above. The adapter never constructs, submits, or treats their
canonical encoding as a portable MALT value; it only follows the session and
generation that carry them.

Every mutating request MUST carry a non-empty `Idempotency-Key` header;
omission returns `400 invalid_request`. Authenticated keys are scoped to
capability, route, and workspace. Pairing creation has no capability yet, so
its key is scoped to installation ID, adapter kind, and route. Reusing any key
with a different body returns `409 idempotency_mismatch`; replaying the same
request returns the original logical result across daemon restarts.

The plugin retries an unknown response for at most the advertised
`max_retry_seconds`. The daemon durably retains a pairing key/result until the
pairing is terminal plus at least `min_idempotency_retention_seconds`, and an
authenticated key/result until its operation/apply/conflict is terminal plus
that interval. A mutation without a durable resource, such as a dirty hint, is
retained for at least the same interval from first acceptance. The approval-
status route is a read and capability secrets are never response payloads.

## 3. Errors

Non-2xx JSON responses use this envelope:

```json
{
  "error": {
    "code": "apply_precondition_failed",
    "message": "notes/today.md changed after the prepared scan",
    "retryable": false,
    "workspace_id": "ws_...",
    "operation_id": "op_...",
    "apply_id": "apply_...",
    "details": {}
  }
}
```

`message` is diagnostic and MUST NOT be parsed. Stable v0 codes are:

| HTTP | Code | Meaning |
|---|---|---|
| 400 | `invalid_request` | JSON, identifier, path, or state value is invalid. |
| 401 | `invalid_capability` | Capability is absent, malformed, expired, or unknown. |
| 403 | `scope_denied` | Capability is valid but lacks this exact workspace action. |
| 404 | `not_found` | Scoped resource does not exist or is deliberately concealed. |
| 409 | `state_conflict` | Workspace state cannot perform the requested transition. |
| 409 | `vault_identity_mismatch` | Installation, current Vault path/config, or path generation differs from approval. |
| 409 | `branch_writer_mismatch` | Gateway branch writer contract/epoch/capability is absent or mismatched. |
| 409 | `idempotency_mismatch` | An idempotency key was reused with another request. |
| 409 | `apply_precondition_failed` | Vault state differs from the immutable apply precondition. |
| 410 | `pairing_expired` | Pairing request can no longer be approved. |
| 410 | `apply_expired` | Apply session can no longer be resumed. |
| 413 | `request_too_large` | JSON body exceeds the declared limit. |
| 416 | `invalid_range` | Object byte range is invalid. |
| 423 | `workspace_busy` | A serialized workspace mutation is active. |
| 429 | `rate_limited` | Pairing or operation rate limit was exceeded. |
| 503 | `daemon_not_ready` | Recovery or required application services are incomplete. |

Gateway, verification, decryption, key, trust, and merge failures receive a
safe application-specific `code` and remain errors; the adapter MUST NOT map a
generic transport failure to clean or synchronized state.

## 4. Pairing

Pairing creates authorization; discovering the private socket does not.

### 4.1 Create request

The plugin generates a random 256-bit `capability_secret`, stores it as a
pending value in Obsidian `SecretStorage`, and sends only its SHA-256 digest:

```http
POST /v1/workspace-adapters/pairings
Idempotency-Key: <plugin pairing attempt ID>
```

```json
{
  "contract": "workspace-adapter/0",
  "adapter_kind": "obsidian",
  "installation_id": "install_...",
  "workspace": {
    "display_name": "Research",
    "desktop_path": "/Users/alice/Notes/Research",
    "config_dir": ".obsidian"
  },
  "content_policy": {
    "id": "obsidian-visible-v0",
    "revision": 1,
    "fixture_sha256": "sha256:0fddd4e451f00585718c14392dbd7bcf4e8f9e804f3ee9aec1f20ea65a3fb6dd"
  },
  "capability_digest": "sha256:..."
}
```

`installation_id` is random, non-secret plugin data. `desktop_path` is the
current value returned by the desktop `FileSystemAdapter.getBasePath()`; it is
a claim, not authorization. `config_dir` is read from `Vault.configDir`; it
must be one portable non-empty path component beginning with `.`, not a hard-
coded assumption. The daemon canonicalizes the path, validates that it is an
existing directory and a safe potential Binding, validates the configuration
component and exact content-policy identity, and stores both the reported and
canonical paths in the request for at most ten minutes.

Example `201` response:

```json
{
  "pairing_id": "pair_...",
  "approval_code": "MALT-7K4P-XM2D-9QRF-V6TW-3H8C",
  "expires_at": "2026-08-16T10:15:00Z",
  "approval_command": "malt workspace approve MALT-7K4P-XM2D-9QRF-V6TW-3H8C --bucket <bucket> --branch <branch>"
}
```

The approval code contains at least 80 bits of randomness, is rate-limited and
single-use, and is not the capability. It is intended for copy/paste rather
than manual transcription. Listing pairings over the adapter API is forbidden.

### 4.2 CLI approval

Approval is a CLI operation, not an adapter route:

```text
malt workspace approve <approval-code> --bucket <selector> \
  --branch <branch> [--create-branch]
```

v0 has no implicit branch default and forbids `main` as an adapter target.
Both joining and creation require an explicit non-`main` branch. When
`--create-branch` is present, that name must not already exist; without it,
the named branch must already carry the exact immutable adapter writer
descriptor. This matches the Gateway's existing reserved `main` ref and avoids
reinterpreting a pre-created empty `main` as a create-if-absent result.

Before confirmation the CLI displays:

- adapter kind, Vault display name, and canonical absolute path;
- the exact reported `Vault.configDir` and the requirement that it remain a
  single dot-prefixed component;
- whether the exact Bucket/branch will enroll an existing remote Plan or the
  explicit `--create-branch` path will reserve a new branch identity;
- exact Bucket and branch;
- fixed content-policy ID, revision, fixture digest, Unicode/Windows rules,
  and unsupported-object policy;
- immutable Gateway branch writer contract/epoch and whether this device has
  an approved branch-writer capability;
- requested workspace scopes; and
- whether an old capability will be rotated.

Approval calls the existing protected-source and global Binding-overlap
validation. It fails closed if the path, pairing, account, Bucket, or branch
changed. It does not accept a Gateway root or perform a scan/sync.

Adapter approval requires the Gateway to expose an immutable generic branch
writer descriptor with this exact value:

```json
{
  "contract_id": "malt-client.backup-plan/workspace-adapter-v0",
  "epoch": 1,
  "manifest_version": 2,
  "content_policy_id": "obsidian-visible-v0",
  "content_policy_fixture_sha256": "sha256:0fddd4e451f00585718c14392dbd7bcf4e8f9e804f3ee9aec1f20ea65a3fb6dd"
}
```

Each explicitly approved device receives a separate revocable Gateway branch-
writer capability ID backed by a daemon-generated random 256-bit secret. Before
requesting either branch creation or a join grant, the daemon first fsyncs an
owner-only pending keyring record containing the secret and its SHA-256 digest,
then durably journals a non-secret pending-grant record containing that digest,
request idempotency key, authorization principal, Bucket, explicit branch
name, contract/epoch, and issuing device credential. It sends the request only
after both records are durable. Only the digest is sent to or stored by the
Gateway. After the Gateway resolves the immutable branch ID, its
authorization record is bound to the exact tuple
`(authorization_principal_id, bucket_id, immutable_branch_id, contract_id,
epoch, device_credential_id)` plus that digest; `authorization_principal_id`
is the canonical account or tenant security principal used by Gateway
authorization. The plugin, encrypted manifest, and Plan store never receive
the secret.

Every branch push carries the exact contract ID/epoch, capability ID, and
secret under the issuing device credential. In the same authorization
transaction, the Gateway hashes the presented secret, matches the digest and
all six scope fields, and rejects an absent, cross-principal, cross-Bucket,
cross-branch, cross-device, mismatched, stale, or revoked assertion before
candidate materialization, idempotency reservation, conflict preservation,
merge, or ref mutation. Bucket writer role alone is insufficient, so an old
v1 device cannot fast-forward or otherwise mutate the adapter branch.

New Plan/Binding IDs are allowed only when approval names a new non-`main`
branch, includes `--create-branch`, and a generic Bucket branch create-if-
absent CAS succeeds while atomically binding that writer descriptor. The
authenticated response must bind the authorization principal, Bucket, exact
branch name, immutable branch identity, zero/head-absent generation, issuing
device credential, request idempotency key, creation generation, complete
writer descriptor, non-secret writer-capability ID, and the exact pending
secret digest. The capability grant in that receipt has the exact six-field
scope above. The daemon re-reads and matches that branch metadata and receipt
digest, journals the receipt and capability ID in the provisional enrollment
record, and atomically promotes the matching pending keyring record to active
before approval returns. If the branch already exists, the CAS fails; v0 does
not initialize an existing empty-looking branch, including the Bucket's pre-
created `main`. A lost response is recovered by the idempotency key and exact
receipt plus the already durable pending secret, never by interpreting a later
`404` as absence or asking the Gateway to replay plaintext secret material.

Joining an existing non-`main` adapter branch requires its immutable descriptor
to match exactly and a Gateway-authenticated approval under the joining device
credential to bind the already persisted pending digest to a new capability
ID with the exact principal/Bucket/branch/contract/epoch/device scope. The
idempotent join-grant receipt returns that ID, digest, and complete scope so a
lost response is recoverable exactly like creation. An uncontracted branch, a
descriptor/policy mismatch, `main`, or a manifest without matching branch
metadata is unsupported. v0 has no in-place contract adoption, epoch downgrade,
or generic-branch migration. Revocation names the scoped capability ID and
cannot revoke or rotate a capability outside that same principal/Bucket/
branch/device scope. If recovery finds the pending secret missing or corrupt,
it fails closed and uses an explicit authenticated revoke-and-reissue flow with
a new durable pending secret/idempotency key; a capability ID or receipt alone
never reconstructs or authorizes a secret.

Approval creates a provisional workspace for the canonical path and exact
Bucket/branch. It does not invent a Plan or Binding ID when an encrypted remote
manifest may already exist. The first workspace sync freezes the local scan,
then observes and explicitly accepts the root before verified manifest
discovery. It imports an existing identity only from an encrypted version 2
manifest with exactly one Binding and this exact client-owned application
marker:

```json
{
  "version": 2,
  "workspace_adapter": {
    "kind": "obsidian",
    "contract": "workspace-adapter/0",
    "content_policy": {
      "id": "obsidian-visible-v0",
      "revision": 1,
      "fixture_sha256": "sha256:0fddd4e451f00585718c14392dbd7bcf4e8f9e804f3ee9aec1f20ea65a3fb6dd"
    },
    "writer_contract": {
      "id": "malt-client.backup-plan/workspace-adapter-v0",
      "epoch": 1
    },
    "installer": "vault-api"
  }
}
```

The Plan/Binding IDs and remaining metadata are authenticated fields in that
same manifest. This is a `malt-client` schema, not a MALT wire or Gateway
schema. A version 1 generic Plan, an absent or mismatched marker, a non-empty
branch with no valid adapter manifest, and a multi-Binding Plan are unsupported
in v0. Without the exact journaled create-if-absent receipt, the first sync
must import the marked remote identity and cannot create new IDs. Network,
authorization, timeout, missing-head, manifest lookup, proof, decryption,
decode, and validation failures are errors, never evidence of an empty branch.
A local Plan already targeting the Bucket/branch must already be the exact
adapter Plan or approval fails; the user chooses another branch. No automatic
legacy migration is permitted. The current Gateway create/push APIs do not yet
implement the required receipt, immutable writer descriptor, or per-device
capability checks; adapter approval, enrollment, and publication are therefore
hard-disabled until that generic Gateway dependency is deployed, even if an
existing head appears to contain a valid version 2 manifest.

The provisional record reserves its canonical path before approval returns.
Plan binding, restore-destination, foreground, and other adapter validation
MUST consult both provisional and initialized workspace reservations under
shared cross-process coordination. Enrollment activates the imported/created
Plan Binding and workspace record with a crash-recoverable journal so the path
cannot become eligible for the generic installer between files.

After enrollment, generic and scheduled backup, generic sync, and
conflict-install commands reject the adapter Plan and direct the user to the
paired workspace workflow. Only an authenticated workspace request carrying
the complete current live Vault assertion may scan for publication or push a
candidate, and only a daemon holding the matching Gateway branch-writer
capability may publish it.

### 4.3 Observe approval

The plugin polls the opaque request without sending its secret:

```http
GET /v1/workspace-adapters/pairings/{pairing_id}
```

Before approval the route returns `200` with `state: "pending"`. Rejection
returns `state: "rejected"`; expiry returns `410`. Successful approval
returns:

```json
{
  "state": "approved",
  "workspace_id": "ws_...",
  "scopes": [
    "workspace:read",
    "dirty:write",
    "scan:start",
    "sync:start",
    "candidate:accept",
    "conflict:resolve",
    "apply:read",
    "apply:write"
  ],
  "content_policy": {
    "id": "obsidian-visible-v0",
    "revision": 1,
    "fixture_sha256": "sha256:0fddd4e451f00585718c14392dbd7bcf4e8f9e804f3ee9aec1f20ea65a3fb6dd"
  },
  "vault_identity": {
    "installation_id": "install_...",
    "approved_desktop_path": "/Users/alice/Notes/Research",
    "canonical_desktop_path": "/Users/alice/Notes/Research",
    "config_dir": ".obsidian",
    "path_generation": 1
  }
}
```

CLI approval atomically activates the digest committed by the request. The
daemon stores only that SHA-256 digest and compares bearer secrets in constant
time; the 256-bit random capability is not password-derived and is never
returned by the daemon. The plugin promotes its pending SecretStorage entry to
active after observing approval. If that pending secret is lost, the workspace
must be re-paired; there is no lossy one-time token-delivery response to replay.
The plugin MUST NOT put the capability, Gateway credential, recovery material,
or daemon lifecycle token in `data.json`.

Deleting a pending pairing and revoking/rotating an approved workspace are CLI
operations. Adapter self-revocation MAY be added later, but v0 does not allow
the plugin to delete the Plan, Binding, remote branch, or local Vault.

## 5. Capability authentication

Every route after pairing uses:

```http
Authorization: Bearer <workspace capability>
```

The capability is bound to one workspace record, plugin installation ID,
canonical approved Vault path, monotonic path generation, and exact scope set.
A path parameter naming another workspace returns `404`, not a cross-
workspace existence signal. The adapter listener has no general control
handlers, and the adapter never receives the daemon lifecycle instance token.

### 5.1 Live Vault assertion

Every capability-authenticated workspace route MUST carry the plugin's current
Vault identity. The unauthenticated pairing-status poll is outside this rule.
JSON mutation bodies include:

```json
{
  "vault_assertion": {
    "installation_id": "install_...",
    "desktop_path": "/Users/alice/Notes/Research",
    "config_dir": ".obsidian",
    "path_generation": 1
  }
}
```

`desktop_path` is freshly read from desktop
`FileSystemAdapter.getBasePath()`; `config_dir` is freshly read from
`Vault.configDir`. For bodyless GET/Range routes the same object is UTF-8
compact JSON, base64url-encoded without padding, in
`Malt-Vault-Assertion`. The decoded value is bounded to 8 KiB. Duplicate JSON
keys, unknown fields, invalid UTF-8, an oversized value, or a missing assertion
are rejected.

On every request the daemon compares installation ID and generation, resolves
and canonicalizes `desktop_path` using the same pairing rules, and requires
the result plus `config_dir` to equal the approved record. It performs this
check before a scan, dirty scheduling, operation/candidate/conflict mutation,
staged-object response, or apply continuation. A mismatch returns
`409 vault_identity_mismatch`, blocks the workspace, and has no scan,
publication, trust, journal-prefix, object-delivery, or Vault-side effect.

`path_generation` starts at 1 and is monotonic for any recovered or explicitly
approved identity-record replacement. Moving, renaming, or reopening a
different Vault never retargets a workspace. v0 requires a new pairing for the
new path and explicit revocation of the old workspace; there is no silent path
update. Every queued operation, candidate, conflict, apply session, and
idempotency record freezes the workspace path
generation. Replaying it after any identity generation change fails before its
logical result can be reused.

The workspace API MUST NOT proxy or expose:

- generic root create/trust/candidate endpoints;
- arbitrary Backup Plan selectors or source paths;
- keyring, recovery, device credential, or Gateway authentication operations;
- arbitrary Gateway routes, CAS reads, or filesystem reads;
- daemon start, stop, restart, PID, socket unlink, or lifecycle identity; or
- staging directory paths and conflict checkout paths.

## 6. Workspace and status

```http
GET /v1/workspace-adapters/workspaces/{workspace_id}
```

Example response:

```json
{
  "workspace_id": "ws_...",
  "adapter_kind": "obsidian",
  "display_name": "Research",
  "vault_identity": {
    "installation_id": "install_...",
    "approved_desktop_path": "/Users/alice/Notes/Research",
    "canonical_desktop_path": "/Users/alice/Notes/Research",
    "config_dir": ".obsidian",
    "path_generation": 1
  },
  "branch_writer": {
    "contract_id": "malt-client.backup-plan/workspace-adapter-v0",
    "epoch": 1,
    "capability_id": "bwc_..."
  },
  "plan": {
    "id": "plan_...",
    "name": "Research",
    "bucket_id": "bucket_...",
    "bucket_name": "notes",
    "branch": "obsidian-research",
    "binding_id": "binding_..."
  },
  "enrollment": null,
  "state": "dirty",
  "scan_generation": 41,
  "confirmed_generation": 40,
  "last_full_scan_at": "2026-08-16T10:20:00Z",
  "dirty_since": "2026-08-16T10:20:00Z",
  "active_operation_id": null,
  "apply_id": null,
  "conflict_id": null,
  "accepted_root": "bafy...",
  "candidate": null,
  "last_error": null
}
```

`plan` is `null` while a newly approved workspace is provisional. Its
immutable Bucket/branch target is returned as:

```json
{
  "plan": null,
  "enrollment": {
    "state": "provisional",
    "bucket_id": "bucket_...",
    "bucket_name": "notes",
    "branch": "obsidian-research",
    "writer_contract_id": "malt-client.backup-plan/workspace-adapter-v0",
    "writer_epoch": 1,
    "writer_capability_id": "bwc_..."
  }
}
```

The adapter cannot modify that target. `enrollment` becomes `null` after the
marked version 2 single-Binding Plan is imported or created. An observed
enrollment head is represented only by the top-level `candidate` resource; the
enrollment object never duplicates its root.

Stable state values are:

```text
provisional
enrolling
idle
stale
dirty
scanning-local
backing-up
pushing
observing-remote
awaiting-root-acceptance
merging
preparing-apply
apply-ready
applying
confirming-scan
conflict
blocked
error
```

`idle` means the daemon's last full filtered scan matches its confirmed
baseline and is no older than `idle_stale_after_seconds`. While loaded, the
plugin MUST request a status-only reconciliation scan at least every
`reconcile_interval_seconds`, with jitter and the complete current live Vault
assertion. If no
successful attested scan completes by the stale threshold, the daemon changes
`idle` to `stale`; it never presents an indefinitely old scan as clean.
`idle` does not mean the Gateway is reachable forever or that an unseen remote
head cannot later appear.

## 7. Dirty hints and scans

After layout readiness, the plugin coalesces Vault events:

```http
POST /v1/workspace-adapters/workspaces/{workspace_id}/dirty
Idempotency-Key: <plugin event batch ID>
```

```json
{
  "vault_assertion": {
    "installation_id": "install_...",
    "desktop_path": "/Users/alice/Notes/Research",
    "config_dir": ".obsidian",
    "path_generation": 1
  },
  "observed_at": "2026-08-16T10:20:00Z",
  "overflow": false,
  "hints": [
    {"kind": "modify", "path": "notes/today.md"},
    {"kind": "rename", "path": "assets/new.png", "old_path": "assets/old.png"}
  ]
}
```

Kinds are `create`, `modify`, `delete`, `rename`, and `unknown`. Paths are
diagnostic scheduling hints. The daemon MAY ignore, collapse, or redact them;
it MUST NOT update a fingerprint, baseline, candidate, or apply operation from
them. `overflow: true` asks for prompt full reconciliation.

An empty hints array with `overflow: true` is valid after plugin startup,
resume, or event-listener uncertainty. The plugin then requests a full scan.
The same scan is requested on the advertised live-plugin cadence even when no
event arrived. A dropped ordinary event is therefore discovered by the next
scan; if the plugin is absent, the daemon reports `stale`. v0 has no offline
periodic or scheduled publication for an adapter Plan, because only the live
plugin can report the active `Vault.configDir`.

```http
POST /v1/workspace-adapters/workspaces/{workspace_id}/scans
Idempotency-Key: <request ID>
```

```json
{
  "vault_assertion": {
    "installation_id": "install_...",
    "desktop_path": "/Users/alice/Notes/Research",
    "config_dir": ".obsidian",
    "path_generation": 1
  }
}
```

requests an asynchronous full scan. It returns `202` with an operation. The
daemon MUST validate the complete assertion before opening the recorded source
path and MAY coalesce it only with an active or queued scan carrying the same
identity tuple. This standalone route updates scan generation and status only;
it never creates or publishes a local candidate. Candidate publication occurs
only inside an explicit sync operation carrying the same current assertion.

Every full scan that may report `idle`, confirm an apply, or feed a local
candidate MUST construct an owner-only immutable plaintext snapshot while
hashing the included file bytes. After it finishes, the daemon performs a fresh
complete live scan and may use the result only when it exactly equals the
staged fingerprint; otherwise it discards/retries the attempt and keeps the
workspace dirty. A status-only cadence scan then deletes its staged bytes and
never publishes them. For a candidate, both the logical tree fingerprint and
encrypted archive derive from those exact staged bytes; archive creation MUST
NOT reopen the live Vault as its source. File metadata comparisons may avoid
unnecessary reads but are not stability proof. This rule covers ABA edits and
concurrent partial reads that a simple before/archive/after fingerprint
sequence could miss.

## 8. Asynchronous operations and events

Sync and confirming scans may exceed ordinary HTTP client timeouts. Mutating
routes return an operation resource:

```json
{
  "operation_id": "op_...",
  "kind": "sync",
  "state": "queued",
  "created_at": "2026-08-16T10:21:00Z"
}
```

```http
GET /v1/workspace-adapters/workspaces/{workspace_id}/operations/{operation_id}
```

Operation states are `queued`, `running`, `waiting`, `succeeded`, `failed`, and
`cancelled`. A `waiting` operation names one required action such as
`accept_candidate`, `resolve_conflict`, or `apply_session`.

Candidate acceptance, conflict resolution, apply begin/ack/complete, and their
confirmation scan are continuations of that same owning operation. They are
allowed while it is `waiting`; they do not create a second workspace mutation.
The daemon rejects only an unrelated top-level mutation or a continuation whose
immutable generation does not belong to the owning operation. Dirty hints and
read/status/event requests remain concurrent hints or reads.

For timely UI updates the adapter may long-poll:

```http
GET /v1/workspace-adapters/workspaces/{workspace_id}/events?after=<cursor>&wait=30
```

The maximum wait is 30 seconds. A successful response has this minimum
envelope:

```json
{
  "cursor": "cursor_...",
  "events": [
    {
      "id": "event_...",
      "type": "operation_changed",
      "workspace_id": "ws_...",
      "resource": {"kind": "operation", "id": "op_..."}
    }
  ]
}
```

Event types are `status_refresh`, `operation_changed`, `candidate_changed`,
`apply_changed`, and `conflict_changed`. `resource` names only a resource
readable through this workspace capability. It is present for operation,
apply, and conflict events; it is omitted for `status_refresh` and
`candidate_changed`, which direct the plugin to reread workspace status. Event
IDs and cursors are opaque and MUST NOT be compared or used as state versions.
A timeout returns `200` with an empty `events` array and a resumable cursor. A
missing or expired cursor returns `200` with a new cursor and exactly one
`status_refresh` event for the requested workspace; the plugin then reads the
workspace status and any referenced durable resource.

Events are hints to refresh durable resources, not state themselves. The
plugin therefore remains correct after disconnects or lost event batches. v0
requires no WebSocket.

## 9. Start synchronization

```http
POST /v1/workspace-adapters/workspaces/{workspace_id}/syncs
Idempotency-Key: <user action ID>
```

```json
{
  "vault_assertion": {
    "installation_id": "install_...",
    "desktop_path": "/Users/alice/Notes/Research",
    "config_dir": ".obsidian",
    "path_generation": 1
  },
  "reason": "user",
  "message": "Sync from MALT Sync",
  "allow_automatic_merge": true
}
```

`reason` is `user`, `scheduled`, or `reconcile`. Plugin v0 may request `user`
only. The daemon validates the complete live Vault assertion before opening the
source, freezing an operation, or consulting idempotency state. It schedules
one serialized operation and returns `202`.

The daemon MUST execute these semantic stages:

1. recover unfinished workspace and Backup Plan journals, validate the live
   Vault assertion, and verify that the daemon still holds a current writer
   capability for the exact immutable Gateway branch contract/epoch;
2. complete a full policy-filtered local scan into an immutable staged
   plaintext snapshot only after those identity checks;
3. if the workspace is provisional, durably freeze that snapshot and choose
   exactly one enrollment path:
   - with the exact journaled create-if-absent branch receipt, writer
     descriptor, and capability ID, establish a new marked Plan bound to its
     branch generation and prepare the local-only candidate; or
   - for an existing contracted head and an explicitly granted current device
     writer capability, record it as a candidate, wait for exact user
     acceptance, verify/decrypt the marked version 2 single-Binding manifest
     and its Binding tree, and complete the empty-base enrollment merge before
     publication; a conflict publishes nothing, while a clean result stashes
     only the merged candidate with the exact accepted root as its CAS base;
4. create and push the changed encrypted candidate with the exact Gateway
   writer contract/epoch and current device writer capability: an initialized
   workspace and a receipt-created new branch preserve local-candidate-first
   order, but existing-branch enrollment may publish only the merged candidate
   from stage 3; a CAS race returns that enrollment to observation and merge
   and MUST NOT publish its raw local snapshot;
5. for an initialized workspace, fetch the branch only after the local
   candidate is durably stashed/pushed; existing-branch enrollment has already
   performed its exceptional accepted-root fetch in stage 3;
6. require exact root acceptance when trust policy does not already accept the
   observed root;
7. perform Gateway-independent/local three-way merge as needed;
8. verify proofs and CIDs, decrypt outside the Vault, and prepare an apply
   session; and
9. wait for adapter apply plus a confirming daemon scan.

Existing-branch enrollment is the only case that observes a branch before any
local candidate can be published, because the authenticated remote manifest
owns the Binding ID and its remote tree participates in the first merge. The
local tree is scanned and journaled first. When both enrollment trees are
non-empty, an empty logical base permits distinct additions, converges byte-
identical additions at the same path, and treats differing additions at the
same path as conflicts. No first-join conflict publishes a candidate. Only a
new branch backed by the exact create-if-absent receipt may directly publish a
local-only first candidate. A dedicated name or valid encrypted manifest is
never sufficient without immutable branch writer metadata and this device's
current capability.

The route does not accept an arbitrary Plan selector, source path, root,
Gateway URL, merge implementation, key, or destination.

Adapter enrollment is hard-disabled in the transition release that first makes
the coordinator mandatory. Its offline enable command uses a platform upgrade-
fence provider to acquire and continuously hold the existing v1-compatible
exclusive `plansPath + ".lock"` before process census or store inspection. It
then suppresses daemon restart, proves no pre-transition daemon or direct CLI
that already loaded v1 for this client root is still in flight, and drains all
coordinated operations; without that proof it releases the lock without
migration. A newly started old CLI cannot load v1: it blocks or fails on this
compatibility lock, and any retry after release rejects v2. While still
holding it, the command atomically moves the local Plan store from top-level
version 1 to 2, assigns every Plan an explicit installer descriptor, records
the minimum coordinator-capable client epoch, makes the v2 file and enable
marker durable, and performs its final process check. No adapter field is
written into v1 and no adapter Plan exists yet. After lock release, old v1-only
binaries that were blocked during the fence reject v2 on open. A subsequent
adapter-enabled release requires the enable marker before exposing approval/
enrollment.

That local two-release barrier is necessary but not sufficient across devices.
The adapter-enabled release also requires the Gateway writer-contract feature
to be deployed. Conformance must start a real v1 client on another device/
client root with ordinary Bucket writer role and prove its legacy push is
rejected before candidate storage or ref mutation. If the Gateway cannot make
that guarantee, all adapter approval and enrollment remain disabled rather
than claiming single-device safety for a remotely shared branch.

Installer and writer policy are mandatory `PlanService` and Plan-store-writer
dependencies, not handler checks. The local PlanStore v2 descriptor records the
content-policy identity, immutable Gateway writer contract/epoch, and non-
secret capability ID; the capability secret remains in the daemon keyring.
Plan descriptor, adapter registry, encrypted manifest, Gateway branch metadata,
and keyring capability state must agree after recovery.

Every generic publication, install, bind, import, schedule mutation, or future
Plan-store write acquires their current guard generation under the enrollment
coordinator and holds the lease through the entire transaction. Generic bind
rejects the adapter Plan ID and Bucket/branch target even for a disjoint source.
Only enrollment may jointly create/import its single Binding and activate the
descriptor/registry. A scheduler or writer that loaded a Plan earlier must
reacquire current policy. Missing, corrupt, unrecovered, or inconsistent state
fails closed. An adapter-backed Plan therefore cannot enter generic/scheduled
publication or `installBindings`/`installPrepared` through daemon sync,
foreground sync, keep-remote, or manual conflict resolution. The workspace
operation in this section is the only v0 publication/install coordinator for
such a Plan.

## 10. Candidate acceptance

When state is `awaiting-root-acceptance`, status includes an exact candidate
record:

```json
{
  "candidate": {
    "root": "bafy...",
    "base_root": "bafy...",
    "operation_id": "op_...",
    "generation": 42,
    "source": "workspace-sync",
    "observed_at": "2026-08-16T10:22:00Z"
  }
}
```

After showing the exact CID and receiving user confirmation, the plugin may
call:

```http
POST /v1/workspace-adapters/workspaces/{workspace_id}/candidates/{root}/accept
Idempotency-Key: <confirmation ID>
```

```json
{
  "vault_assertion": {
    "installation_id": "install_...",
    "desktop_path": "/Users/alice/Notes/Research",
    "config_dir": ".obsidian",
    "path_generation": 1
  },
  "confirmed": true,
  "expected_operation_id": "op_...",
  "expected_candidate_generation": 42,
  "expected_accepted_root": "bafy..."
}
```

`candidate` is the single authoritative nullable candidate representation in
workspace status. `base_root` is omitted when no accepted MALT base exists;
`operation_id` and `generation` bind it to the owning immutable observation.
The daemon accepts only the currently recorded candidate for this workspace,
generation, operation, and exact base. It performs the trust-store compare-and-swap. The route cannot
trust an arbitrary CID and cannot accept a candidate belonging to a different
alias, base, Plan, branch, or workspace. An empty accepted root is represented
by an absent JSON field, not a magic CID.

## 11. Apply sessions

### 11.1 Session resource

```http
GET /v1/workspace-adapters/workspaces/{workspace_id}/applies/{apply_id}
```

Example:

```json
{
  "apply_id": "apply_...",
  "state": "ready",
  "vault_identity": {
    "installation_id": "install_...",
    "approved_desktop_path": "/Users/alice/Notes/Research",
    "canonical_desktop_path": "/Users/alice/Notes/Research",
    "config_dir": ".obsidian",
    "path_generation": 1
  },
  "source_scan_generation": 41,
  "source_tree_fingerprint": "sha256:...",
  "target_tree_fingerprint": "sha256:...",
  "accepted_root": "bafy...",
  "created_at": "2026-08-16T10:23:00Z",
  "expires_at": "2026-08-17T10:23:00Z",
  "operations": [
    {
      "op_id": "aop_1",
      "kind": "mkdir",
      "path": "notes",
      "expected": {"kind": "missing"},
      "target": {"kind": "directory"}
    },
    {
      "op_id": "aop_2",
      "kind": "write_file",
      "path": "notes/today.md",
      "expected": {"kind": "missing"},
      "target": {"kind": "file", "bytes": 126, "digest": "sha256:..."},
      "content_mode": "text-utf8",
      "object_id": "object_..."
    }
  ]
}
```

Session states are `ready`, `applying`, `confirming`, `confirmed`, `invalid`,
`aborted`, and `expired`. Operations are immutable and ordered. The daemon
rejects duplicate/colliding paths and enforces the fixed ignore policy before
publishing the resource.

Preparation also compares the target with a physical namespace projection
that is not part of the logical tree fingerprint. A target file currently
occupied by a directory, a required parent occupied by a file, or any other
file/directory type obstruction sets workspace state to `blocked` with safe
code `physical_namespace_obstruction` and bounded relative paths. The daemon
publishes no apply session, never removes the obstruction, and requires user
repair plus a fresh scan. Thus an ignored empty folder cannot cause the same
logically valid but physically impossible apply to be regenerated forever.

Operation kinds are:

- `mkdir`: create the missing directory with `Vault.createFolder`;
- `write_file`: fetch and verify an object, then create or update through the
  Vault API;
- `trash_file`: call `FileManager.trashFile` for an exact existing file.

v0 has no permanent-delete or arbitrary-rename operation. A rename in the
target tree is a `write_file` at the new path followed by `trash_file` at the
old path.

`content_mode` is `text-utf8` only when the target bytes round-trip as strict
UTF-8 and the expected entry is either missing or an existing file whose exact
bytes also round-trip as strict UTF-8. Otherwise it is `binary`, including a
non-UTF-8-to-UTF-8 replacement. Directory creation is ordered shallowest-first,
followed by writes and file trashing. Empty directories left after trashing are
ignored by the logical tree and are never removed by v0.

Every `trash_file` carries `requires_user_confirmation: true`. The adapter
MUST display the exact path, recheck its expected length and digest immediately
after confirmation, and never execute it for a scheduled/unattended apply.
`FileManager.trashFile` follows the user's configured trash destination and
does not permanently delete. The contract acknowledges a residual race between
that recheck and trashing; because trash is excluded from the logical tree, a
matching final target does not prove which bytes were moved. Any stable
deletion therefore requires a stronger later primitive. Every `write_file`
that replaces an existing file carries `requires_user_confirmation: true` in
alpha. For binary content confirmation does not close the `modifyBinary` race;
for text it does not turn `Vault.process` into an OS-level CAS against external
writers. Those existing-entry races already require a recoverable quarantine,
atomic compare-and-swap, or equivalent supported primitive for text/binary and
attended/unattended actions. A missing-file precheck has an analogous external-
writer gap: the Vault API does not provide a portable OS-level create-if-absent
transaction, so another process may create the path before the Vault write.
Stable releases therefore MUST disable every `write_file` and `trash_file`
until a supported primitive proves create-if-absent, replacement, and deletion
behavior. This is an apply-wide preflight at `/begin`, not a per-operation
check: if any prepared operation is `write_file` or `trash_file`, stable mode
rejects the whole session before the first Vault API call, including any
preceding `mkdir`, so rejection has zero Vault-side effects. A mkdir-only
session is never emitted because directories are absent from the logical tree.
Alpha implementations recheck missing immediately before create, abort on an
observed same-path event, and disclose that this does not close the residual
race.

### 11.2 Begin or resume

```http
POST /v1/workspace-adapters/workspaces/{workspace_id}/applies/{apply_id}/begin
Idempotency-Key: <plugin apply attempt ID>
```

```json
{
  "vault_assertion": {
    "installation_id": "install_...",
    "desktop_path": "/Users/alice/Notes/Research",
    "config_dir": ".obsidian",
    "path_generation": 1
  }
}
```

Before either a fresh begin or a resume returns an operation, stable mode
performs the apply-wide content-mutation gate from section 11.1. Rejection
returns no operation and permits no Vault API call.

For a fresh session the daemon validates that no unrelated workspace mutation
is active, the complete live Vault assertion equals the session identity and
current registry generation, and the current full scan still equals
`source_tree_fingerprint`. The plugin MUST re-read its installation ID,
`FileSystemAdapter.getBasePath()`, and `Vault.configDir`; any change blocks
begin rather than retargeting the session. For resume the daemon MUST NOT
require the original source fingerprint, but it MUST revalidate the same
identity tuple before allowing any remaining operation and then scan
and revalidate the exact physical/logical postcondition of every operation in
the journaled successful prefix. Any drift invalidates the session immediately.
Only after the whole prefix still matches does it evaluate later operations in
order using physical as well as logical state:

- if the next operation's exact postcondition holds, it journals that
  already-applied-but-unacknowledged operation and continues;
- if its exact precondition holds, that operation is the resume point and every
  later operation must still satisfy its precondition; and
- any other or out-of-order state invalidates the session with
  `409 apply_precondition_failed`.

This rule includes `mkdir`'s physical `TFolder` postcondition even though
empty directories are absent from the logical fingerprint. The daemon chooses
the longest continuous valid operation prefix; it never coerces or rolls back
state behind Obsidian.

### 11.3 Fetch object

```http
GET /v1/workspace-adapters/workspaces/{workspace_id}/applies/{apply_id}/objects/{object_id}
```

The object must be referenced by this session and a remaining `write_file`
operation. The daemon validates the `Malt-Vault-Assertion` header before
opening or returning staged bytes. Response headers include:

```http
Content-Type: application/octet-stream
Content-Length: 126
ETag: "object_..."
Digest: sha-256=<base64 standard SHA-256>
```

If ranges are advertised, a valid single `Range: bytes=start-end` returns
`206` and `Content-Range`. The plugin verifies the complete object's declared
hex digest before any Vault write. An object route cannot address a filesystem
path, conflict checkout, unrelated apply, arbitrary CAS block, or remote URL.

### 11.4 Apply preconditions

Immediately before every Vault API side effect, including `mkdir`, the plugin
re-reads its current installation ID, desktop
`FileSystemAdapter.getBasePath()`, and `Vault.configDir` and requires exact
equality with the immutable session tuple and path generation. A mismatch or
Vault lifecycle change aborts without executing the operation. Only then does
it check the exact Vault-relative entry:

- `missing` requires no `TAbstractFile` at the path;
- `directory` requires `TFolder`;
- `file` requires `TFile`, exact byte length, and exact SHA-256 over
  `Vault.readBinary` bytes.

For a missing `text-utf8` target the alpha implementation uses `Vault.create`
after the immediate precheck, but does not treat that call as an atomic
create-if-absent against external writers. For an existing `text-utf8` file it
uses `Vault.process`; inside the callback it strictly
re-encodes the callback's current string, verifies its exact expected length
and SHA-256 again, then returns the already round-tripped target string. It
throws/aborts on mismatch and MUST NOT rely only on the earlier precondition
read. For `binary` it uses `createBinary` when expected is missing or
`modifyBinary` for an existing file; the latter remains covered by the alpha
confirmation and stable-mode prohibition even when the target happens to be
UTF-8. It MUST abort on a precondition mismatch or unexpected same-path event.
It MUST NOT fall back to Node `fs`, shell commands, or daemon filesystem
writes. These mappings describe alpha execution only; stable mode rejects all
`write_file` operations until the required primitive is configured.

### 11.5 Acknowledge operation

```http
POST /v1/workspace-adapters/workspaces/{workspace_id}/applies/{apply_id}/operations/{op_id}/ack
Idempotency-Key: <apply ID + operation ID>
```

```json
{
  "vault_assertion": {
    "installation_id": "install_...",
    "desktop_path": "/Users/alice/Notes/Research",
    "config_dir": ".obsidian",
    "path_generation": 1
  },
  "outcome": "applied",
  "observed_before": {"kind": "missing"},
  "observed_after": {"kind": "file", "bytes": 126, "digest": "sha256:..."}
}
```

Outcomes are `applied`, `precondition_failed`, and `failed`. A failure includes
a bounded diagnostic string but no stack, Vault content, token, or absolute
path. Only `applied` can advance the successful prefix. Before journaling it,
the daemon MUST scan the exact path and validate the immutable target
postcondition; plugin-reported state alone is insufficient. The plugin MUST
wait for that successful ack response before executing the next operation.
`precondition_failed` or `failed` makes the session `invalid`, stops later
operations, and requires a new full scan/sync. An ack is idempotent and never
advances the workspace baseline. The daemon accepts an ack only for the next
operation after the journaled/recovered successful prefix; out-of-order acks
return `409 state_conflict`.

### 11.6 Complete or abort

After every operation is successfully applied, independently validated, and
acknowledged:

```http
POST /v1/workspace-adapters/workspaces/{workspace_id}/applies/{apply_id}/complete
Idempotency-Key: <apply completion ID>
```

```json
{
  "vault_assertion": {
    "installation_id": "install_...",
    "desktop_path": "/Users/alice/Notes/Research",
    "config_dir": ".obsidian",
    "path_generation": 1
  }
}
```

The plugin re-reads the complete live Vault identity; the daemon requires an
exact installation/path/config/generation match before it transitions to
`confirming`, opens the source, runs a complete filtered scan, and returns
`202` with the confirmation operation. Only an exact match with
`target_tree_fingerprint` changes the session to `confirmed`, records history,
and returns the workspace to `idle`. A mismatch changes it to `invalid` and
returns the workspace to `dirty` or `conflict`; staged evidence is retained
according to the recovery policy.

```http
POST /v1/workspace-adapters/workspaces/{workspace_id}/applies/{apply_id}/abort
Idempotency-Key: <apply abort ID>
```

```json
{
  "vault_assertion": {
    "installation_id": "install_...",
    "desktop_path": "/Users/alice/Notes/Research",
    "config_dir": ".obsidian",
    "path_generation": 1
  },
  "reason": "user cancelled"
}
```

accepts the complete matching live Vault assertion plus a bounded reason and
leaves the Vault as-is. A Vault identity mismatch on any route already returns
`409 vault_identity_mismatch` and safely invalidates/blocks the session; the
plugin MUST NOT replay the old assertion merely to make abort succeed. Abort
does not attempt a filesystem rollback behind Obsidian. The next scan determines the actual local
state and a later sync merges or prepares a new session.

## 12. Conflict resources

```http
GET /v1/workspace-adapters/workspaces/{workspace_id}/conflicts/{conflict_id}
```

Example response:

```json
{
  "conflict_id": "conflict_...",
  "operation_id": "op_...",
  "generation": 7,
  "state": "unresolved",
  "roots": {
    "base": "bafy...",
    "local": "bafy...",
    "remote": "bafy..."
  },
  "tree_fingerprints": {
    "base": "sha256:...",
    "local": "sha256:...",
    "remote": "sha256:..."
  },
  "paths": [
    {
      "path": "notes/today.md",
      "kind": "both-modified",
      "base": {"kind": "file", "bytes": 100, "digest": "sha256:..."},
      "local": {"kind": "file", "bytes": 110, "digest": "sha256:..."},
      "remote": {"kind": "file", "bytes": 126, "digest": "sha256:..."}
    }
  ],
  "allowed_resolutions": ["keep-local", "keep-remote"],
  "manual_merge_available": false
}
```

`kind` is one of `both-added`, `both-modified`, `delete-modify`, or
`working-tree-changed`. `both-added` has `base: {"kind":"missing"}`.
Logical conflict sides use only `missing` or `file` and omit bytes/digest when
missing. Directories are derived physical containers outside the logical tree;
file/directory namespace collisions are reported separately as
`physical_namespace_obstruction`, never as a merge `type-change`.

`tree_fingerprints` always binds the daemon-owned empty/base/local/remote
logical trees for this generation. `roots` contains only available MALT roots.
In a first-join conflict the empty base and unpublished local tree have no MALT
root, so `roots.base` and `roots.local` are omitted; `roots.remote` remains the
accepted observed root. Absence is never a magic CID. v0 returns no plaintext
conflict object, arbitrary object route, or local daemon checkout path.

The initial choices are:

```http
POST /v1/workspace-adapters/workspaces/{workspace_id}/conflicts/{conflict_id}/resolve
Idempotency-Key: <user decision ID>
```

```json
{
  "vault_assertion": {
    "installation_id": "install_...",
    "desktop_path": "/Users/alice/Notes/Research",
    "config_dir": ".obsidian",
    "path_generation": 1
  },
  "resolution": "keep-local",
  "confirmed": true,
  "expected_conflict_generation": 7,
  "expected_base_tree_fingerprint": "sha256:...",
  "expected_local_tree_fingerprint": "sha256:...",
  "expected_remote_tree_fingerprint": "sha256:...",
  "expected_base_root": "bafy...",
  "expected_local_root": "bafy...",
  "expected_remote_root": "bafy..."
}
```

`resolution` is `keep-local`, `keep-remote`, or `automatic-merge`. The daemon
accepts only choices valid for the current immutable conflict generation.
Expected root fields are omitted when that generation has no corresponding
MALT root; all three expected tree fingerprints and the conflict generation
remain required.
`allowed_resolutions` is the authoritative dynamic set for that generation;
the example omits `automatic-merge` because it is not valid for that conflict.
`keep-remote` requires its remote root to have been accepted under the scoped
candidate route. Resolution remains daemon-owned and produces either a backup
operation or an apply session.

Manual edited merge is intentionally absent from v0. Adding it requires an
explicit content-edit contract that retains base/local/remote evidence and
does not make the plugin a merge engine.

## 13. Event handling during apply

The plugin continues to register and forward Vault events during apply. It may
attach optional correlation data:

```json
{
  "kind": "modify",
  "path": "notes/today.md",
  "during_apply": {"apply_id": "apply_...", "op_id": "aop_2"}
}
```

Correlation is a UI/scheduling hint. Neither side assumes that an event was
caused by the plugin merely because path and time match. Unknown or unrelated
events mark the workspace dirty and can invalidate the session. After apply,
the complete daemon scan is the only authority.

## 14. Recovery and retention

The daemon journals before publishing each of these transitions and retains
their idempotent logical results for the common-values window:

- pairing approved and capability digest activated;
- Gateway branch-writer secret pending, digest grant committed, activated, or
  revoked;
- operation queued/running/waiting/completed;
- apply prepared/begun;
- operation acknowledgement accepted;
- confirmation begun/committed; and
- conflict resolution selected.

On startup the daemon completes its local recovery before returning a mutating
route as ready. It validates every staging path against its owner-only staging
root and every session against the paired Plan/Binding. Unknown or unsafe
journals fail closed and are quarantined; they are never replayed into a Vault.

For a pending branch-writer grant, recovery loads the non-secret journal,
replays its exact idempotency key, matches the returned capability ID, digest,
six-field scope, writer descriptor, and creation/join receipt, then promotes
the pending keyring record. A missing or corrupt pending secret still leaves
the journal able to identify and authentically revoke the committed grant
before explicit reissue; it never recovers a secret from a non-secret receipt.

Confirmed/expired object bytes are deleted according to a bounded retention
policy only after their journal no longer requires them. Diagnostic metadata
must not retain Vault content indefinitely. Capability revocation prevents
future reads immediately even if staging still exists.

## 15. Required conformance cases

A daemon and adapter claiming `workspace-adapter/0` must share fixtures that
cover at least:

1. unsupported contract, content-policy revision, or fixture-digest rejection;
2. expired, rejected, replayed, and secret-mismatched pairing;
3. capability use against another workspace, apply, object, or conflict, plus
   wrong installation ID, moved/renamed desktop base path, changed config
   directory, and stale path generation on every body and GET assertion;
4. missing idempotency keys, duplicate keys with same/different bodies, and
   same-body replay after daemon restart through the advertised retention
   window;
5. dropped, duplicate, reordered, and overflow dirty events, including a
   silently dropped event found by the cadence scan and overdue `idle`
   becoming `stale`;
6. local scan changing during backup and during apply preparation, including
   A to B to A edits and partial reads while constructing the immutable
   plaintext snapshot;
7. one authoritative candidate resource, plus acceptance rejection for stale
   operation/generation/base or wrong exact root;
8. object truncation, range mismatch, and digest mismatch before Vault write;
9. create/update/trash precondition mismatch for file-vs-folder and content;
10. plugin or daemon restart before/after every acknowledgement, including an
    earlier successful-prefix path drifting before resume, the Obsidian Vault
    moving or switching after begin and immediately before each operation, zero
    Vault API side effects on identity mismatch, and failed/precondition-failed
    acks stopping all later operations;
11. completion acknowledgement without matching full target scan;
12. both Go and TypeScript execute the exact
    `0fddd4e451f00585718c14392dbd7bcf4e8f9e804f3ee9aec1f20ea65a3fb6dd`
    fixture and Unicode 17.0.0 normalization tests, including C/F full non-
    Turkic folding, expansion/sigma/dotted-I vectors, all declared Windows
    device/character rules, a lone non-NFC path, an NFC/NFD pair, and an apply
    alias;
13. excluded dot paths, Windows metadata, included symlink rejection, and
    rejection of non-dot, nested, or post-pairing `Vault.configDir` changes;
14. large binary attachment streaming without a JSON/body fallback;
15. concurrent Obsidian edit and external filesystem write during apply;
16. no TCP listener and no generic daemon/root/key/Gateway route mounted on
    the workspace-adapter endpoint;
17. Windows named-pipe and Unix socket owner isolation plus desktop
    cross-build;
18. generic/scheduled backup, daemon/foreground sync, and every conflict
    installer refuse an adapter-backed Plan before publication or a filesystem
    rename transaction, including concurrent enrollment after a scheduler
    loaded the Plan and missing/corrupt/inconsistent registry state;
19. the transition build exposes no enrollment, refuses its offline enable
    while a pre-transition daemon or CLI that already loaded v1 is in flight,
    first holds the v1 `plansPath + ".lock"` across census, lease drain, durable
    PlanStore v1-to-v2 migration, and enablement; an old CLI started between
    census and rename cannot load v1 and either lock-fails or rejects v2 on a
    later open; every PlanStore
    writer shares the coordinator, and same-target/disjoint-source bind,
    generic import, and schedule mutation all reject an adapter Plan;
20. a new adapter uses an explicitly named non-`main` branch; the Gateway
    atomically binds the exact writer contract/epoch and policy digest, the
    daemon fsyncs a pending random secret and journals its digest, scope, and
    idempotency key before either create or join; create/grant replay returns
    the exact receipt bound to that digest; crashes immediately before and
    after pending-keyring fsync, pending-grant journal commit, Gateway commit,
    receipt delivery, receipt journal commit, and keyring promotion all
    recover the same usable grant, while a missing pending secret requires
    revoke-and-reissue; each approved device receives a distinct revocable
    capability scoped to the exact authorization principal, Bucket, immutable
    branch, contract, epoch, and issuing device credential; cross-principal,
    cross-Bucket, cross-ref, and cross-device capability reuse plus a real v1
    client with ordinary Bucket writer role are rejected before candidate,
    idempotency, conflict, merge, or ref mutation on fast-forward and stale-
    head pushes;
21. provisional enrollment preserves a non-empty local tree, imports the
    remote Plan/Binding identity only from the marked version 2 manifest after
    exact root acceptance, creates new IDs only from the journaled
    create-if-absent branch receipt, never treats transport/authorization/
    missing-head/proof/decryption/manifest errors as empty, rejects generic
    version 1, marker mismatch, and multi-Binding manifests, converges byte-
    identical same-path additions, never publishes raw local content when
    joining an existing branch, publishes nothing on first-join conflict,
    publishes only a clean merged candidate against the exact accepted root,
    and represents differing same-path additions as `both-added` with a
    missing base entry, no magic base/local root, and exact tree fingerprints;
22. adapter fingerprints ignore filesystem metadata and empty folders, and no
    apply session can request folder deletion;
23. a target file colliding with an ignored empty folder, or a required parent
    colliding with a file, becomes `physical_namespace_obstruction` without an
    apply-regeneration loop;
24. stable mode refuses an entire apply containing any write or deletion,
    including a missing-target create racing an external writer, before the
    first Vault API call or preceding `mkdir`; the configured primitive must
    prove create-if-absent, replacement, and deletion behavior; and
25. a non-UTF-8 existing file with a UTF-8 target remains `binary`, while a
    text update rechecks the exact expected byte digest inside
    `Vault.process`.

The final Product E2E must also prove that authenticated remote content is
ProofList/CID verified before decryption, local state is preserved before
remote observation, every remote adapter publication is rejected without the
exact Gateway branch-writer contract/capability, and the generic directory
Backup Plan continues using its existing crash-safe filesystem installer
unchanged.
