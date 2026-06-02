# MALT

[![Go CI](https://github.com/dewebprotocol/malt/actions/workflows/go.yml/badge.svg)](https://github.com/dewebprotocol/malt/actions/workflows/go.yml)

MALT is an authenticated mutable structure layer over immutable
content-addressed storage.

Immutable payload bytes still live in ordinary CAS blocks and keep ordinary
CIDs. MALT adds a verifiable structure layer above those payload CIDs, so a
client can ask for a path or range, receive `result + ProofList`, and verify
that answer against a structure root without trusting the daemon, cache, or
materialized index state.

## Why This Exists

Traditional Merkle-DAG traversal authenticates structure by embedding child
links in parent content. A local structural change can force rootward object
rewrites because the relationship and the object identity are coupled.

MALT separates those concerns:

- payload content remains immutable CAS data
- mutable structure is authenticated by independent structure roots
- list/map semantics define typed read and write operations
- reads return verifier-facing `ProofList` evidence
- local structure updates advance structure roots without rewriting unrelated
  payload blocks

The claim is not that updates are free. The claim is that MALT replaces
implicit ancestor-rewrite cost with explicit, verifiable structure maintenance.

## Current Status

MALT is a research prototype and Go implementation. It is suitable for
experimentation, evaluator work, and design review. It is not yet a production
storage service, and several schemas and policies are intentionally still open.

Current in-tree capabilities:

- root-centric `malt` CLI for local daemon lifecycle, add, resolve, and verify
- proof-bearing HTTP reads for file bytes, directory JSON, and byte ranges
- pure MALT UnixFS-style layout built from map/list semantics and CAS blobs
- stateless commitment backends for semantic proof primitives
- ArcTable-backed structure materialization with overwrite and versioned modes
- `malt-eval` workloads for read queries, write traces, CAS models, proof
  overhead, and storage overhead

Known non-goals for the current prototype:

- no managed global head publication service
- no multi-writer merge or freshness protocol
- no tenant, quota, pinning, or garbage-collection policy
- no stable public API compatibility guarantee yet
- response-body binding for large-file byte ranges is still a ProofList-schema
  design item

## Quick Start

Prerequisites:

- Go 1.24.6 or newer
- Git

Build the two local binaries:

```bash
mkdir -p bin
go build -buildvcs=false -o bin/malt ./cmd/malt
go build -buildvcs=false -o bin/malt-eval ./cmd/eval/malt-eval
```

Initialize the local runtime. The default configuration uses a local embedded
mock CAS at `127.0.0.1:4318` and a daemon API at `127.0.0.1:4317`.

```bash
bin/malt init --non-interactive
bin/malt start
bin/malt status
```

Add a file, resolve it, and verify the returned ProofList:

```bash
printf 'hello malt\n' >/tmp/malt-hello.txt
ROOT=$(bin/malt add /tmp/malt-hello.txt | awk '/Result root:/ {print $3}')
bin/malt resolve "$ROOT" malt-hello.txt >/tmp/malt-resolve.json
bin/malt verify --prooflist /tmp/malt-resolve.json
```

Stop the managed daemon when finished:

```bash
bin/malt stop
```

## Developer Workflow

Run the full Go validation suite from the repository root:

```bash
go test ./...
go vet ./...
```

Inspect the command surfaces:

```bash
bin/malt --help
bin/malt-eval --help
bin/malt-eval schema
```

Run the local smoke evaluation plan:

```bash
bin/malt-eval run --plan examples/eval-smoke-plan.json --run-id smoke
```

The evaluator writes disposable workspace state under `output/<run_id>` and
durable result artifacts under `result/<run_id>`. Those directories are ignored
by git.

## Core Model

MALT's current implementation is easiest to read through these layers:

| Layer | Role |
| --- | --- |
| Semantic layer | Abstract list/map read and write semantics |
| ArcTable | Namespace-scoped arcset persistence and materialization |
| Commitment backend | Stateless proof primitives over semantic representations |
| Graph ports | Resolver read path and writer mutation path |
| Server API | Runtime surface for daemon HTTP routes |
| Application layout | Product data model above list/map/CAS blobs |

The verifier-facing shape is:

```text
Read(root, query) -> result + ProofList
VerifyRead(root, query, result, ProofList) -> valid / invalid

ApplyMutation(baseRoot, semantic mutation) -> newRoot + writeReceipt
```

`list` describes stable-indexed child references. `map` describes authenticated
key-to-target relations and reserves `@payload` as the terminal materialization
binding for map semantic objects. Layouts translate source-domain data into
semantic mutations; they do not define the core semantics.

For a deeper implementation walkthrough, see [ARCHITECTURE.md](./ARCHITECTURE.md).

## Repository Layout

```text
cmd/malt/                      primary runtime CLI
cmd/eval/                      malt-eval workloads, schemas, and helpers
api/http/                      daemon request/response DTOs
auth/                          arcset, commitment, proof, list/map interfaces
graph/                         resolver and writer graph ports
layout/unixfs/                 UnixFS-style layout over map/list/CAS blobs
runtime/                       node, graph, ArcTable, metrics, implementations
sdk/client/                    Go daemon client facade
server/                        daemon HTTP server
storage/                       CAS and KV storage libraries
wire/maltcid/                  MALT map/list root CID codecs
docs/                          public contributor and maintainer docs
examples/                      small runnable plans and examples
```

## Evaluation

`malt-eval` supports both direct commands and a framework runner:

- `malt-eval read` emits read-query JSONL records for MALT and IPLD UnixFS
  baselines
- `malt-eval write` replays Git traces and emits write-amplification JSONL
- `malt-eval run` executes JSON plans and writes `manifest.json`, raw
  envelopes, and summary CSVs under `result/<run_id>`
- `malt-eval schema` lists or prints embedded JSON schemas
- `malt-eval summarize` regenerates summary CSVs from a result directory
- `malt-eval metrics` inspects daemon evaluation metrics

See [docs/evaluation.md](./docs/evaluation.md) for commands, result layout, and
schema notes.

## Roadmap

The near-term roadmap is focused on stabilizing proof schemas, evaluator
outputs, and the UnixFS-style layout boundary before claiming production
readiness. See [ROADMAP.md](./ROADMAP.md).

## Contributing

Contributions are welcome once the repository is public. Start with
[CONTRIBUTING.md](./CONTRIBUTING.md), keep changes small, and include focused
tests for behavior changes.

Please report security issues through the private process in
[SECURITY.md](./SECURITY.md), not through public issues.

## License

MALT is released under the [MIT License](./LICENSE).
