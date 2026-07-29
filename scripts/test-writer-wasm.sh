#!/usr/bin/env sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/malt-writer-wasm.XXXXXX")
trap 'rm -rf "$work_dir"' EXIT HUP INT TERM

if go list -buildvcs=false -deps -tags=writer_kzg ./cmd/malt-writer-wasm | rg -q '/auth/commitment/ipa$'; then
  printf '%s\n' "KZG writer unexpectedly links the IPA backend" >&2
  exit 1
fi
if go list -buildvcs=false -deps -tags=writer_ipa,malt_no_default_kzg ./cmd/malt-writer-wasm | rg -q '/auth/commitment/kzg$'; then
  printf '%s\n' "IPA writer unexpectedly links the KZG backend" >&2
  exit 1
fi

sh "$repo_root/scripts/build-writer-wasm.sh" "$work_dir/writer"
(
  cd "$repo_root"
  MALT_WRITER_WASM_FIXTURE_OUT="$work_dir/fixtures.json" \
    go test ./cmd/malt-writer-wasm \
    -run '^TestGenerateWASMFixtures$' \
    -count=1
)
node "$repo_root/scripts/run-writer-wasm-smoke.mjs" \
  "$work_dir/writer/malt-writer-kzg.wasm" \
  "$work_dir/writer/wasm_exec.js" \
  "$work_dir/fixtures.json" \
  kzg
node "$repo_root/scripts/run-writer-wasm-smoke.mjs" \
  "$work_dir/writer/malt-writer-ipa.wasm" \
  "$work_dir/writer/wasm_exec.js" \
  "$work_dir/fixtures.json" \
  ipa
