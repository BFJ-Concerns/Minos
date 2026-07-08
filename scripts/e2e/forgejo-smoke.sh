#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
work="${PUMP19_E2E_DIR:-$(mktemp -d)}"
container="${PUMP19_E2E_CONTAINER:-pump19-forgejo-e2e}"
image="${PUMP19_FORGEJO_IMAGE:-codeberg.org/forgejo/forgejo:14.0.5}"
port="${PUMP19_FORGEJO_PORT:-33080}"
hook_port="${PUMP19_HOOK_PORT:-18919}"
capture_port="${PUMP19_CAPTURE_PORT:-18920}"
secret="pump19-secret"
owner="pump19"
repo="subject"
fixture_dir="${PUMP19_E2E_FIXTURE_DIR:-${work}/fixtures}"

cleanup() {
  set +e
  [[ -n "${receiver_pid:-}" ]] && kill "$receiver_pid" >/dev/null 2>&1
  [[ -n "${capture_pid:-}" ]] && kill "$capture_pid" >/dev/null 2>&1
  docker rm -f "$container" >/dev/null 2>&1
}
trap cleanup EXIT

api() {
  api_with_token "$bot_token" "$@"
}

api_with_token() {
  local token="$1"
  shift
  local method="$1"
  local path="$2"
  local data="${3:-}"
  if [[ -n "$data" ]]; then
    curl -fsS -X "$method" -H "Authorization: token ${token}" -H "Content-Type: application/json" -d "$data" "http://127.0.0.1:${port}${path}"
  else
    curl -fsS -X "$method" -H "Authorization: token ${token}" "http://127.0.0.1:${port}${path}"
  fi
}

wait_for_call() {
  local description="$1"
  shift
  for _ in {1..120}; do
    if "$@" >/dev/null 2>&1; then
      echo "ok: ${description}"
      return 0
    fi
    sleep 1
  done
  echo "timed out: ${description}" >&2
  return 1
}

status_is() {
  local sha="$1"
  local context="$2"
  local expected="$3"
  local actual
  actual="$(status_state "$sha" "$context")"
  if [[ "$actual" != "$expected" ]]; then
    return 1
  fi
}

label_has() {
  local pr="$1"
  local label="$2"
  labels_csv "$pr" | grep -q "$label"
}

label_lacks() {
  local pr="$1"
  local label="$2"
  ! labels_csv "$pr" | grep -q "$label"
}

status_state() {
  local sha="$1"
  local context="$2"
  api GET "/api/v1/repos/${owner}/${repo}/commits/${sha}/statuses" |
    jq -r --arg context "$context" '[.[] | select(.context == $context) | (.status // .state)][0] // ""'
}

labels_csv() {
  local pr="$1"
  api GET "/api/v1/repos/${owner}/${repo}/pulls/${pr}" |
    jq -r '[.labels[]?.name] | join(",")'
}

create_branch_and_pr() {
  local name="$1"
  local text="$2"
  (
    cd "$subject_clone"
    git checkout main >/dev/null 2>&1
    git checkout -b "$name" >/dev/null 2>&1
    printf '%s\n' "$text" >> file.txt
    git add file.txt
    git commit -m "change ${name}" >/dev/null 2>&1
    git push origin "$name" >/dev/null 2>&1
  )
  api POST "/api/v1/repos/${owner}/${repo}/pulls" "$(jq -nc --arg head "$name" --arg base main --arg title "$name" '{head:$head,base:$base,title:$title}')" |
    jq -r '.number'
}

push_update() {
  local branch="$1"
  local text="$2"
  (
    cd "$subject_clone"
    git checkout "$branch" >/dev/null 2>&1
    printf '%s\n' "$text" >> file.txt
    git add file.txt
    git commit -m "update ${branch}" >/dev/null 2>&1
    git push origin "$branch" >/dev/null 2>&1
    git rev-parse HEAD
  )
}

head_sha() {
  local pr="$1"
  api GET "/api/v1/repos/${owner}/${repo}/pulls/${pr}" | jq -r '.head.sha'
}

run_sweep() {
  PUMP19_STUB_MODE="${1:-normal}" "${root}/pump19" sweep --config "$work/config"
}

mkdir -p "$work/config/repos" "$work/runs" "$work/logs" "$work/adaptations" "$work/forgejo/gitea/conf" "$fixture_dir"
cp -R "$root/scripts/adaptations/forgejo/." "$work/adaptations/"

