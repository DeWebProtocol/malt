# Product commands

This namespace contains only the supported `malt` runtime binary:

```bash
go build -o bin/malt ./cmd/malt
```

Benchmark workers are not product commands. They live in
[`malt-evaluation/adapters/node/cmd`](https://github.com/DeWebProtocol/malt-evaluation/tree/main/adapters/node/cmd)
and call public node/runtime capabilities.
