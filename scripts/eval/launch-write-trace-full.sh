#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

timestamp="${MALT_EVAL_TIMESTAMP:-$(date -u +%Y%m%dT%H%M%SZ)}"
run_id="${MALT_EVAL_RUN_ID:-write-trace-full-${timestamp}}"
eval_root="${MALT_EVAL_ROOT:-/home/ubuntu/malt/eval-results}"
run_dir="${MALT_EVAL_RUN_DIR:-${eval_root}/${run_id}}"
session="${MALT_EVAL_TMUX_SESSION:-malt-eval-${timestamp}}"

mkdir -p "$run_dir"

export MALT_EVAL_TIMESTAMP="$timestamp"
export MALT_EVAL_RUN_ID="$run_id"
export MALT_EVAL_RUN_DIR="$run_dir"

cmd="./scripts/eval/run-write-trace-full.sh"

if command -v tmux >/dev/null 2>&1; then
  tmux new-session -d -s "$session" "$cmd"
  {
    printf 'launcher=tmux\n'
    printf 'session=%s\n' "$session"
    printf 'run_dir=%s\n' "$run_dir"
    printf 'attach_command=tmux attach -t %s\n' "$session"
  } | tee "${run_dir}/launcher.txt"
  echo
  echo "Started detached tmux session: ${session}"
  echo "Run directory: ${run_dir}"
  echo "Attach: tmux attach -t ${session}"
  echo "Detach without stopping: Ctrl-b then d"
  exit 0
fi

if command -v setsid >/dev/null 2>&1; then
  setsid bash -lc "$cmd" >/dev/null 2>&1 < /dev/null &
  pid=$!
  {
    printf 'launcher=setsid\n'
    printf 'pid=%s\n' "$pid"
    printf 'run_dir=%s\n' "$run_dir"
  } | tee "${run_dir}/launcher.txt"
  echo
  echo "Started detached process with setsid: ${pid}"
  echo "Run directory: ${run_dir}"
  exit 0
fi

nohup bash -lc "$cmd" >/dev/null 2>&1 < /dev/null &
pid=$!
{
  printf 'launcher=nohup\n'
  printf 'pid=%s\n' "$pid"
  printf 'run_dir=%s\n' "$run_dir"
} | tee "${run_dir}/launcher.txt"
echo
echo "Started detached process with nohup: ${pid}"
echo "Run directory: ${run_dir}"
