#!/usr/bin/env sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/malt-writer-ts.XXXXXX")
trap 'rm -rf "$work_dir"' EXIT HUP INT TERM

if go list -buildvcs=false -deps -tags=writer_kzg ./cmd/malt-writer-wasm | rg -q '/auth/commitment/ipa$'; then
  printf '%s\n' "KZG commitment artifact unexpectedly links IPA" >&2
  exit 1
fi
if go list -buildvcs=false -deps -tags=writer_ipa,malt_no_default_kzg ./cmd/malt-writer-wasm | rg -q '/auth/commitment/kzg$'; then
  printf '%s\n' "IPA commitment artifact unexpectedly links KZG" >&2
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
for backend in kzg ipa; do
  node "$repo_root/scripts/run-writer-ts-smoke.mjs" \
    "$work_dir/writer/malt-commitment-$backend.wasm" \
    "$work_dir/writer/wasm_exec.js" \
    "$repo_root/cmd/malt-writer-wasm/ts/writer-session.ts" \
    "$work_dir/fixtures.json" \
    "$backend"
done
