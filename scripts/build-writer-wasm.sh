#!/usr/bin/env sh
set -eu

output_dir=${1:-dist/writer}
mkdir -p "$output_dir"
GOOS=js GOARCH=wasm go build -tags=writer_kzg -buildvcs=false -trimpath -o "$output_dir/malt-writer-kzg.wasm" ./cmd/malt-writer-wasm
GOOS=js GOARCH=wasm go build -tags=writer_ipa,malt_no_default_kzg -buildvcs=false -trimpath -o "$output_dir/malt-writer-ipa.wasm" ./cmd/malt-writer-wasm
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" "$output_dir/wasm_exec.js"
printf '%s\n' \
  "built $output_dir/malt-writer-kzg.wasm" \
  "built $output_dir/malt-writer-ipa.wasm"
