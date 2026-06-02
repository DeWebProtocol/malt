# Evaluation

`malt-eval` contains the research and benchmarking surface for MALT. It is
separate from the primary `malt` runtime CLI so evaluator compatibility code and
baselines do not leak into the product command surface.

Build the evaluator:

```bash
mkdir -p bin
go build -buildvcs=false -o bin/malt-eval ./cmd/eval/malt-eval
```

Inspect available commands and schemas:

```bash
bin/malt-eval --help
bin/malt-eval schema
bin/malt-eval schema --name run-plan
```

## Smoke Plan

The repository includes a local-only smoke plan:

```bash
bin/malt-eval run --plan examples/eval-smoke-plan.json --run-id smoke
```

This plan exercises:

- `cas_model`
- `proof_overhead`
- `storage_overhead`

It does not require a running daemon or network access.

## Result Layout

`malt-eval run` separates durable results from disposable workspace state:

```text
result/<run_id>/
  manifest.json
  raw/
  summary/

output/<run_id>/
  disposable suite workspaces
```

`result/<run_id>` is the directory to archive when preserving benchmark results.
`output/<run_id>` can be deleted and regenerated.

## Direct Commands

### Read Queries

```bash
bin/malt-eval read --systems merkledag,hamt --iterations 3
```

`maltflat` read-query runs require a configured daemon and seed arcs that include
`@payload`. Without those arcs, the command rejects the run instead of inventing
a root.

Read records follow:

```text
cmd/eval/schemas/readbench-result.schema.json
```

### Git Write Traces

Use an existing local checkout when possible:

```bash
bin/malt-eval write \
  --repo-path /path/to/repo \
  --limit 10 \
  --systems maltflat,merkledag,hamt \
  --out /tmp/malt-write-trace.jsonl
```

For remote replay, use GitHub HTTPS repository URLs:

```bash
bin/malt-eval write \
  --repo-url https://github.com/ipfs/kubo \
  --limit 10
```

### Framework Runs

Framework plans are JSON files with at least one suite:

```json
{
  "run_id": "example",
  "suites": [
    {
      "name": "proof_overhead",
      "config": {
        "structures": ["map", "list"],
        "sizes": [1, 8],
        "iterations": 1,
        "commitment": ["ipa"]
      }
    }
  ]
}
```

Registered suite names:

- `write_trace`
- `read_query`
- `cas_model`
- `proof_overhead`
- `storage_overhead`

## Schema Discipline

Evaluator schemas live under `cmd/eval/schemas`. When a record shape changes,
update the schema and add tests in the same PR.

The most important schemas are:

- `run-plan.schema.json`
- `run-manifest.schema.json`
- `readbench-result.schema.json`
- `read-query-result.schema.json`
- `write-trace-result.schema.json`
- `cas-model-result.schema.json`
- `proof-overhead-result.schema.json`
- `storage-overhead-result.schema.json`
