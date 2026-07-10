#!/usr/bin/env bash

# Allocate process-owned names and ports so concurrent estate runs cannot
# stop each other's Forgejo or bind each other's listeners.
pump19_e2e_allocate_resources() {
  local lock_root="${XDG_RUNTIME_DIR:-/tmp}/pump19-e2e-resources-${UID}"
  mkdir -p "$lock_root"

  PUMP19_E2E_RESOLVED_CONTAINER="${PUMP19_E2E_CONTAINER:-pump19-forgejo-e2e-${BASHPID}}"
  local container_key
  container_key="$(printf '%s' "$PUMP19_E2E_RESOLVED_CONTAINER" | tr -c 'A-Za-z0-9_.-' '_')"
  exec {container_lock_fd}>"${lock_root}/container-${container_key}.lock"
  if ! flock -n "$container_lock_fd"; then
    echo "e2e container resource is already claimed: ${PUMP19_E2E_RESOLVED_CONTAINER}" >&2
    return 1
  fi
  PUMP19_E2E_RESOURCE_LOCK_FDS+=("$container_lock_fd")

  pump19_e2e_claim_port PUMP19_E2E_RESOLVED_FORGEJO_PORT "${PUMP19_FORGEJO_PORT:-}" "$lock_root"
  pump19_e2e_claim_port PUMP19_E2E_RESOLVED_HOOK_PORT "${PUMP19_HOOK_PORT:-}" "$lock_root"
  pump19_e2e_claim_port PUMP19_E2E_RESOLVED_CAPTURE_PORT "${PUMP19_CAPTURE_PORT:-}" "$lock_root"
}

pump19_e2e_claim_port() {
  local output_name="$1"
  local requested="$2"
  local lock_root="$3"
  local start candidate offset port_lock_fd

  if [[ -n "$requested" ]]; then
    start="$requested"
  else
    # The lock is authoritative between estate runs; the PID-derived start
    # merely keeps concurrent allocators from queueing on the same first file.
    start=$((20000 + BASHPID % 20000))
  fi

  for ((offset = 0; offset < 20000; offset++)); do
    if [[ -n "$requested" ]]; then
      candidate="$requested"
    else
      candidate=$((20000 + (start - 20000 + offset) % 20000))
    fi

    exec {port_lock_fd}>"${lock_root}/port-${candidate}.lock"
    if flock -n "$port_lock_fd" && pump19_e2e_port_is_free "$candidate"; then
      PUMP19_E2E_RESOURCE_LOCK_FDS+=("$port_lock_fd")
      printf -v "$output_name" '%s' "$candidate"
      return 0
    fi
    eval "exec ${port_lock_fd}>&-"

    if [[ -n "$requested" ]]; then
      echo "e2e port resource is unavailable: ${requested}" >&2
      return 1
    fi
  done

  echo "could not allocate a free e2e port" >&2
  return 1
}

pump19_e2e_port_is_free() {
  python3 - "$1" <<'PY'
import socket
import sys

with socket.socket() as listener:
    listener.bind(("127.0.0.1", int(sys.argv[1])))
PY
}
