# Commitment And Proof Encoding

This document fixes the implementation-bound byte encodings exercised by the
Resolve/Read conformance corpus v1. It complements the typed-root rules in
[CID and wire format](./cid-and-wire-format.md) and the semantic contract in
[ProofList format](./prooflist-format.md).

## Status And Scope

These encodings are experimental. Before the first release, an intentional
wire change may regenerate the checked-in v1 vectors in the same change. Once
the corpus is released, its vectors are immutable conformance inputs and a
byte-level change to an exercised encoding requires a new corpus version. If
the change also makes a `malt.resolve/v0alpha1` or `malt.read/v0alpha1` value
incompatible, it requires a new enclosing protocol profile as well.

Resolve/Read evidence uses single-index openings wrapped in the map or list
semantic envelopes described below. The primitive batch encodings are recorded
for completeness, but they are not inputs to the v1 Resolve/Read verifier and
are not independently locked by this corpus.

## Common Cell And Root Rules

A primitive commitment authenticates an indexed vector of opaque
`commitment.Cell` byte strings. A CID-valued semantic slot is the binary CID
bytes, not its text form. An undefined slot is the empty cell.

Typed MALT roots are CIDv1 values whose `0x30VSBB` multicodec fields select the
MALT wire version, map/list semantic, and KZG/IPA backend suite. Their identity
multihash digest is the raw commitment:

| Backend | Commitment bytes | Maximum primitive vector length |
| --- | ---: | ---: |
| KZG | 48 | 4096 |
| IPA | 32 | 256 |

The backend ID determines the commitment encoding and expected byte length.
Verification must not infer or override the backend from the digest length.

Map radix nodes and list tree nodes use 256 physical slots. Thus IPA's full
vector and the first 256 positions of KZG's vector carry those node cells; all
remaining primitive positions are zero.

## KZG

The current implementation is `auth/commitment/kzg` and uses
`go-kzg-4844` v1.1.0. Its `NewContext4096Secure` supplies the Ethereum KZG
ceremony parameters.

### Cell To Scalar

For each cell:

1. compute SHA-256 over the exact cell bytes;
2. interpret the 32-byte digest as an unsigned big-endian integer;
3. reduce it modulo the BLS12-381 scalar-field modulus
   `0x73eda753299d7d483339d80809a1d80553bda402fffe5bfeffffffff00000001`;
4. serialize the result as exactly 32 big-endian bytes.

The fixed 4096-position blob is zero-filled after the supplied cells.

### Index Domain

Index openings use a 4096-point BLS12-381 scalar-field domain. The
implementation starts from the order-`2^32` root
`10238227357739495823651030575849232062558860180284477541189508159991286009131`,
raises it to `2^(32-log2(4096))`, enumerates successive powers beginning with
one, and bit-reverses the resulting array. Position `i` is opened at domain
point `domain[i]`.

### Single Proof

A KZG index proof is exactly 84 bytes:

| Offset | Size | Encoding |
| ---: | ---: | --- |
| 0 | 48 | compressed KZG proof |
| 48 | 32 | claimed scalar, in the backend scalar serialization |
| 80 | 4 | index as unsigned big-endian `uint32` |

Verification rejects any other length, an index above 4095, a proof-carried
index different from the requested index, or a claimed scalar different from
the SHA-256-derived cell scalar.

### Current Batch Proof

KZG batch proof generation currently concatenates single proofs; it is not a
native KZG multi-opening. Its bytes are a 4-byte unsigned big-endian proof
count followed by that many 84-byte single proofs in caller-supplied index
order. The ordered index and cell arrays are supplied separately to
verification.

## IPA

The current implementation is `auth/commitment/ipa`. It uses the 256-element
configuration from `go-ipa`
v0.0.0-20240724233137-53bbb0ceb27a.

The 256 SRS points are generated deterministically. Starting with counter zero,
hash ASCII `eth_verkle_oct_2021` followed by the counter as big-endian `uint64`,
interpret the digest as an unsigned big-endian integer reduced modulo the
BLS12-381 scalar modulus used as the Bandersnatch base field, serialize it as
32 big-endian bytes, pass that encoding to Banderwagon point decoding, skip
unsuccessful decodes, and continue until 256 points have been accepted. The IPA
auxiliary point `Q` is the dependency's `banderwagon.Generator`.

### Cell To Scalar

For each cell, compute SHA-256 over the exact bytes, interpret the digest as an
unsigned big-endian integer, and reduce it modulo the Bandersnatch scalar-field
modulus
`13108968793781547619861935127046491459309155893440570251786403306729687672801`.
The fixed 256-element vector is zero-filled after the supplied cells. The
commitment is the multiscalar multiplication of that vector by the ordered SRS;
commitment bytes are the 32-byte `banderwagon.Element.Bytes()` result.

