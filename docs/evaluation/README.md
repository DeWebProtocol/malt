# Evaluation

This folder collects the implementation-bound evaluation surface.

See also the evaluator code under `cmd/eval/`.

## Commands

`malt-eval` supports:

- the `read_matrix` suite for paper-facing fair resolve-path comparisons over
  shared logical paths
- `malt-eval read` for paper-facing read benchmark records
- `malt-eval write` for write-trace replay records
- `malt-eval run` for JSON run plans and durable run directories
- `malt-eval summarize` for summary CSV regeneration
- `malt-eval schema` for embedded JSON schema listing and printing
- `malt-eval metrics` for daemon runtime counters

## Artifacts

The framework runner writes durable results under `result/<run_id>` and
disposable workspace state under `output/<run_id>`.

Current machine-readable schemas live in `cmd/eval/schemas`:

- `run-plan.schema.json`
- `run-manifest.schema.json`
- `common-record.schema.json`
- `read-matrix-result.schema.json`
- `readbench-result.schema.json`
- `read-query-result.schema.json`
- `write-trace-result.schema.json`
- `cas-model-result.schema.json`
- `proof-overhead-result.schema.json`
- `storage-overhead-result.schema.json`

When a record shape changes, update the matching schema and focused schema or
normalization tests in the same PR.

## Benchmark Reporting

Paper-facing reports should keep these dimensions separate:

- payload bytes
- read dataset metadata: `dataset`, `file_count`, `directory_count`,
  `path_count`, `path_depth`, and `logical_payload_bytes`
- read workload labels, with `deep_path_lookup` used for the main
  path-depth matrix
- configured per-CAS-Get latency (`cas_latency_ms`)
- read-path latency (`elapsed_ns`)
- MALT client ProofList verification latency (`verify_elapsed_ns`)
- proof bytes
- ProofList step counts
- comparable baseline evidence items, such as CAS block fetches
- CAS operation counters
- ArcTable operation counters
- writer receipt counts
- summary CSV fields used in figures

Do not count unverifiable helper metadata as proof evidence. Do not present
writer receipt counts as correctness proofs. Receipts and metrics are
operational accounting unless tied to verifier evidence.

The `read_matrix` suite is the main paper-facing read comparison. It builds one
deterministic logical lookup tree and materializes the same paths as:

- `maltflat`: flat MALT authenticated arcs keyed by the complete logical path
- `merkledag`: IPLD UnixFS basic directory traversal
- `hamt`: IPLD UnixFS HAMT directory traversal
- `flathamt`: one IPFS/Boxo HAMT map keyed by the complete logical path

It emits one `resolve_path` record per system, path depth, path sample,
configured CAS latency, and iteration. Use it to compare whether lookup latency
grows with path depth under warm per-block CAS latency models. The `maltflat`
and `flathamt` records include flat path lookup plus one target blob fetch from
CAS; the IPLD UnixFS baselines include their serial directory traversal and
terminal target-node fetches.

In `read_matrix`, `path_depth` means the number of edges from the root directory
to the target file. Therefore depth `1` is a file directly under the root,
depth `2` is one directory plus a file, and so on. It is not the number of
directory components.

The suite writes raw records to `raw/read_matrix.jsonl` and a figure-facing
aggregate to `aggregate/read_matrix.csv`. Use the aggregate's median and p95
columns for plots; raw records remain the audit trail for per-sample behavior.
Set `paths_per_depth` above `1` for the main paper run so that key-dependent
radix/HAMT artifacts do not dominate a single depth point.

Use depths `[1, 2, 3, 4, 5, 6]` for the main paper figure. A sweep up to depth
10 is acceptable for a sensitivity appendix, but depth 16 should be treated as
an artificial stress case rather than the default dataset shape.

Use `[0, 25, 50, 100, 200]` milliseconds as the primary
`cas_latency_ms` sweep. These values model local execution, near-region managed
CAS, low-latency public gateway access, typical public internet access, and
poor/far public gateway access. A separate `2100` millisecond stress run can be
used for cold IPFS DHT/provider-discovery behavior, but it should be reported
separately from the main warm-CAS figure because it is not a normal per-block
steady-state latency.

Report the read matrix from two views:

- fixed CAS latency: plot path depth on the x-axis, total elapsed time on the
  y-axis, and one line per system; use median latency as the main line and p95
  as optional error bars or a separate table; use two or three latency scenarios
  such as `25`, `200`, and optionally `2100` ms
- fixed path depth: hold depth at a representative value such as `4`, plot CAS
  latency on the x-axis, total elapsed time on the y-axis, and one line per
  system; again report median first and p95 as tail latency

The read matrix intentionally excludes full-content reads, range reads,
list-backed payloads, and data-size/proof-generation sweeps. Dataset size effects
on flat MALT proof generation belong in a separate microbenchmark.

`malt-eval read` and the `read_query` suite remain useful for daemon-oriented
end-to-end checks. They exercise the HTTP daemon path for MALT and local IPLD
baselines, so they should not be mixed into the same aggregate as `read_matrix`
core benchmark results. For MALT records, `verify_elapsed_ns` measures
client-side verification of returned ProofList evidence; baseline records omit
it and use CAS block fetches as the comparable evidence-item count.

Historical smoke artifacts that predate the current schemas should be labeled
legacy rather than silently mixed into paper-facing aggregates.

## Related Proposals

[MIP-1009](../mips/mip-1009-benchmark-proof-reporting.md) tracks remaining
decisions about stable benchmark-facing proof, receipt, and metric reporting.
