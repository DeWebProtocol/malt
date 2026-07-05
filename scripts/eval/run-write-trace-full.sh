#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

timestamp="${MALT_EVAL_TIMESTAMP:-$(date -u +%Y%m%dT%H%M%SZ)}"
run_id="${MALT_EVAL_RUN_ID:-write-trace-full-${timestamp}}"
eval_root="${MALT_EVAL_ROOT:-/home/ubuntu/malt/eval-results}"
run_dir="${MALT_EVAL_RUN_DIR:-${eval_root}/${run_id}}"
result_dir="${run_dir}/result"
output_dir="${run_dir}/output"
log_file="${run_dir}/run.log"
status_file="${run_dir}/status.txt"
binary="${MALT_EVAL_BINARY:-${repo_root}/bin/malt-eval}"
plan="${MALT_EVAL_PLAN:-${repo_root}/examples/eval-write-trace-plan.json}"

mkdir -p "$run_dir" "$result_dir" "$output_dir"

status="running"
finish_status() {
  code=$?
  if [[ "$status" == "running" ]]; then
    status="failed"
  fi
  {
    printf 'status=%s\n' "$status"
    printf 'exit_code=%s\n' "$code"
    printf 'finished_at_utc=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  } > "$status_file"
  exit "$code"
}
trap finish_status EXIT

exec > >(tee -a "$log_file") 2>&1

{
  printf 'status=running\n'
  printf 'run_id=%s\n' "$run_id"
  printf 'started_at_utc=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf 'run_dir=%s\n' "$run_dir"
  printf 'result_dir=%s\n' "$result_dir"
  printf 'output_dir=%s\n' "$output_dir"
} > "$status_file"

cat > "${run_dir}/command.txt" <<EOF
GOMEMLIMIT=${GOMEMLIMIT:-6GiB} GOGC=${GOGC:-25} GODEBUG=${GODEBUG:-madvdontneed=1} \\
${binary} run \\
  --plan ${plan} \\
  --run-id ${run_id} \\
  --result-dir ${result_dir} \\
  --output-dir ${output_dir} \\
  --resume
EOF

{
  printf 'repo_root=%s\n' "$repo_root"
  printf 'plan=%s\n' "$plan"
  printf 'binary=%s\n' "$binary"
  printf 'run_id=%s\n' "$run_id"
  printf 'run_dir=%s\n' "$run_dir"
  printf 'result_dir=%s\n' "$result_dir"
  printf 'output_dir=%s\n' "$output_dir"
  printf 'GOMEMLIMIT=%s\n' "${GOMEMLIMIT:-6GiB}"
  printf 'GOGC=%s\n' "${GOGC:-25}"
  printf 'GODEBUG=%s\n' "${GODEBUG:-madvdontneed=1}"
  printf 'go=%s\n' "$(go version)"
  printf 'git=%s\n' "$(git --version)"
  printf 'hostname=%s\n' "$(hostname)"
  printf 'uname=%s\n' "$(uname -a)"
} > "${run_dir}/env.txt"

{
  git rev-parse HEAD
  git status --short
} > "${run_dir}/git.txt"

if grep -q '"max_commits_per_repo"' "$plan"; then
  echo "ERROR: ${plan} contains max_commits_per_repo; refusing non-full replay." >&2
  status="failed"
  exit 2
fi

echo "Run directory: ${run_dir}"
echo "Result directory: ${result_dir}"
echo "Output directory: ${output_dir}"
echo "Building malt-eval..."
go build -buildvcs=false -o "$binary" ./cmd/eval/malt-eval

echo "Starting full write_trace replay..."
set +e
env \
  GOMEMLIMIT="${GOMEMLIMIT:-6GiB}" \
  GOGC="${GOGC:-25}" \
  GODEBUG="${GODEBUG:-madvdontneed=1}" \
  "$binary" run \
    --plan "$plan" \
    --run-id "$run_id" \
    --result-dir "$result_dir" \
    --output-dir "$output_dir" \
    --resume
run_code=$?
set -e
if [[ "$run_code" -ne 0 ]]; then
  echo "ERROR: malt-eval exited with ${run_code}." >&2
  status="failed"
  exit "$run_code"
fi

if [[ ! -s "${result_dir}/aggregate/write_trace.csv" ]]; then
  echo "ERROR: aggregate/write_trace.csv was not produced." >&2
  status="failed"
  exit 3
fi

status="success"
echo "Completed full write_trace replay."
echo "Run directory: ${run_dir}"
