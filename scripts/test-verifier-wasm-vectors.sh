#!/usr/bin/env sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/malt-wasm-vectors.XXXXXX")
trap 'rm -rf "$work_dir"' EXIT HUP INT TERM

sh "$repo_root/scripts/build-verifier-wasm.sh" "$work_dir/verifier"
(
  cd "$repo_root"
  MALT_VERIFIER_WASM_MAP_PROOF_FIXTURE_OUT="$work_dir/map-proof-fixtures.json" \
    go test ./cmd/malt-verifier-wasm \
    -run '^TestGenerateMapProofWASMFixtures$' \
    -count=1
)
node "$repo_root/scripts/run-verifier-wasm-vectors.mjs" \
  "$work_dir/verifier/malt-verifier.wasm" \
  "$work_dir/verifier/wasm_exec.js" \
  "$repo_root/conformance/resolve-read/v2/vectors.json" \
  all \
  "$work_dir/map-proof-fixtures.json"
node "$repo_root/scripts/run-verifier-wasm-vectors.mjs" \
  "$work_dir/verifier/malt-verifier.wasm" \
  "$work_dir/verifier/wasm_exec.js" \
  "$repo_root/conformance/resolve-read/v2/vectors.json" \
  kzg \
  "$work_dir/map-proof-fixtures.json"
node "$repo_root/scripts/run-verifier-wasm-vectors.mjs" \
  "$work_dir/verifier/malt-verifier.wasm" \
  "$work_dir/verifier/wasm_exec.js" \
  "$repo_root/conformance/resolve-read/v2/vectors.json" \
  ipa \
  "$work_dir/map-proof-fixtures.json"
