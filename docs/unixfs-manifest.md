# UnixFS directory manifests

This format belongs to the MALT local runtime's UnixFS application. It does not
add a node kind, storage kind, or expansion rule to MALT Core.

## V2

Each directory Prefix binds the typed system payload selector to a manifest CID using codec `0x310002`
(`malt-unixfs-directory-manifest-json-v2`). The payload records each immediate
child's UnixFS projection:

```json
{"entries":[{"name":"docs","type":"dir"},{"name":"report.docx","type":"file"}]}
```

`type` controls only UnixFS presentation and traversal. It does not constrain
the child's MALT layout or payload representation. A `file` may target
a Prefix containing a system payload selector and other explicit bindings. Those arcs
remain available to graph applications, but UnixFS does not traverse through
the file as though it were a directory.

A `dir` may be a Prefix whose system payload selector targets the manifest, or it may target
the manifest CID directly. In the direct form the node and payload CID are the
same and there is no payload-binding step; descendant path bindings may still
be retained by an authenticated ancestor Prefix. This permits an empty directory
projection without manufacturing a child Prefix solely to label it as expandable.

V2 uses these canonical encoding rules:

- the document is UTF-8 JSON with exactly one top-level `entries` field;
- every entry has exactly `name` followed by `type`;
- `type` is exactly `dir` or `file`;
- `name` is one lossless UnixFS path segment: it is non-empty, is neither `.`
  nor `..`, and contains no NUL, `/`, or `\`;
- entries are unique and sorted by their names' UTF-8 bytes;
- no insignificant whitespace is emitted;
- printable Unicode is emitted directly, the short JSON escapes are used for
  `"`, `\`, backspace, form feed, newline, carriage return, and tab, and other
  controls use lower-case `\u00xx`.

Readers reject non-canonical V2 bytes after decoding and re-encoding them. No name-only or raw-codec fallback is accepted.

The shared empty V2 manifest CID is:

```text
bagbibrabciqnqankd6353tbtbjpdc4zxf2tk6sr5bdwfqb2epdufvjlah2jgmwa
```

The cross-implementation golden vector above has CID:

```text
bagbibrabciqkfloqxwbi2arag4vedouzjjh4tiwninbyrjp7n5reg5wup7f4fla
```

## Rejected historical encodings

Only codec `0x310002` with canonical V2 bytes is accepted. Name-only V1
manifests, their `0x310001` codec, and the early raw-CID fallback are removed.
The runtime does not infer file/directory type from a target Root layout.
Experimental old trees must be rebuilt from their original application data;
changing a CID prefix does not authenticate a V2 manifest.

The manifest is a lossless payload, so names starting with `@` and boundary
whitespace are preserved. Interactive staging continues to enforce its stricter
canonical path policy at the application entry point. Flat repository imports
use `model.FlatPathLabel`: the empty path maps to `@payload`; a source path
beginning with `@` is escaped with a leading `/`. Other paths retain their full
relative spelling. Only the ArcSet is flat; directory payloads never enumerate
descendants recursively or include child CIDs.

`WriteChunkedFile` stores raw chunks and uses `model.EncodeFileMetadata` for the
CID-bound `malt.unixfs.chunks/1` payload. Its optional `mode` preserves filesystem
permission changes; count remains authenticated Positional metadata. CAS receipts
are checked before the original inputs reach `nodeapi.AuthenticationBuilder`.
