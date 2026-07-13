#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
work="${MINOS_DEPLOYMENT_SMOKE_DIR:-$(mktemp -d)}"
source "$root/scripts/e2e/resources.sh"
declare -a MINOS_E2E_RESOURCE_LOCK_FDS=()
port=""
minos_e2e_claim_single_port port "${MINOS_DEPLOYMENT_SMOKE_PORT:-}"
receiver_pid=""

cleanup() {
  if [[ -n "$receiver_pid" ]]; then
    kill "$receiver_pid" >/dev/null 2>&1 || true
    wait "$receiver_pid" 2>/dev/null || true
  fi
  if [[ -z "${MINOS_DEPLOYMENT_SMOKE_KEEP:-}" ]]; then
    rm -rf "$work"
  fi
}
trap cleanup EXIT

for command in curl go jq sed systemd-analyze; do
  command -v "$command" >/dev/null || {
    echo "missing required command: $command" >&2
    exit 1
  }
done

mkdir -p \
  "$work/prefix/bin" \
  "$work/prefix/adaptations/forgejo" \
  "$work/config/repos" \
  "$work/logs" \
  "$work/runs" \
  "$work/units"

go build -o "$work/prefix/bin/minos" ./cmd/minos
cp -R "$root/scripts/adaptations/forgejo/." "$work/prefix/adaptations/forgejo/"
printf '%s\n' 'minos-secret' >"$work/config/webhook.secret"
printf '%s\n' 'dummy-token' >"$work/config/forgejo.token"

# Exercise the shipped skeleton itself, with only paths and the offline endpoint
# replaced. No active repo TOML is installed, so the smoke never calls a forge.
sed \
  -e "s|bind = \":8919\"|bind = \"127.0.0.1:${port}\"|" \
  -e "s|/opt/minos/adaptations/forgejo|$work/prefix/adaptations/forgejo|" \
  -e 's|REPLACE_WITH_FORGEJO_BASE_URL|https://forgejo.invalid|' \
  -e "s|/etc/minos/webhook.secret|$work/config/webhook.secret|" \
  -e "s|/etc/minos/forgejo.token|$work/config/forgejo.token|" \
  -e "s|/var/lib/minos/runs|$work/runs|" \
  -e "s|/var/log/minos/sweep.log|$work/logs/sweep.log|" \
  "$root/deploy/etc/minos/service.toml" >"$work/config/service.toml"

"$work/prefix/bin/minos" receive --config "$work/config" >"$work/logs/receiver.log" 2>&1 &
receiver_pid="$!"

ready=false
for _ in {1..50}; do
  if ! kill -0 "$receiver_pid" 2>/dev/null; then
    echo "receiver exited before becoming ready; see $work/logs/receiver.log" >&2
    exit 1
  fi
  status="$(curl --silent --output /dev/null --write-out '%{http_code}' \
    "http://127.0.0.1:${port}/hooks/forgejo" || true)"
  if [[ "$status" == "405" ]]; then
    ready=true
    break
  fi
  sleep 0.1
done
if [[ "$ready" != true ]]; then
  echo "receiver did not become ready" >&2
  exit 1
fi

fixture="$root/internal/shell/testdata/forgejo14/001-pull_request-opened.json"
printf '%s' "$(jq -r '.body' "$fixture")" >"$work/payload.json"
signature="$(jq -r '.headers["X-Forgejo-Signature"]' "$fixture")"
status="$(curl --silent --output "$work/response.txt" --write-out '%{http_code}' \
  --request POST "http://127.0.0.1:${port}/hooks/forgejo" \
  --header 'X-Forgejo-Event: pull_request' \
  --header 'X-Forgejo-Delivery: deployment-smoke' \
  --header "X-Forgejo-Signature: $signature" \
  --data-binary @"$work/payload.json")"
if [[ "$status" != "202" ]] || ! grep -qx 'not opted in' "$work/response.txt"; then
  echo "synthetic delivery failed: HTTP $status: $(cat "$work/response.txt")" >&2
  exit 1
fi

"$work/prefix/bin/minos" sweep --config "$work/config"
test -f "$work/logs/sweep.log"

for unit in "$root"/deploy/systemd/user/*; do
  sed \
    -e "s|/usr/local/bin/minos|$work/prefix/bin/minos|" \
    -e "s|/etc/minos|$work/config|" \
    "$unit" >"$work/units/$(basename "$unit")"
done
systemd-analyze --user verify "$work"/units/*

echo "ok: installed minos and adaptations under a temporary prefix"
echo "ok: receiver accepted an authenticated fixture (202 not opted in)"
echo "ok: sweep completed and opened its configured log"
echo "ok: systemd user units verified"
if [[ -n "${MINOS_DEPLOYMENT_SMOKE_KEEP:-}" ]]; then
  echo "evidence kept at: $work"
fi
