# Compatibility Policy

MALT core is experimental and pre-v1. Exact tags should be pinned and unknown
profiles rejected.

## Compatibility surfaces

| Surface | Status |
| --- | --- |
| Root `malt` typed facade | Experimental |
| `malt.resolve/v0alpha1` | Profiled; incompatible wire revisions require a new profile |
| `malt.read/v0alpha1` | Profiled; incompatible wire revisions require a new profile |
| ProofList JSON and proof semantics | Experimental, verifier-facing |
| Typed MALT root CIDs/codecs | Experimental, verifier-facing |
| `SegmentPath` projection | `/`-joined UTF-8 segments; experimental |
| Public Go semantic/materializer interfaces | Experimental source API |
| `malt.artifact/v0alpha2` | Frozen v0.0.4 compatibility profile |
| ArcTable/KV/CAS implementations | Outside this module and not a core compatibility surface |
| CLI, daemon, HTTP routes, UnixFS | Outside this module |

The frozen artifact profile accepts only its released operation set. New
integrations use operation-specific resolve/read request/result pairs rather
than extending that union.

## Pre-v1 changes

Breaking Go package changes are allowed before v1 but must be explicit in
release notes. Changes to the following require matching tests, schemas, and
documentation in the same PR:

- profile identifiers or serialized request/result fields;
- ProofList fields, ordering, step kinds, or verification rules;
- root/CID encoding and commitment backend selection;
- canonical segment/arc validation;
- mutation or receipt value semantics;
- payload-binding and measured-list evidence.

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
