# Threat Model

MALT core protects graph-relation integrity relative to a caller-selected
root. It does not provide availability, confidentiality, authorization, or
freshness.

## Trusted inputs

- an accepted root selected by client/application policy;
- verifier code and cryptographic parameters;
- the exact operation request independently constructed or checked by the
  client, including the canonical key for a Map-proof request;
- application policy deciding which root is authorized and current.

## Untrusted components

- gateways, executors, and remote diagnostic verifiers;
- ArcTable/materializer implementations and caches;
- CAS/object-store responses;
- network transport, serialized results, result presence/target fields, and
  ProofLists;
- any root, key, or request envelope selected or echoed by an untrusted service
  unless independently matched to client policy;
- service-supplied complete UpdateViews and imported Map materialization before
  local validation;
- candidate roots and mutation or materialization receipts returned by a
  service.

These components may affect latency and availability. They must not cause a
correct local verifier to accept the wrong result for the trusted request.

## Security properties

For a trusted root and request, MALT aims to provide:

- typed map/list relation integrity;
- complete root-to-target resolve binding;
- primitive map/list read binding;
- exact keyed Map membership or non-membership binding, including consistency
  between the presence state, optional target, and proof-step kind;
- ordered ProofList and commitment-proof verification;
- rejection of cross-root, cross-key, cross-query, cross-kind, presence-state,
  target, or reordered evidence;
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
- stable pre-v1 source or wire compatibility;
- List-index, graph-path, object, payload-byte, or remote-data absence inferred
  from a Map non-membership proof.

## Attack cases

### Incorrect target or spliced proof

The verifier binds the returned target, ProofList root/query, ordered step
continuity, operation kind, and caller request. A valid proof for another root,
query, primitive read, or target must be rejected.

### Map-proof request relabeling

For Map-proof verification, the accepted root and exact canonical key are
trusted inputs chosen independently by the caller. `present`, the optional
target, and the ProofList are untrusted. `VerifyMapProof` binds those returned
values and the cryptographic evidence to the caller's root and key. Relabeling
valid evidence to another root or key, toggling the presence state, or
attaching a target to an absence result must be rejected.

Passing a gateway-authored request/result envelope to a local verifier without
first matching its request to the client's accepted root and intended key does
not authenticate the relation the client intended to query.

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

### Client-root bundle and receipt

The client-root surface included in v0.0.7-rc.1 is experimental. Local
computation derives an exact candidate from a validated complete view and
normalized intent, while receipt acceptance may advance only the local writer
session defined by that contract. A bundle or receipt is not a portable
state-transition, publication, freshness, authorization, or trust proof and
does not promote an application's accepted root.
