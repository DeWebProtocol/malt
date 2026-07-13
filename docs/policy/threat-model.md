# Threat Model

MALT is an experimental reference implementation. It is runnable end to end,
but public APIs, ProofList schemas, wire formats, and deployment policies may
change. It is not production-ready.

This document describes the core correctness boundary for the current MALT
implementation. It does not define production availability, confidentiality,
access-control, or multi-tenant service guarantees.

## Trusted Inputs

MALT verification starts from a small set of trusted inputs:

- an accepted MALT root selected by the caller or application policy
- verifier code and configured cryptographic parameters
- the application policy that decides which root is current, fresh, or
  authorized

The root is the correctness handle. A daemon, gateway, cache, or materialized
index can help retrieve data and proofs, but it does not choose the trusted
root for the verifier.

## Untrusted Components

The following components are treated as untrusted for correctness:

- gateways and remote daemon instances
- CAS backends and remote object stores
- ArcTable materializations
- local or remote caches
- network responses
- remote IPFS or Filecoin infrastructure
- resolver adapters and other runtime acceleration state

Untrusted components can affect latency, availability, or cost. They should not
be able to make a verifier accept an incorrect target for an accepted root and
query when the verifier checks the returned evidence correctly.

## Security Properties

For an accepted root and query, MALT aims to provide:

- object-content integrity through ordinary CAS CIDs
- relationship and path-binding integrity through authenticated list/map
  semantics
- proof soundness for verifier-facing ProofLists
- target binding from the queried root to the resolved target
- detection of corrupted CAS responses when payload bytes do not match the
  claimed CID

For HTTP content reads, the daemon returns body bytes together with
`X-Malt-ProofList` evidence by default. Large-file byte-range reads include
measured-list range evidence that authenticates layout metadata and segment
CIDs. Portable ProofList verification does not by itself hash the returned HTTP
body; UnixFS callers must additionally use `sdk/unixfs.VerifyRangeBody` or
an equivalent segment-byte binding before accepting those bytes.

## Not Guaranteed By Core MALT

Core MALT does not currently guarantee:

- freshness of the selected root
- availability of payloads, proofs, or materialized state
- confidentiality of payloads or structure
- access control or authorization
- rollback prevention without an external head-publication policy
- multi-writer conflict resolution or merge policy
- production tenant isolation
- quotas, pinning, or garbage collection
- durability guarantees of the underlying storage backend
- stable API or wire-format compatibility

These properties can be supplied by applications, deployments, or future
services, but they are outside the current core semantic layer.

## Attack Examples

### Gateway Returns An Incorrect Target

An untrusted gateway may return a target CID that is not bound to the queried
path under the accepted root. The verifier should reject the response when the
ProofList does not validate against the root, query, and target.

### ArcTable Returns Fabricated State

ArcTable is materialized runtime state, not a trust root. If it returns stale or
fabricated arcs, proof generation should fail or produce evidence that the
verifier rejects.

### CAS Returns Corrupted Bytes

CAS backends may return bytes that do not match the requested CID. Clients must
verify payload bytes against the CID when accepting content.

### Attacker Replays An Old Root

An old root can still verify cryptographically. MALT core does not decide
freshness. Applications need a head-publication, timestamping, consensus, or
other policy if rollback prevention is required.

### Malformed ProofList

Malformed or incomplete ProofLists should be rejected by validation and
verification code. Changes to ProofList JSON or proof-step semantics are
experimental and should include tests.

### Root-Type Confusion

MALT roots encode map/list and commitment-backend information. Verifiers should
reject roots or proof steps whose type, backend, or expected semantic kind does
not match the query and evidence.

### Path Canonicalization Ambiguity

Path parsing ambiguity can create disagreement between writer, resolver, and
verifier behavior. The profiled artifact API carries canonical segment arrays;
it does not apply filesystem dot-segment or whitespace cleaning. Root-relative
legacy HTTP and UnixFS adapters may reject reserved transport paths before
typed-query construction. Generic map coordinates are not Unix paths.

### Alternative Valid Derivation

If a root authenticates overlapping arcs, an untrusted resolver may choose a
different complete derivation than an application expected. The artifact
verifier proves the returned derivation but does not prove it was longest or
unique. Applications that require one deterministic namespace must enforce an
overlap/conflict policy when constructing or accepting the layout. This is
separate from proof soundness: invalid evidence or a path that does not fully
consume the requested segments is still rejected.
