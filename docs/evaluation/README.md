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
deterministic logical lookup tree, materializes the same paths as flat MALT
authenticated arcs, IPLD UnixFS MerkleDAG, and IPLD UnixFS HAMT, then emits one
`resolve_path` record per system, path depth, configured CAS latency, and
iteration. Use it to compare whether lookup latency grows with path depth under
warm per-block CAS latency models.

Use `[0, 25, 50, 100, 200]` milliseconds as the primary
`cas_latency_ms` sweep. These values model local execution, near-region managed
CAS, low-latency public gateway access, typical public internet access, and
poor/far public gateway access. A separate `2100` millisecond stress run can be
used for cold IPFS DHT/provider-discovery behavior, but it should be reported
separately from the main warm-CAS figure because it is not a normal per-block
steady-state latency.

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
