# Positional chunk metadata

A Positional Root authenticates its element count and an optional payload CID.
Core does not interpret file sizes, chunk widths or application JSON. UnixFS
requires a payload CID for chunked files and uses the following canonical UTF-8
JSON document, encoded as a raw CID:

```json
{"profile":"malt.unixfs.chunks/1","chunk_size":"4","total_size":"8"}
```

Fields occur in the order shown, without whitespace or extra fields. Sizes are
canonical unsigned decimal strings. `chunk_size` must be positive. The count
already authenticated by Core must equal zero for an empty file, otherwise
`1 + (total_size - 1) / chunk_size`. The document does not duplicate count.
`unixfs/model` owns encoding and validation. Identity CIDs may inline the same
bytes; other CIDs are fetched through the injected block reader and hashed
before decoding.

Writers upload this document with the chunk blocks, verify the returned CID,
and pass that CID to `CreatePositionalPayload`. The document consumes no data
index. Readers authenticate the root metadata, verify the JSON CID and count,
translate the requested half-open byte interval into a half-open element-index
interval, and verify that range against the same Root. Every returned chunk is
CID-checked, length-checked, then sliced locally. Empty files retain their
metadata document. Encrypted files use ciphertext sizes in this document and
check them against their authenticated encryption manifest.

A payload CID is also a graph dependency: write plans retain its block or Root,
place referenced candidates before their parents, and include it in dependency
closure. It does not accept a trusted Root or authorize publication.

The structural layout and proof format are defined only by the
[Core specification](../../malt-core/docs/spec/authentication-contracts.md).
