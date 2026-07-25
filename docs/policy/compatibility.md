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

Typed-root decoders accept only the current registered `MALTVersionID`,
semantic IDs, backend suite IDs, and combinations. Earlier experimental codec
allocations are not compatibility inputs and must not be recognized through
fallback mappings.

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
