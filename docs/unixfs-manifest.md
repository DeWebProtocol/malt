# UnixFS directory manifests

This format belongs to the MALT local runtime's UnixFS application. It does not
add a node kind, storage kind, or expansion rule to MALT Core.

## V2

Each directory Map binds `@payload` to a manifest CID using codec `0x310002`
(`malt-unixfs-directory-manifest-json-v2`). The payload records each immediate
child's UnixFS projection:

```json
{"entries":[{"name":"docs","type":"dir"},{"name":"report.docx","type":"file"}]}
```

`type` controls only UnixFS presentation and traversal. It does not constrain
the child's MALT semantic kind or payload representation. A `file` may target
a Map containing `@payload`, `@comments`, or other explicit arcs. Those arcs
remain available to graph applications, but UnixFS does not traverse through
the file as though it were a directory.

A `dir` may be a Map whose `@payload` targets the manifest, or it may target
the manifest CID directly. In the direct form the node and payload CID are the
same and there is no payload-binding step; descendant path bindings may still
be retained by an authenticated ancestor Map. This permits an empty directory
projection without manufacturing a child Map solely to label it as expandable.

V2 uses these canonical encoding rules:

- the document is UTF-8 JSON with exactly one top-level `entries` field;
- every entry has exactly `name` followed by `type`;
- `type` is exactly `dir` or `file`;
- `name` is one lossless UnixFS path segment: it is non-empty, is neither `.`
  nor `..`, does not start with the reserved `@` prefix, contains no NUL, `/`,
  or `\`, and has no leading or trailing Unicode whitespace or U+FEFF;
- entries are unique and sorted by their names' UTF-8 bytes;
- no insignificant whitespace is emitted;
- printable Unicode is emitted directly, the short JSON escapes are used for
  `"`, `\`, backspace, form feed, newline, carriage return, and tab, and other
  controls use lower-case `\u00xx`.

Readers reject non-canonical V2 bytes after decoding and re-encoding them. The
codec, rather than a duplicated JSON version field, distinguishes V2 from V1;
this is significant because both versions encode an empty directory as
`{"entries":[]}`.

The shared empty V2 manifest CID is:

```text
bagbibrabciqnqankd6353tbtbjpdc4zxf2tk6sr5bdwfqb2epdufvjlah2jgmwa
```

The cross-implementation golden vector above has CID:

```text
bagbibrabciqkfloqxwbi2arag4vedouzjjh4tiwninbyrjp7n5reg5wup7f4fla
```

## V1 compatibility

V1 uses codec `0x310001` and name-only entries:

```json
{"entries":["docs","report.docx"]}
```

Early native writers also stored those same bytes under the raw codec, so
readers accept raw-CID manifests as V1. V1 has no authenticated projection
field; only while reading V1 does the UnixFS runtime apply its historical rule:
a Map target is a directory, while a List or CAS target is a file.

Readers apply the same lossless UnixFS segment rules to V1 names so historical
bytes cannot collapse into aliases such as `.` or `..` during traversal.
Writers always emit V2. V1 is a read-only compatibility format.
