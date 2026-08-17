# Product commands

This namespace contains only the supported `malt` runtime binary:

```bash
go build -o bin/malt ./cmd/malt
```

Benchmark workers are not product commands. They live under
[`tools/evaluation/cmd`](../tools/evaluation/cmd) so packaging and API reviews
cannot confuse evaluator process adapters with the user-facing CLI.
