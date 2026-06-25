# Evaluation

This folder collects the implementation-bound evaluation surface.

See also the evaluator code under `cmd/eval/`.

## Commands

`malt-eval` supports:

- `malt-eval read` for read-query records
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

Historical smoke artifacts that predate the current schemas should be labeled
legacy rather than silently mixed into paper-facing aggregates.

## Related Proposals

[MIP-1009](../mips/mip-1009-benchmark-proof-reporting.md) tracks remaining
decisions about stable benchmark-facing proof, receipt, and metric reporting.
