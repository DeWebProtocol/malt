# Encrypted MALT-native filesystem profile

`malt.encrypted-unixfs/v1` is a runtime-owned application profile for backup,
restore, synchronization, filesystem projection, and authorized browser
inspection. It changes no MALT Core Root, CID, ProofList, mutation, receipt, or
commitment encoding.

The profile stores a directory as authenticated MALT relations instead of a
compressed container. Every plaintext name is carried by an encrypted parent
manifest. The surrounding MALT Map authenticates only a reserved binding or an
opaque lookup token to its target root.

```text
dataset Map
├── @payload -> encrypted DatasetManifest CID
└── binding token -> binding directory Map

directory Map
├── @payload -> encrypted DirectoryManifest CID
└── entry token -> child directory/file Map

regular-file Map
├── @payload -> encrypted FileManifest CID
└── @content -> encrypted raw block or fixed MALT List
```

No tar, gzip, or whole-directory ciphertext object is part of this profile.

## Manifests

The dataset manifest identifies the Bucket dataset, local Plan, branch, display
name, and top-level bindings. A binding contains its stable local ID, display
name, restore path name, and opaque token, but not its target Root.

The directory manifest contains mode, modification time, and a sorted list of
`{name, type, token}` entries. It deliberately omits child targets. Changing a
child target therefore updates an authenticated Map arc without unnecessarily
changing the directory-listing payload.

The file manifest contains mode, modification time, plaintext size, storage
kind, chunk geometry, and—for a symlink—one validated portable relative target.
It never contains plaintext file bytes. Regular file content uses one encrypted
raw block for a single chunk and a fixed MALT List for multiple chunks.

All manifests use strict JSON decoding, reject unknown fields, and use sorted
binding/entry arrays. The JSON is encrypted before CAS storage; it is not a
public enumeration endpoint.

## Opaque lookup tokens

Tokens have the textual form `e1-` followed by unpadded lowercase Base32 of an
HMAC-SHA-256 value. Key derivation is domain-separated by the profile ID,
dataset, branch, binding, and parent path as applicable.

Each KDF input field is encoded as `length:u64be | UTF-8 bytes`; the first two
fields are the profile ID and domain label, followed by the listed context
fields. `opaqueToken(key, value)` is Base32-lowercase of
`HMAC-SHA-256(key, UTF-8(value))`. Implementations must use the labels in
`unixfs/encrypted/profile.go`; changing a label or field order creates a new
application profile rather than a compatible implementation.

| Purpose | KDF domain and following fields |
| --- | --- |
| dataset manifest | `dataset-manifest`, dataset ID, branch |
| binding content base | `binding`, dataset ID, branch, binding ID |
| directory manifest | `directory-manifest`, relative parent path |
| file manifest | `file-manifest`, relative file path |
| file content | `file-content`, relative file path |
| binding-token base | `binding-token`, dataset ID, branch |
| child-token base | `entry-token`, relative parent path |

Directory/file/content keys derive from the current-epoch binding content
base. The binding-token base derives directly from the epoch-1 Bucket namespace
key. A child-token base derives from an epoch-1 binding namespace base using
the same `binding` domain before applying `entry-token`.

Binding tokens are derived from the binding ID. Child tokens are derived from
the plaintext child name and its plaintext parent path. The namespace key is
the Bucket key at epoch 1, while manifest and content encryption uses the
envelope's content-key epoch. Content-key rotation can therefore rewrite
ciphertext without renaming the authenticated namespace.

An untrusted Gateway can resolve a supplied token but cannot recover the name
used to derive it. Enumeration is performed by an authorized consumer:

1. resolve `@payload` under the locally selected directory Root;
2. verify the returned ProofList locally;
3. fetch and verify the encrypted manifest bytes against that payload CID;
4. decrypt the manifest locally;
5. derive or read the listed opaque token for the selected child;
6. resolve that token under the same directory Root and verify its ProofList.

This is equally applicable to the daemon, a host-filesystem adapter, a local
API client, an Obsidian integration, or browser code with an authorized key
provider and a MALT Core verifier. None of those consumers need a Gateway
directory-enumeration route.

## Encryption envelope

Dataset, directory, file, and chunk plaintexts use XChaCha20-Poly1305 with a
fresh 24-byte nonce. The binary envelope is:

