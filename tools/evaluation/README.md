# MALT runtime evaluation adapters

This directory contains runtime-owned process adapters launched by the separate
[`malt-evaluation`](https://github.com/DeWebProtocol/malt-evaluation)
repository. Keeping an adapter beside the implementation lets it measure and
verify the exact runtime code at a pinned commit.

This directory does not own benchmark suites, campaign plans, comparison
policy, result schemas, result interpretation, or provenance. Those remain in
`malt-evaluation`. The adapters are not supported user commands or a public Go
API, even though their executable names and JSON/JSONL contracts are pinned
campaign inputs.

## Commands

| Command | Runtime-side responsibility |
| --- | --- |
| `malt-eval-machine-descriptor-probe` | Reports bounded machine/runtime evidence consumed by evaluation setup. |
| `malt-eval-rq1-fixture-build` | Builds and verifies the runtime-owned RQ1 route fixture against a disposable Gateway. |
| `malt-eval-rq1-worker` | Measures locally verified selective-CAR and Direct-CAS read paths. |
| `malt-eval-rq2-fixture-build` | Builds and verifies shared KZG/IPA client-root fixtures. |
| `malt-eval-rq2-worker` | Runs the native client-root mutation adapter. |
| `malt-eval-rq2-browser-wasm` | Implements the Go/WASM mutation boundary used by the browser adapter. |
| `malt-eval-rq2-browser-worker` | Drives the browser/WASM process boundary and emits its measurement records. |
| `malt-eval-rq3-malt-worker` | Replays the portable RQ3 mutation stream through the MALT runtime/Gateway path. |
| `malt-eval-rq3-hash-worker` | Replays the RQ3 comparison stream through the runtime-owned hash-system adapter. |

Build the adapters without placing them in the product command namespace:

```bash
go build -buildvcs=false ./tools/evaluation/cmd/...
```

## Private support boundary

Shared adapter code lives under `internal/evaluation`. In particular,
`internal/evaluation/gatewaytransport` owns the disposable-Gateway instance
token, bootstrap authorization, unchecked raw-CAS fetch, selective-CAR fetch,
and evaluation health projection. These capabilities are intentionally absent
from public package `transport`.

Production packages and `cmd/malt` must not import `internal/evaluation`.
Architecture tests enforce that dependency direction and keep `cmd/` reserved
for the product binary. Code under this directory may use public runtime
packages to exercise real product behavior, but production code must never
depend on an evaluator adapter.
