#!/usr/bin/env sh
set -eu

output_dir=${1:-dist/writer}
mkdir -p "$output_dir"
GOOS=js GOARCH=wasm go build -buildvcs=false -trimpath -o "$output_dir/malt-writer.wasm" ./cmd/malt-writer-wasm
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" "$output_dir/wasm_exec.js"
cp cmd/malt-writer-wasm/browser/malt-writer-worker.mjs "$output_dir/malt-writer-worker.mjs"
cp cmd/malt-writer-wasm/browser/malt-writer-workers.mjs "$output_dir/malt-writer-workers.mjs"
printf '%s\n' \
  "built $output_dir/malt-writer.wasm" \
  "built $output_dir/malt-writer-worker.mjs" \
  "built $output_dir/malt-writer-workers.mjs"
