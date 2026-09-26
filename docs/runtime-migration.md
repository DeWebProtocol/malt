# Local runtime architecture migration

The active source uses the [typed authentication path](typed-authentication-migration.md).
The initial local-runtime migration plan is historical: its former Resolve/Read,
Map/List and client-root interfaces are deleted, and its dependency table is
not a current build contract. The original plan is available in
[immutable repository history](https://github.com/DeWebProtocol/malt/blob/ab962c1d1a806b773f8de329b5478617e84c9bb3/docs/runtime-migration.md).

## Current boundaries

| Owner | Responsibility |
| --- | --- |
| `malt-core` | Typed inputs, coordinate authentication tree, engine, explicit traversal, retained writer, wire schemas and verification |
| Gateway | Untrusted query execution, persistent ArcTable/KV/CAS, exact atomic batches, authorization and publication |
| `transport` | Untrusted native authentication and CAS capabilities; no trust policy |
| `trust` | Separate accepted, candidate and observed roots; explicit promotion |
| `unixfs` | Flat/hybrid/rooted application layouts, manifests, typed planning and payload/range binding |
| `application` | Shared backup, sync, root selection and candidate-only write-back workflows |
| `internal/runtime` | Reusable composition of local services and transports |
| `cmd/malt` | CLI and daemon entrypoints |
| `merkledag` | Isolated IPFS-compatible import and CID/link replay |
| `tools/evaluation` | Private measured workers, separate from production policy |

The runtime's Go module namespace remains `github.com/dewebprotocol/malt-client`
until its separately governed module migration. The module pins reviewed Core
source by an immutable Go pseudo-version. It does not claim a published release
or change the frozen evaluator baselines.

## Preserved behavior

The typed migration preserves all three application layouts, complete ordered
write batches, retry identity, exact durable receipt checks, payload CID and
range verification, encrypted UnixFS, local staging, and accepted-root fences.
A materialization receipt or successful HTTP request never accepts a root.
Historical Core readers, optional-interface compatibility adapters and old
wire entrypoints are removed. Persistent application data and the encrypted
UnixFS profile remain under their own runtime contracts.

See [the Go API](go-api.md), [encrypted UnixFS](encrypted-unixfs.md), and the
[v0.0.5 ownership ledger](v0.0.5-parity.md) for their respective boundaries.

Current runtime simplification keeps one structured trust model and accepts
only current state schemas. `internal/runtime` binds content commands and
mounts to the same Bucket layout and gateway credentials. `unixfs` shares one
directory-binding projection between staged materialization and typed mutation
planning; `unixfs/model` owns one manifest representation and codec.
`application/backup` separates backup, sync, restore, and manifest policy from
the local installation transaction engine, whose dependencies are plan identity
and filesystem state. Platform locking is shared through `internal/filelock`.
