# Evaluation

This folder collects the implementation-bound evaluation surface.

See also the evaluator code under `cmd/eval/`.

## Commands

`malt-eval` supports:

- the `read_matrix` suite for paper-facing fair read comparisons over shared
  logical datasets
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
- read workload labels: `deep_path_lookup`, `small_file_read`, and
  `large_file_range_read`
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
deterministic logical file tree per dataset point, materializes that same source
dataset into MALT, IPLD UnixFS MerkleDAG, and IPLD UnixFS HAMT, then emits one
record per system, dataset scale, path depth, workload, and iteration. Use it
to compare how read latency changes as data scale and lookup depth increase.

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