cat >"$work/forgejo/gitea/conf/app.ini" <<EOF
APP_NAME = Pump-19 E2E
RUN_USER = git

[server]
DOMAIN = 127.0.0.1
HTTP_PORT = ${port}
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

[webhook]
ALLOWED_HOST_LIST = 127.0.0.1,localhost
EOF

docker rm -f "$container" >/dev/null 2>&1 || true
docker run -d --name "$container" \
  --network host \
  -v "${work}/forgejo:/data" \
  -e USER_UID="$(id -u)" \
  -e USER_GID="$(id -g)" \
  "$image" >/dev/null

wait_for_call "Forgejo API" curl -fsS "http://127.0.0.1:${port}/api/v1/version"

docker exec -u git "$container" forgejo admin user create \
  --username bob --password password --email bob@example.invalid --admin --must-change-password=false >/dev/null
docker exec -u git "$container" forgejo admin user create \
  --username "$owner" --password password --email pump19@example.invalid --must-change-password=false >/dev/null
bot_token="$(docker exec -u git "$container" forgejo admin user generate-access-token \
  --username "$owner" --token-name pump19-e2e --scopes 'write:repository,write:issue,write:user' --raw)"
ben_token="$(docker exec -u git "$container" forgejo admin user generate-access-token \
  --username bob --token-name bob-e2e --scopes 'write:repository,write:issue,write:user' --raw)"
printf '%s\n' "$bot_token" >"$work/token"
printf '%s\n' "$secret" >"$work/webhook.secret"

api POST "/api/v1/user/repos" '{"auto_init":true,"default_branch":"main","name":"subject","private":false}' >/dev/null
for label in Reviewing Fixing Finishing Converged "Standing Findings" "Partial Coverage" Ready; do
  api POST "/api/v1/repos/${owner}/${repo}/labels" "$(jq -nc --arg name "$label" '{name:$name,color:"#336699"}')" >/dev/null || true
done

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

cat >"$work/config/repos/local--${owner}--${repo}.toml" <<EOF
forge = "local"
owner = "${owner}"
repo = "${repo}"

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

PUMP19_FIXTURE_DIR="$fixture_dir" \
PUMP19_CAPTURE_PORT="$capture_port" \
PUMP19_FORWARD_URL="http://127.0.0.1:${hook_port}/hooks/local" \
  "$root/scripts/e2e/capture_forward.py" >"$work/logs/capture.log" 2>&1 &
capture_pid="$!"

"${root}/pump19" receive --config "$work/config" >"$work/logs/receiver.log" 2>&1 &
receiver_pid="$!"
sleep 1

api POST "/api/v1/repos/${owner}/${repo}/hooks" "$(jq -nc --arg url "http://127.0.0.1:${capture_port}/hooks/local" --arg secret "$secret" '{type:"gitea",config:{url:$url,content_type:"json",secret:$secret},events:["pull_request","pull_request_label","pull_request_rejected","pull_request_approved","issue_comment"],active:true}')" >/dev/null

subject_clone="$work/subject"
git clone "http://${owner}:${bot_token}@127.0.0.1:${port}/${owner}/${repo}.git" "$subject_clone" >/dev/null 2>&1
(
  cd "$subject_clone"
  git config user.name "Pump 19 E2E"
  git config user.email "pump19@example.invalid"
)

pr1="$(create_branch_and_pr journey-one "journey one")"
sha1="$(head_sha "$pr1")"
wait_for_call "journey 1 review status" status_is "$sha1" "pump19/review" success
wait_for_call "journey 1 label cleared" label_lacks "$pr1" Reviewing

first_fixture="$(ls "$fixture_dir"/*pull_request-opened.json | head -n1)"
curl -fsS -X POST "http://127.0.0.1:${hook_port}/hooks/local" \
  -H "X-Forgejo-Event: pull_request" \
  -H "X-Forgejo-Delivery: replay-journey-one" \
  -H "X-Forgejo-Signature: $(jq -r '.headers["X-Forgejo-Signature"]' "$first_fixture")" \
  --data-binary "$(jq -r '.body' "$first_fixture")" >/dev/null
sleep 1
status_count="$(api GET "/api/v1/repos/${owner}/${repo}/commits/${sha1}/statuses" | jq '[.[] | select(.context == "pump19/review")] | length')"
if [[ "$status_count" != "1" ]]; then
  echo "duplicate delivery produced ${status_count} review statuses" >&2
  exit 1
