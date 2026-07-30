# Compatibility Policy

MALT core is experimental and pre-v1. Exact tags should be pinned and unknown
profiles rejected.

## Compatibility surfaces

| Surface | Status |
| --- | --- |
| Root `malt` typed facade | Experimental |
| `malt.resolve/v0alpha1` | Profiled; incompatible wire revisions require a new profile |
| `malt.read/v0alpha1` | Profiled; incompatible wire revisions require a new profile |
| `malt.update-view/v1` | Experimental post-v0.0.6 complete semantic closure |
| `stateful-complete-vectors-v1` | Experimental state profile required by the current update-view contract |
| `malt.semantic-intent/v1` | Experimental post-v0.0.6 output-free update intent |
| `malt.client-root-bundle/v1` | Experimental post-v0.0.6 exact-root submission; not a transition proof |
| `malt.client-root-materialization/v1` | Experimental root-bound map proof-serving witness; not a transition proof |
| `malt.writer-compute-result/v1` | Legacy browser-local bundle and next-view result; decode compatibility only |
| `malt.writer-compute-result/v2` | Experimental browser-local bundle, map materialization, and next-view result |
| `malt.materialization-receipt/v1` | Experimental post-v0.0.6 exact-bundle durability acknowledgement |
| ProofList JSON and proof semantics | Experimental, verifier-facing |
| Typed MALT root CIDs/codecs | Experimental, verifier-facing |
| `SegmentPath` projection | `/`-joined UTF-8 segments; experimental |
| Public Go semantic/materializer interfaces | Experimental source API |
| `sdk/writer` | Experimental source API for local complete-view client-root computation |
| `auth/observation` phases | Diagnostic source API; not wire data or proof evidence |
| `malt.artifact/v0alpha2` | Frozen v0.0.4 compatibility profile |
| ArcTable/KV/CAS implementations | Outside this module and not a core compatibility surface |
| CLI, daemon, HTTP routes, UnixFS | Outside this module |

The frozen artifact profile accepts only its released operation set. New
integrations use operation-specific resolve/read request/result pairs rather
than extending that union.

The `/v1` suffixes on the client-root profiles version those serialized
contracts. They do not mean that MALT, the Go module, or the source APIs have
reached a stable v1 release. The profiles are present on post-v0.0.6 `main` and
are not part of the immutable v0.0.6 tag.

## Pre-v1 changes

Breaking Go package changes are allowed before v1 but must be explicit in
release notes. Changes to the following require matching tests, schemas, and
documentation in the same PR:

- profile identifiers or serialized request/result fields;
- ProofList fields, ordering, step kinds, or verification rules;
- root/CID encoding and commitment backend selection;
- canonical segment/arc validation;
- mutation, client-root, or receipt value semantics;
- payload-binding and measured-list evidence.

Typed-root constructors emit the current registered `MALTVersionID=3`.
Decoders explicitly retain v2 typed roots for read/proof compatibility and
reject every other unknown version, semantic ID, backend suite ID, and
combination; there is no heuristic fallback mapping.

Radix map profiles bind the entire internal tree to one typed-root version and
backend: v2 roots use v1 variable-length collision-bucket references, while v3
roots use v2 fixed-domain references. Readers recognize both profiles but reject
mixed-version internal children or a bucket reference that does not match its
parent profile. Only the v3/fixed-domain profile supports collision-bucket
non-membership proofs. Incremental map/list mutation APIs require the input root
version to match the semantics instance. The client writer handles v2 updates
by exactly replaying the complete v2 view, fully rebuilding it as v3, and
applying changes only to that v3 working root; direct partial v2-to-v3 mutation
is rejected so unchanged v2 child CIDs cannot leak into a v3 root.

v0.0.6 intentionally removes application and deployment packages from this
module. Consumers of the former CLI/daemon/UnixFS/server/storage packages must
use `malt-client` or `gateway`; no forwarding packages are provided.

Payload CIDs remain governed by the selected CAS/CID rules. MALT proof
verification authenticates the CID relation, while the consuming client is
responsible for hashing returned bytes.

## Release notes

Every source release records:

- exact commit;
- verifier/profile/schema changes;
- Go source compatibility changes;
- reproducible test, vet, build, and WASM checks;
- known verification limitations.

Treat `main` as an integration branch, not a stable dependency.
