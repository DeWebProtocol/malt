# Evaluation

This page summarizes the benchmark methods and headline results for MALT's core
performance claims. Detailed methodology, full result tables, and paper figures
belong in the research documents workspace.

MALT's performance claim is narrow:

```text
MALT replaces ancestor-propagating Merkle-DAG structural rewrites with explicit,
verifiable structure maintenance, while keeping authenticated reads practical.
```

## Benchmarks

| Question | Suite | Comparison | Signal |
|---|---|---|---|
| Does lookup latency grow with path depth? | `read_matrix` | `maltflat` vs `merkledag` | latency vs path depth under the same CAS latency |
| How does flat authenticated lookup scale with key count? | `flat_index_cardinality` | `maltflat` vs `flathamt` | total read latency and MALT `prove_elapsed_ns` |
| How much structural data is rewritten during real updates? | `write_trace` | `maltflat` vs `merkledag` vs `hamt` | cumulative write amplification |

Supporting suites such as `cas_model`, `proof_overhead`, and
`storage_overhead` validate implementation details. They are not the main
performance story.

## Headline Results

Current checkpoint results are from the 2026-06-30 evaluation package in the
paper workspace. They are useful for understanding the current implementation
shape; final paper numbers should be regenerated from locked full-history
workloads.

Read path depth:

- `maltflat` stays nearly constant as path depth increases because the complete
  path is authenticated as a flat key.
- `merkledag` grows with one additional serial CAS fetch per path edge.
- At 200 ms CAS latency, the checkpoint showed `maltflat` around 282-285 ms
  across depths 1-6, while `merkledag` grew from about 401 ms at depth 1 to
  about 1403 ms at depth 6.

Flat key count:

- MALT proof/open cost grows with committed radix depth, not logical path
  depth.
- In the checkpoint, MALT `prove_elapsed_ns` was about 70-72 ms for 1-64 keys
  and about 142-144 ms for 256-16384 keys.
- Flat HAMT latency follows CAS-get bands as HAMT depth grows.

Write trace:

```text
cumulative_write_amplification =
  physical_persisted_bytes / logical_changed_payload_bytes
```

Checkpoint aggregate over the default repository set with a 200-commit cap:

| system | cumulative WA | metadata bytes |
|---|---:|---:|
| `maltflat` | 1.013 | 1,019,255 |
| `merkledag` | 1.302 | 33,600,645 |
| `hamt` | 1.294 | 32,613,177 |

The 200-commit cap was an intermediate checkpoint. The primary Git replay
method is full first-parent history replay for each repository. Use a commit
cap only for smoke tests or focused debugging.

## Running

Build:

```bash
mkdir -p bin
go build -buildvcs=false -o bin/malt-eval ./cmd/eval/malt-eval
```

Run the current paper-oriented plan:

```bash
bin/malt-eval run --plan examples/eval-paper-plan.json
```

Run the write-trace plan:

```bash
bin/malt-eval run --plan examples/eval-write-trace-plan.json --resume
```

In `write_trace`, omitting `max_commits_per_repo` means replay all selected
first-parent commits. A positive value is a cap. The checked-in full-history
plans use the Badger store backend so replay state does not have to remain in
process memory. The suite runs each `repository + system` pair as an independent
task, writes per-task checkpoints under `output/<run_id>/write-checkpoints/`,
and can resume an interrupted run without duplicating existing raw records.
`jobs` controls task parallelism; the checked-in plans keep `jobs: 1` to minimize
memory pressure during full-history replay.

Outputs:

```text
result/<run_id>/manifest.json
result/<run_id>/raw/*.jsonl
result/<run_id>/aggregate/*.csv
result/<run_id>/summary/*.csv
output/<run_id>/
```

`result/` contains durable benchmark artifacts. `output/` contains disposable
workspace state and must not be committed.

## Schemas

Evaluator schemas live in `cmd/eval/schemas`:

```bash
bin/malt-eval schema
bin/malt-eval schema --name read-matrix-result
bin/malt-eval schema --name flat-index-cardinality-result
bin/malt-eval schema --name write-trace-result
```

When result fields change, update the schema and focused tests in the same
change.

## Boundary

Keep this repository focused on executable benchmark methods, commands, result
schemas, and headline implementation results. Keep detailed experiment
narrative, figure interpretation, limitations, and paper-specific ordering in
the documents repository.
