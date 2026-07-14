# Threat Model

MALT core protects graph-relation integrity relative to a caller-selected
root. It does not provide availability, confidentiality, authorization, or
freshness.

## Trusted inputs

- an accepted root selected by client/application policy;
- verifier code and cryptographic parameters;
- the exact resolve/read request independently constructed by the client;
- application policy deciding which root is authorized and current.

## Untrusted components

- gateways, executors, and remote diagnostic verifiers;
- ArcTable/materializer implementations and caches;
- CAS/object-store responses;
- network transport and serialized results;
- candidate roots and mutation receipts returned by a service.

These components may affect latency and availability. They must not cause a
correct local verifier to accept the wrong result for the trusted request.

## Security properties

For a trusted root and request, MALT aims to provide:

- typed map/list relation integrity;
- complete root-to-target resolve binding;
- primitive map/list read binding;
- ordered ProofList and commitment-proof verification;
- rejection of cross-root, cross-query, cross-kind, or reordered evidence;
- payload CID authentication.

Payload CID authentication does not hash arbitrary returned response bytes.
The consuming client must compare full bytes to the target CID, or validate
application-defined range composition against authenticated segment CIDs.

## Non-goals

Core does not guarantee:

- latest-root freshness or rollback prevention;
- mutation state-transition proofs;
- multi-writer merge/conflict policy;
- payload availability, durability, confidentiality, or access control;
- tenant isolation, quotas, pinning, GC, or deployment security;
- ArcTable/KV consistency;
- stable pre-v1 source or wire compatibility.

## Attack cases

### Incorrect target or spliced proof

The verifier binds the returned target, ProofList root/query, ordered step
continuity, operation kind, and caller request. A valid proof for another root,
query, primitive read, or target must be rejected.

### Fabricated materializer state

Materialized state is a proof-generation input, not a trust root. Corruption
should prevent proof generation or produce evidence rejected by the portable
verifier.

### Corrupted CAS bytes

Clients hash fetched bytes against the authenticated CID. Proof verification
alone is insufficient.

### Replayed root

An old root remains cryptographically valid. Freshness requires an external
publication, timestamp, consensus, or application policy.

### Path syntax ambiguity

The profiled resolve contract carries segment arrays. Core validates segments
and uses `/` only for its canonical textual projection. Filesystem, URL,
JavaScript `.`/`[]`, escaping, and dot-segment rules belong to clients and
transports.

### Alternative valid derivation

Resolution is existential. If overlapping arcs permit several complete valid
derivations, the executor may return any one of them. `VerifyResolve` does not
prove longest-prefix maximality or uniqueness. Applications may impose a
preference policy; this is not a proof-soundness requirement.

### Candidate mutation root

A mutation receipt is operational evidence, not a transition proof. Clients
must not automatically promote a gateway-returned root solely because the
receipt names it.
