# Merkle DAG vs MALT

MALT keeps the useful part of content-addressed storage: immutable payload bytes
can still live in CAS blocks with ordinary CIDs. The difference is that MALT
does not use the Merkle-DAG object chain as the application proof path.

## Short Version

| Concern | Merkle DAG | MALT |
| --- | --- | --- |
| Payload storage | Immutable content-addressed objects | Ordinary immutable CAS payloads |
| Relationship authentication | Links embedded in parent object content | Typed map/list semantics under structure roots |
| Read shape | Traverse linked objects from a root CID | Query `root + path` |
| Proof material | Traversal objects and sibling or linked evidence | Dedicated `ProofList` evidence |
| HTTP content reads | HTTP can transport blocks or gateway responses | Body can be ordinary HTTP content, proof can ride in `X-Malt-ProofList` |
| Update cost | Child-reference changes can propagate rootward | Structure roots advance without rewriting unrelated payload objects |

## Proof Material

In a Merkle DAG, each object CID commits to that object's bytes, including its
links. To verify a path, a client usually needs the linked object chain from
the trusted root to the target. Those intermediate objects are not just
payload; they are evidence for the traversal.

MALT separates the proof from the payload object chain. The verifier checks a
`ProofList` against a trusted MALT root, query, and result:

```text
VerifyRead(root, query, result, ProofList) -> valid / invalid
```

For flat MALT lookups, proof material for each semantic lookup is fixed-size
under the selected commitment backend. A response that intentionally combines
multiple semantic lookups or range segments carries the corresponding proof
steps, but the proof is still a dedicated verifier artifact rather than the
Merkle-DAG traversal objects themselves.

## Direct Root And Path Reads

Merkle-DAG readers normally traverse from object to object. A gateway can hide
that traversal, but then the client must either trust the gateway's answer or
fetch enough evidence to verify the traversal.

MALT's read contract is root-relative:

```text
Read(root, path) -> result + ProofList
```

The application can request the object it wants by path, and the verifier checks
the result against the trusted root.

## HTTP Proof Transport

Merkle DAGs can be transported over HTTP. The difference is what the client
must receive to verify the answer.

MALT content routes can return application data as ordinary HTTP response
bodies and place verification evidence in headers:

```text
GET /{root}/{path}

200 OK
X-Malt-ProofList: <base64url-json>
X-Malt-ProofList-Encoding: base64url-json

<application bytes or directory JSON>
```

That means a gateway, CDN, or cache can serve normal content responses while
the client still verifies `root + path -> result` without trusting that
intermediary.

The exact header behavior is implementation-bound and documented in
[ProofList format](../spec/prooflist-format.md) and
[HTTP API](../spec/http-api.md).

## Rewrite Amplification

In a Merkle DAG, changing a child reference changes the parent object's CID.
If that parent is linked from another parent, the change can propagate toward
the root. This is not a bug; it is how content-addressed identity authenticates
embedded links.

MALT moves mutable relationships into authenticated structure roots. Updating a
relationship advances the relevant MALT root, but unrelated payload objects can
keep their original CIDs.

MALT does not claim that updates are free. It replaces implicit
ancestor-rewrite cost with explicit, verifiable structure maintenance.

## What MALT Does Not Replace

MALT is not a replacement for IPFS, Filecoin, Kubo, S3, or object storage. It
can run above those systems. They provide payload storage and transport; MALT
provides authenticated mutable structure, proof generation, and verification
above payload objects.
