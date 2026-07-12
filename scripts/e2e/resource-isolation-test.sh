#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
work="$(mktemp -d)"

cleanup() {
  rm -rf "$work"
}
trap cleanup EXIT

probe() {
  local output="$1"
  local peer="$2"
  bash -c '
    set -euo pipefail
    source "$1/scripts/e2e/resources.sh"
    declare -a MINOS_E2E_RESOURCE_LOCK_FDS=()
    minos_e2e_allocate_resources
    printf "%s\t%s\t%s\t%s\n" \
      "$MINOS_E2E_RESOLVED_CONTAINER" \
      "$MINOS_E2E_RESOLVED_FORGEJO_PORT" \
      "$MINOS_E2E_RESOLVED_HOOK_PORT" \
      "$MINOS_E2E_RESOLVED_CAPTURE_PORT" >"$2"
    for _ in {1..100}; do
      [[ -s "$3" ]] && exit 0
      sleep 0.05
    done
    echo "peer allocator did not publish its resource set" >&2
    exit 1
  ' _ "$root" "$output" "$peer" &
}

probe "$work/first" "$work/second"
first_pid="$!"
probe "$work/second" "$work/first"
second_pid="$!"
wait "$first_pid"
wait "$second_pid"

if [[ "$(cat "$work/first")" == "$(cat "$work/second")" ]]; then
  echo "concurrent e2e allocators returned the same resource set" >&2
  exit 1
fi

resource_count="$(cat "$work/first" "$work/second" | tr '\t' '\n' | sort -u | wc -l | tr -d ' ')"
if [[ "$resource_count" != "8" ]]; then
  echo "concurrent e2e allocators shared a container name or port" >&2
  cat "$work/first" "$work/second" >&2
  exit 1
fi

echo "ok: concurrent e2e runs allocate disjoint resources"
