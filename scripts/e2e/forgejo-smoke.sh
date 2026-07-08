#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
work="${PUMP19_E2E_DIR:-$(mktemp -d)}"
container="${PUMP19_E2E_CONTAINER:-pump19-forgejo-e2e}"
image="${PUMP19_FORGEJO_IMAGE:-codeberg.org/forgejo/forgejo:14.0.5}"
port="${PUMP19_FORGEJO_PORT:-33080}"
hook_port="${PUMP19_HOOK_PORT:-18919}"
secret="pump19-secret"

cleanup() {
  docker rm -f "$container" >/dev/null 2>&1 || true
}
trap cleanup EXIT

mkdir -p "$work/config/repos" "$work/runs" "$work/logs" "$work/adaptations" "$work/forgejo/gitea/conf"
cp -R "$root/scripts/adaptations/forgejo/." "$work/adaptations/"

cat >"$work/forgejo/gitea/conf/app.ini" <<EOF
APP_NAME = Pump-19 E2E
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
SECRET_KEY = pump19-e2e-secret-key
INTERNAL_TOKEN = pump19-e2e-internal-token

[service]
DISABLE_REGISTRATION = false
REQUIRE_SIGNIN_VIEW = false
EOF

docker rm -f "$container" >/dev/null 2>&1 || true
docker run -d --name "$container" \
  -p "127.0.0.1:${port}:3000" \
  --add-host=host.docker.internal:host-gateway \
  -v "${work}/forgejo:/data" \
  -e USER_UID="$(id -u)" \
  -e USER_GID="$(id -g)" \
  "$image" >/dev/null

for _ in {1..120}; do
  if curl -fsS "http://127.0.0.1:${port}/api/v1/version" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

docker exec -u git "$container" forgejo admin user create \
  --username bob --password password --email bob@example.invalid --admin || true
docker exec -u git "$container" forgejo admin user create \
  --username pump19 --password password --email pump19@example.invalid || true

token="$(docker exec -u git "$container" forgejo admin user generate-access-token --username pump19 --token-name pump19-e2e --scopes all 2>/dev/null | awk '/Access token was successfully created/ {print $NF}')"
if [[ -z "${token}" ]]; then
  token="$(docker exec -u git "$container" forgejo admin user generate-access-token --username pump19 --token-name pump19-e2e-$(date +%s) --scopes all 2>/dev/null | awk '/Access token was successfully created/ {print $NF}')"
fi
printf '%s\n' "$token" >"$work/token"
printf '%s\n' "$secret" >"$work/webhook.secret"

curl -fsS -H "Authorization: token ${token}" -H 'Content-Type: application/json' \
  -d '{"auto_init":true,"default_branch":"main","name":"subject","private":false}' \
  "http://127.0.0.1:${port}/api/v1/user/repos" >/dev/null

cat >"$work/config/service.toml" <<EOF
[listener]
bind = "127.0.0.1:${hook_port}"

[forges.local]
adaptation = "${work}/adaptations"
api-base = "http://127.0.0.1:${port}"
webhook-secret-file = "${work}/webhook.secret"
credential-file = "${work}/token"
signature-header = "X-Forgejo-Signature"

[runs]
dir = "${work}/runs"

[sweep]
liveness-threshold = "2s"
log = "${work}/logs/sweep.log"

[scrub]
vars = ["PUMP19_FORGE_TOKEN"]

[spawn]
mode = "systemd"
EOF

cat >"$work/config/repos/local--pump19--subject.toml" <<EOF
forge = "local"
owner = "pump19"
repo = "subject"

[adaptation]
briefs = ".review"
skill = "${root}/README.md"

[[trigger]]
run = "review"
on = ["pr-opened", "pr-reopened", "pr-synchronized", "pr-edited"]
authors = ["*"]
drafts = false

[[trigger]]
run = "fix"
on = ["review-rejected"]
actors = ["pump19"]

[[trigger]]
run = "finish"
on = ["label-added:Ready"]
actors = ["bob"]
EOF

"${root}/pump19" receive --config "$work/config" >"$work/logs/receiver.log" 2>&1 &
receiver_pid="$!"
trap 'kill "$receiver_pid" >/dev/null 2>&1 || true; cleanup' EXIT
sleep 1

curl -fsS -H "Authorization: token ${token}" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg url "http://host.docker.internal:${hook_port}/hooks/local" --arg secret "$secret" '{type:"gitea",config:{url:$url,content_type:"json",secret:$secret},events:["pull_request","pull_request_label","pull_request_rejected"],active:true}')" \
  "http://127.0.0.1:${port}/api/v1/repos/pump19/subject/hooks" >/dev/null

echo "Forgejo e2e harness started at ${work}"
echo "Create and push a test branch against http://127.0.0.1:${port}/pump19/subject, then open a PR to exercise webhook journeys."
