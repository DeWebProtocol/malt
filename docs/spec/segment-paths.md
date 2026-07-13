# Segment Paths And Resolution

## Canonical Path Model

MALT defines an application-neutral path as an ordered array of UTF-8 segments.
Each segment is non-empty and cannot contain the MALT textual separator `/`.
The empty array denotes the caller-supplied root.

```text
["a", "b", "c", "d"] <-> a/b/c/d
```

The array is the canonical API form. `/` is the canonical MALT textual
projection used by current map coordinates and slash-path adapters; it is not a
requirement that every transport expose a slash-delimited string.

Examples:

- HTTP can map URL path components to segments.
- RPC can send a repeated string field directly.
- a TypeScript SDK can map object access syntax such as `user.name` or
  `items[0]` to application-selected segments before calling MALT.

MALT does not parse JavaScript property syntax, filesystem dot-segment rules,
JSON Pointer escaping, or another application's grammar. Values such as `.` and
`..` are ordinary MALT segments; an HTTP/file adapter may reject or escape them
according to its own transport policy.

The module-root `malt.SegmentPath`, `malt.NewSegmentPath`, and
`malt.ParseSegmentPath` implement this contract.

## Arc Selection And Composition

One MALT map arc may consume one or more leading segments. Given the requested
path:

```text
["a", "b", "c", "d"]
```

an execution engine may select the following authenticated derivation:

```text
root1 --"a/b"--> root2 --"c"--> root3 --"d"--> target
```

The reference resolver uses longest-prefix lookup at each root as its candidate
selection strategy. This lets clients submit segments without discovering arc
boundaries first and keeps ArcTable/index lookup outside the trusted kernel.

## Acceptance Semantics

Longest-prefix selection is not a verifier claim. Verification accepts one
complete ordered derivation whose consumed arc coordinates concatenate to the
requested segment path and whose evidence is valid from the trusted root to the
returned target.

The proof does not establish that a longer competing arc was absent. It also
does not establish uniqueness, global optimality, or application preference.
Multiple authenticated derivations are a supported graph feature. If an
application permits overlapping arcs such as `a`, `a/b`, and `a/b/c`, its
layout or client policy decides whether those alternatives are meaningful or a
conflict. In a normal well-formed layout, an unintended alternative derivation
should fail to consume and resolve the remaining segments.

This division keeps the contract small:

- core owns segments, canonical arc projection, proof-carrying composition,
  and verification of the returned derivation;
- execution owns candidate discovery and may prefer longest-prefix lookup;
- applications/clients own their namespace, overlap, and conflict policy;
- transports own URL, RPC, object-syntax, and escaping rules.
