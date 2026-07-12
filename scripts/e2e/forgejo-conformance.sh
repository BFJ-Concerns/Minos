#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
work="${MINOS_CONFORMANCE_DIR:-$(mktemp -d)}"
source "$root/scripts/e2e/resources.sh"
declare -a MINOS_E2E_RESOURCE_LOCK_FDS=()
minos_e2e_allocate_resources
container="$MINOS_E2E_RESOLVED_CONTAINER"
image="${MINOS_FORGEJO_IMAGE:-codeberg.org/forgejo/forgejo:14.0.5}"
port="$MINOS_E2E_RESOLVED_FORGEJO_PORT"
owner="Minos"
repo="adapter-conformance"

cleanup() {
  set +e
  docker rm -f "$container" >/dev/null 2>&1
}
trap cleanup EXIT

wait_for_api() {
  for _ in {1..120}; do
    curl -fsS "http://127.0.0.1:${port}/api/v1/version" >/dev/null 2>&1 && return 0
    sleep 1
  done
  echo "timed out waiting for disposable Forgejo" >&2
  return 1
}

mkdir -p "$work/forgejo/gitea/conf"
cat >"$work/forgejo/gitea/conf/app.ini" <<EOF
APP_NAME = Minos Forge Adapter Conformance
RUN_USER = git

[server]
DOMAIN = 127.0.0.1
HTTP_PORT = 3000
ROOT_URL = http://127.0.0.1:${port}/
DISABLE_SSH = true

[database]
DB_TYPE = sqlite3
PATH = /data/gitea/gitea.db

[security]
INSTALL_LOCK = true
SECRET_KEY = minos-conformance-secret-key
INTERNAL_TOKEN = minos-conformance-internal-token

[service]
DISABLE_REGISTRATION = false
REQUIRE_SIGNIN_VIEW = false
EOF

docker rm -f "$container" >/dev/null 2>&1 || true
docker run -d --name "$container" \
  -p "127.0.0.1:${port}:3000" \
  -v "$work/forgejo:/data" \
  -e USER_UID="$(id -u)" \
  -e USER_GID="$(id -g)" \
  "$image" >/dev/null
wait_for_api

docker exec -u git "$container" forgejo admin user create \
  --username Minos --password password --email minos@example.invalid --must-change-password=false >/dev/null
docker exec -u git "$container" forgejo admin user create \
  --username reader --password password --email reader@example.invalid --must-change-password=false >/dev/null
docker exec -u git "$container" forgejo admin user create \
  --username forker --password password --email forker@example.invalid --must-change-password=false >/dev/null

token="$(docker exec -u git "$container" forgejo admin user generate-access-token \
  --username Minos --token-name adapter-conformance --scopes 'write:repository,write:issue,write:user' --raw)"
reader_token="$(docker exec -u git "$container" forgejo admin user generate-access-token \
  --username reader --token-name adapter-conformance-read --scopes 'read:repository,read:issue,read:user' --raw)"
forker_token="$(docker exec -u git "$container" forgejo admin user generate-access-token \
  --username forker --token-name adapter-conformance-fork --scopes 'write:repository,write:issue,write:user' --raw)"

curl -fsS -X POST \
  -H "Authorization: token ${token}" \
  -H "Content-Type: application/json" \
  --data '{"auto_init":true,"default_branch":"main","name":"adapter-conformance","private":false}' \
  "http://127.0.0.1:${port}/api/v1/user/repos" >/dev/null

MINOS_FORGE_CONFORMANCE=1 \
MINOS_CONFORMANCE_API_BASE="http://127.0.0.1:${port}" \
MINOS_CONFORMANCE_TOKEN="$token" \
MINOS_CONFORMANCE_READER_TOKEN="$reader_token" \
MINOS_CONFORMANCE_FORKER_TOKEN="$forker_token" \
MINOS_CONFORMANCE_OWNER="$owner" \
MINOS_CONFORMANCE_REPO="$repo" \
MINOS_CONFORMANCE_ADAPTATION="$root/scripts/adaptations/forgejo" \
  go test -count=1 -run '^TestForgejoConformance$' -v ./internal/forge

echo "Forgejo adapter conformance passed; retained work directory: $work"