fi
echo "ok: journey 2 duplicate delivery idempotent"

kill "$receiver_pid" >/dev/null 2>&1 || true
sleep 1
PUMP19_STUB_MODE=hang "${root}/pump19" receive --config "$work/config" >"$work/logs/receiver-hang.log" 2>&1 &
hang_receiver_pid="$!"
receiver_pid="$hang_receiver_pid"
pr2="$(create_branch_and_pr journey-three "journey three")"
wait_for_call "journey 3 reviewing label" label_has "$pr2" Reviewing
unit="$(find "$work/runs/local--${owner}--${repo}/pr${pr2}" -name meta.env -print -quit | xargs -r grep -h '^PUMP19_UNIT=' | tail -n1 | cut -d= -f2-)"
if [[ -n "$unit" ]]; then
  systemctl --user kill --signal=KILL "$unit" >/dev/null 2>&1 || true
fi
sleep 3
sha2="$(head_sha "$pr2")"
run_sweep normal
wait_for_call "journey 3 refired status" status_is "$sha2" "pump19/review" success
wait_for_call "journey 3 reviewing cleared" label_lacks "$pr2" Reviewing

kill "$receiver_pid" >/dev/null 2>&1 || true
sleep 1
PUMP19_STUB_MODE=slow PUMP19_STUB_SLOW_SECONDS=6 "${root}/pump19" receive --config "$work/config" >"$work/logs/receiver-slow.log" 2>&1 &
receiver_pid="$!"
pr3="$(create_branch_and_pr journey-four "journey four")"
old_sha="$(head_sha "$pr3")"
sleep 1
new_sha="$(push_update journey-four "journey four update")"
wait_for_call "journey 4 new-head status" status_is "$new_sha" "pump19/review" success
old_count="$(api GET "/api/v1/repos/${owner}/${repo}/commits/${old_sha}/statuses" | jq '[.[] | select(.context == "pump19/review")] | length')"
if [[ "$old_count" != "0" ]]; then
  echo "old head received ${old_count} statuses after head moved" >&2
  exit 1
fi
echo "ok: journey 4 stale output discarded"

kill "$receiver_pid" >/dev/null 2>&1 || true
receiver_pid=""
pr4="$(create_branch_and_pr journey-five "journey five")"
sha4="$(head_sha "$pr4")"
sleep 1
run_sweep normal
wait_for_call "journey 5 sweep-fired status" status_is "$sha4" "pump19/review" success

# Spike A capture tail: fire the extra Forgejo 14.0.5 webhook shapes the
# normaliser needs to know about. These do not participate in the journeys.
api_with_token "$ben_token" POST "/api/v1/repos/${owner}/${repo}/issues/${pr1}/comments" '{"body":"fixture issue comment"}' >/dev/null
api_with_token "$ben_token" POST "/api/v1/repos/${owner}/${repo}/issues/${pr1}/labels" '{"labels":["Ready"]}' >/dev/null
encoded_ready="$(printf '%s' Ready | jq -sRr @uri)"
api_with_token "$ben_token" DELETE "/api/v1/repos/${owner}/${repo}/issues/${pr1}/labels/${encoded_ready}" >/dev/null
api PATCH "/api/v1/repos/${owner}/${repo}/pulls/${pr1}" '{"title":"journey-one undraft probe"}' >/dev/null || true
approve_pr="$(create_branch_and_pr fixture-approve "fixture approve")"
api_with_token "$ben_token" POST "/api/v1/repos/${owner}/${repo}/pulls/${approve_pr}/reviews" '{"body":"fixture approve","event":"APPROVED"}' >/dev/null || true
api_with_token "$ben_token" POST "/api/v1/repos/${owner}/${repo}/pulls/${pr1}/reviews" '{"body":"fixture reject","event":"REQUEST_CHANGES"}' >/dev/null || true
sleep 2

if [[ -n "${PUMP19_E2E_UPDATE_FIXTURES:-}" ]]; then
  rm -rf "$root/internal/shell/testdata/forgejo14"
  mkdir -p "$root/internal/shell/testdata/forgejo14"
  cp "$fixture_dir"/*.json "$root/internal/shell/testdata/forgejo14/"
fi

echo "Forgejo e2e passed. Work dir: ${work}"