### Single Proof

Single openings use transcript label `malt-ipa` and the field element created
from the requested integer index as the evaluation point. The proof encoding
is:

| Order | Size | Encoding |
| --- | ---: | --- |
| round count | 4 | unsigned big-endian `uint32` |
| `L[0..rounds)` | `32 * rounds` | each `banderwagon.Element.Bytes()` |
| `R[0..rounds)` | `32 * rounds` | each `banderwagon.Element.Bytes()` |
| `A_scalar` | 32 | little-endian field element |
| index | 4 | unsigned big-endian `uint32` |

For the current 256-element vector, there are eight rounds and the proof is
552 bytes. Verification requires the proof-carried index to equal the requested
index.

### Current Batch Proof

Batch openings use transcript label `malt-ipa-batch`. The locked `go-ipa`
`MultiProof.Write` encoding is 576 bytes: the 32-byte `D` point, eight 32-byte
`L` points, eight 32-byte `R` points, and a 32-byte little-endian `A_scalar`.
It contains no count or index list; the ordered indices and expected cells are
separate verifier inputs.

## Radix Map Semantic Proof

Both resolve map steps and primitive map reads carry the same UTF-8 JSON proof
envelope. Every byte-valued field below uses standard padded base64 when the
envelope itself is JSON-encoded:

```json
{
  "steps": [
    {"slot": "<CID bytes>", "proof": "<primitive single proof>"}
  ],
  "bucket": {"proof": "<primitive single proof>"}
}
```

`bucket` is optional. Each radix step uses the next byte of
`SHA-256(canonical_key_utf8)` as its primitive slot index. A `slot` is either an
intermediate root marker, a terminal leaf marker, or a bucket-reference marker.
The marker encodings are raw CIDv1 values with identity multihashes over:

- leaf: `malt:map:radix:leaf:v1:` || key-byte-length as big-endian `uint16` ||
  canonical key UTF-8 || target CID bytes;
- bucket reference: `malt:map:radix:bucket:v1:` || bucket-root CID bytes.

A bucket witness opens the expected leaf marker from the bucket commitment;
its primitive proof carries the bucket index. Current map proofs authenticate
membership only.

In a resolve ProofList this envelope is stored in `step.evidence` with
`evidence_kind="explicit"`; the primitive backend is inferred from the typed
`step.from` root. In a primitive map read it is stored in `step.proof` with
`evidence_kind="structure"` and `evidence_backend="map"`.

## Tree List Semantic Proofs

Every committed list node has 256 slots. Slot 0 authenticates node metadata and
slots 1 through 255 authenticate content. Metadata is a raw CIDv1 identity
marker over:

```text
"malt:list:node-meta:"
|| height      as big-endian uint64
|| child_count as big-endian uint64
|| total_size  as big-endian uint64
|| chunk_size  as big-endian uint64
```

`chunk_size == 0` denotes a plain list node and requires `total_size == 0`.
A positive `chunk_size` denotes fixed-width measured metadata.

### Index Envelope

```json
{
  "metadata_proof": "<primitive single proof>",
  "metadata_target": "<optional metadata-marker CID bytes>",
  "steps": [
    {
      "target": "<CID bytes>",
      "proof": "<primitive single proof>",
      "slot": 1
    }
  ]
}
```

`metadata_target` is omitted when plain metadata can be reconstructed from the
authenticated length. `slot` is the physical content slot and is emitted by
the current prover. Each intermediate target is the next list-node root; the
last target is the queried child CID.

This envelope is stored in `step.proof` with
`evidence_kind="structure"` and `evidence_backend="list"`.

### Fixed-Width Range Envelope

```json
{
  "metadata_proof": "<primitive single proof>",
  "index_proofs": [
    {"index": 0, "proof": "<complete index-envelope bytes>"}
  ]
}
```

The outer ProofList step supplies the authenticated `child_count`,
`total_size`, `chunk_size`, ordered segment CIDs, and requested bounds. Each
`index_proofs[].proof` is itself a complete index-envelope JSON byte string.
The range envelope is stored in `step.proof` with
`evidence_kind="structure"` and `evidence_backend="measured_list"`.

Only the current fixed-width measured list is covered. Variable-size measured
evidence is proposal-stage and has no v1 conformance encoding.

## JSON Projection

ProofList CID fields serialize as IPLD-link objects such as
`{"/":"<cid>"}`. Go `[]byte` fields serialize as standard padded base64
strings. Implementations must treat decoded proof bytes, CID bytes, segment
order, integer widths, and optional-field presence as verifier inputs; error
message text is not part of conformance.
