#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
work="${PUMP19_DEPLOYMENT_SMOKE_DIR:-$(mktemp -d)}"
source "$root/scripts/e2e/resources.sh"
declare -a PUMP19_E2E_RESOURCE_LOCK_FDS=()
pump19_e2e_claim_single_port port "${PUMP19_DEPLOYMENT_SMOKE_PORT:-}"
receiver_pid=""

cleanup() {
  if [[ -n "$receiver_pid" ]]; then
    kill "$receiver_pid" >/dev/null 2>&1 || true
    wait "$receiver_pid" 2>/dev/null || true
  fi
  if [[ -z "${PUMP19_DEPLOYMENT_SMOKE_KEEP:-}" ]]; then
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
  "$work/config/secrets" \
  "$work/logs" \
  "$work/runs" \
  "$work/units"

go build -o "$work/prefix/bin/pump19" ./cmd/pump19
cp -R "$root/scripts/adaptations/forgejo/." "$work/prefix/adaptations/forgejo/"
printf '%s\n' 'pump19-secret' >"$work/config/secrets/forgejo-webhook"
printf '%s\n' 'dummy-token' >"$work/config/secrets/forgejo-token"

# Exercise the shipped skeleton itself, with only paths and the offline endpoint
# replaced. No active repo TOML is installed, so the smoke never calls a forge.
sed \
  -e "s|bind = \":8919\"|bind = \"127.0.0.1:${port}\"|" \
  -e "s|/opt/pump19/adaptations/forgejo|$work/prefix/adaptations/forgejo|" \
  -e 's|REPLACE_WITH_FORGEJO_BASE_URL|https://forgejo.invalid|' \
  -e "s|/etc/pump19/secrets/forgejo-webhook|$work/config/secrets/forgejo-webhook|" \
  -e "s|/etc/pump19/secrets/forgejo-token|$work/config/secrets/forgejo-token|" \
  -e "s|/var/lib/pump19/runs|$work/runs|" \
  -e "s|/var/log/pump19/sweep.log|$work/logs/sweep.log|" \
  "$root/deploy/etc/pump19/service.toml" >"$work/config/service.toml"

"$work/prefix/bin/pump19" receive --config "$work/config" >"$work/logs/receiver.log" 2>&1 &
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

"$work/prefix/bin/pump19" sweep --config "$work/config"
test -f "$work/logs/sweep.log"

for unit in "$root"/deploy/systemd/user/*; do
  sed \
    -e "s|/usr/local/bin/pump19|$work/prefix/bin/pump19|" \
    -e "s|/etc/pump19|$work/config|" \
    "$unit" >"$work/units/$(basename "$unit")"
done
systemd-analyze --user verify "$work"/units/*

echo "ok: installed pump19 and adaptations under a temporary prefix"
echo "ok: receiver accepted an authenticated fixture (202 not opted in)"
echo "ok: sweep completed and opened its configured log"
echo "ok: systemd user units verified"
if [[ -n "${PUMP19_DEPLOYMENT_SMOKE_KEEP:-}" ]]; then
  echo "evidence kept at: $work"
fi