```text
"MALTEFS1" | version:u8 | kind:u8 | epoch:u32be | nonce:24 | ciphertext+tag
```

Associated data binds the profile, envelope kind, epoch, and object context.
Dataset manifests bind dataset and branch; directory/file manifests and file chunks
also bind binding ID and relative path; chunk contexts additionally bind the
chunk index. Keys are HMAC-SHA-256 domain-separated from the selected per-Bucket
key epoch.

AAD is `UTF-8(profile) | 0x00 | kind:u8 | epoch:u32be`, followed for each
context field by `length:u32be | UTF-8 bytes`. Dataset context is dataset ID and
branch. Directory/file-manifest context adds binding ID and relative path. File
chunk context adds the decimal chunk index after that relative path.

AEAD authentication is defense in depth, not a substitute for MALT
verification. Every remote read must still start at a locally selected Root,
verify its ProofList, verify the ciphertext CID, and only then decrypt.

## Verified publication

Backup preparation is a local transaction. While holding the selected Plan's
cross-process operation lock, the runtime first removes any ciphertext snapshot
spool left by a crashed or unsuccessfully cleaned prior run, then checks the
workspace for pending/conflicted work. It encrypts changed files into a
Plan-exclusive owner-private local CAS and computes every Map/List Root locally
with the selected MALT Core commitment backend. Normal cleanup errors are
returned to the caller, and the next invocation retries stale-spool recovery.
No ciphertext or graph object is sent to the Gateway during preparation.

After all source fingerprints remain stable, publication uploads the exact
locally CID-bound ciphertext blocks and replays graph objects child before
parent. Every Gateway block CID and Map/List Root must equal the locally
computed value; substitution aborts before Bucket stage or push. Remote
success never accepts the resulting dataset Root. The locally computed Root is
durably recorded as a candidate before Bucket stage/push. A candidate with no
accepted base is an explicit bootstrap candidate and still requires local
`root accept`; it is never silently promoted.

An unchanged binding Root or encrypted dataset-manifest CID is reused only
from a locally accepted and fully verified base dataset. The reused manifest
must exactly match the current Plan metadata. If every binding is rebuilt, the
runtime can publish an independent candidate without trusting payloads or
bindings from the observed base.

Snapshot and restore traversal is rooted in pinned `os.Root` handles. Opened
objects are checked against their pre-open identity, escaping symlinks are
rejected, and the source fingerprint and encryption pass share the same pinned
source Root. Restore staging and final rename use a parent-relative Root plus a
durable random parent pin, so replacing the destination path cannot redirect
writes outside the selected parent. Verified dataset handles retain a private
immutable copy of the proof-bound Root, manifest CID, manifest, and binding
Roots; mutating public compatibility fields cannot change read or reuse
authority.
Because JSON and the KDF use UTF-8 strings, invalid UTF-8 names are rejected
instead of being lossy-normalized into an unreadable profile.

## Range reads

Plaintext files are split into 256 KiB chunks by default. Each chunk is sealed
independently. A range read resolves and verifies the file manifest and content
binding, fetches only the intersecting raw/List ciphertext chunks, verifies
their CIDs, decrypts them locally, and returns only the requested plaintext
slice. Cold data therefore need not be downloaded or decrypted in full.

## Exposure and compatibility

The Gateway observes dataset/account/branch service metadata, reserved binding
names, opaque tokens, graph shape, ciphertext CIDs and sizes, timing, and access
patterns. It does not receive plaintext path names, file bytes, manifests, or
key material. The runtime's default commit message is fixed and contains no
Plan, binding, or path name. A user-supplied commit message is publication
metadata and may disclose whatever plaintext the user puts in it.

This profile replaces the repository's pre-release tar/gzip backup format. The
old decoder is intentionally absent. Before upgrading a branch whose only copy
uses that format, restore it with the previous binary and republish from the
plaintext tree. Local Plan configuration accepts legacy `archive_name` once
and emits `path_name`; this configuration migration does not decode old remote
backup objects. A pending publication journal whose result names the old
profile is left unchanged and rejected before any Gateway status, stage, or
push call; complete or discard it with the previous runtime before upgrading.
Likewise, an interrupted path-based filesystem installation journal from the
pre-release runtime must be recovered with that runtime before this version
continues; new installation journals are parent-pinned format version 2.
