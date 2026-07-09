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
cleanup_units=()

cleanup() {
  set +e
  [[ -n "${receiver_pid:-}" ]] && kill "$receiver_pid" >/dev/null 2>&1
  [[ -n "${capture_pid:-}" ]] && kill "$capture_pid" >/dev/null 2>&1
  for unit in "${cleanup_units[@]:-}"; do
    [[ -n "$unit" ]] && systemctl --user stop "$unit" >/dev/null 2>&1
  done
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

status_count() {
  local sha="$1"
  local context="$2"
  api GET "/api/v1/repos/${owner}/${repo}/commits/${sha}/statuses" |
    jq --arg context "$context" '[.[] | select(.context == $context)] | length'
}

assert_no_status_after() {
  local description="$1"
  local sha="$2"
  local context="$3"
  sleep 3
  local count
  count="$(status_count "$sha" "$context")"
  if [[ "$count" != "0" ]]; then
    echo "${description}: expected no ${context} status, found ${count}" >&2
    return 1
  fi
  echo "ok: ${description}"
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

combined_status_state() {
  local sha="$1"
  api GET "/api/v1/repos/${owner}/${repo}/commits/${sha}/status" |
    jq -r '.state // ""'
}

set_status() {
  local sha="$1"
  local context="$2"
  local state="$3"
  local description="$4"
  api POST "/api/v1/repos/${owner}/${repo}/statuses/${sha}" \
    "$(jq -nc --arg context "$context" --arg state "$state" --arg description "$description" '{context:$context,state:$state,description:$description}')" >/dev/null
}

capture_timeline_fixture() {
  local pr="$1"
  local name="$2"
  api GET "/api/v1/repos/${owner}/${repo}/issues/${pr}/timeline" >"${fixture_dir}/${name}.json"
}

labels_csv() {
  local pr="$1"
  api GET "/api/v1/repos/${owner}/${repo}/pulls/${pr}" |
    jq -r '[.labels[]?.name] | join(",")'
}

label_actor() {
  local pr="$1"
  local label="$2"
  PUMP19_API_BASE="http://127.0.0.1:${port}" PUMP19_FORGE_TOKEN="$bot_token" \
    "$work/adaptations/label-actor" "$owner" "$repo" "$pr" "$label"
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

write_repo_config() {
  local run_body="${1:-}"
  cat >"$work/config/repos/local--${owner}--${repo}.toml" <<EOF
forge = "local"
owner = "${owner}"
repo = "${repo}"

[adaptation]
briefs = ".review"
EOF
  if [[ -n "$run_body" ]]; then
    printf 'run-body = "%s"\n' "$run_body" >>"$work/config/repos/local--${owner}--${repo}.toml"
  fi
  cat >>"$work/config/repos/local--${owner}--${repo}.toml" <<EOF

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
}

head_sha() {
  local pr="$1"
  api GET "/api/v1/repos/${owner}/${repo}/pulls/${pr}" | jq -r '.head.sha'
}

run_sweep() {
  PUMP19_STUB_MODE="${1:-normal}" "${root}/pump19" sweep --config "$work/config"
}

latest_run_meta_value() {
  local pr="$1"
  local key="$2"
  find "$work/runs/local--${owner}--${repo}/pr${pr}" -name meta.env -print0 |
    xargs -0 grep -h "^${key}=" |
    tail -n1 |
    cut -d= -f2-
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
docker exec -u git "$container" forgejo admin user create \
  --username mallory --password password --email mallory@example.invalid --admin --must-change-password=false >/dev/null
bot_token="$(docker exec -u git "$container" forgejo admin user generate-access-token \
  --username "$owner" --token-name pump19-e2e --scopes 'write:repository,write:issue,write:user' --raw)"
ben_token="$(docker exec -u git "$container" forgejo admin user generate-access-token \
  --username bob --token-name bob-e2e --scopes 'write:repository,write:issue,write:user' --raw)"
mallory_token="$(docker exec -u git "$container" forgejo admin user generate-access-token \
  --username mallory --token-name mallory-e2e --scopes 'write:repository,write:issue,write:user' --raw)"
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

EOF

write_repo_config ""

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
api_with_token "$ben_token" POST "/api/v1/repos/${owner}/${repo}/issues/${pr1}/labels" '{"labels":["Ready"]}' >/dev/null
wait_for_call "journey 1 finish status" status_is "$sha1" "pump19/finish" success
wait_for_call "journey 1 finishing cleared" label_lacks "$pr1" Finishing

pr_ready="$(create_branch_and_pr ready-removal "ready removal")"
sha_ready="$(head_sha "$pr_ready")"
wait_for_call "ready-removal review status" status_is "$sha_ready" "pump19/review" success
api_with_token "$mallory_token" POST "/api/v1/repos/${owner}/${repo}/issues/${pr_ready}/labels" '{"labels":["Ready"]}' >/dev/null
assert_no_status_after "unauthorised Ready add does not fire finish" "$sha_ready" "pump19/finish"
encoded_ready="$(printf '%s' Ready | jq -sRr @uri)"
api_with_token "$ben_token" DELETE "/api/v1/repos/${owner}/${repo}/issues/${pr_ready}/labels/${encoded_ready}" >/dev/null
wait_for_call "Ready removed" label_lacks "$pr_ready" Ready
assert_no_status_after "Ready removal does not fire finish" "$sha_ready" "pump19/finish"

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
wait_for_call "journey 4 reviewing cleared" label_lacks "$pr3" Reviewing
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

failing_body="${work}/failing-run-body"
cat >"$failing_body" <<'EOF'
#!/usr/bin/env sh
echo "persistent body failure"
exit 42
EOF
chmod +x "$failing_body"
write_repo_config "$failing_body"
"${root}/pump19" receive --config "$work/config" >"$work/logs/receiver-failing-body.log" 2>&1 &
receiver_pid="$!"
pr_fail="$(create_branch_and_pr persistent-failure "persistent failure")"
sha_fail="$(head_sha "$pr_fail")"
wait_for_call "persistent failure records review error" status_is "$sha_fail" "pump19/review" error
sleep 3
run_sweep normal
run_sweep normal
failure_status_count="$(status_count "$sha_fail" "pump19/review")"
if [[ "$failure_status_count" != "1" ]]; then
  echo "persistent failure produced ${failure_status_count} review statuses" >&2
  exit 1
fi
reaped_failure_count="$(find "$work/runs/local--${owner}--${repo}/pr${pr_fail}" -name '*.reaped-*' | wc -l | tr -d ' ')"
if [[ "$reaped_failure_count" != "0" ]]; then
  echo "persistent failure accumulated ${reaped_failure_count} reaped directories" >&2
  exit 1
fi
echo "ok: persistent failure is loud and not re-fired"
kill "$receiver_pid" >/dev/null 2>&1 || true
receiver_pid=""
write_repo_config ""

kill "$receiver_pid" >/dev/null 2>&1 || true
PUMP19_STUB_MODE=hang "${root}/pump19" receive --config "$work/config" >"$work/logs/receiver-superseded-hang.log" 2>&1 &
receiver_pid="$!"
pr_superseded="$(create_branch_and_pr superseded-hang "superseded hang")"
sha_superseded_old="$(head_sha "$pr_superseded")"
wait_for_call "superseded-head hang applied Reviewing" label_has "$pr_superseded" Reviewing
unit_superseded="$(latest_run_meta_value "$pr_superseded" PUMP19_UNIT)"
workspace_superseded="$(latest_run_meta_value "$pr_superseded" PUMP19_WORKSPACE)"
cleanup_units+=("$unit_superseded")
kill "$receiver_pid" >/dev/null 2>&1 || true
PUMP19_STUB_MODE=normal "${root}/pump19" receive --config "$work/config" >"$work/logs/receiver-superseded-normal.log" 2>&1 &
receiver_pid="$!"
sha_superseded_new="$(push_update superseded-hang "superseded recovered head")"
wait_for_call "superseded-head new review status" status_is "$sha_superseded_new" "pump19/review" success
wait_for_call "superseded-head new run cleared Reviewing" label_lacks "$pr_superseded" Reviewing
sleep 3
run_sweep normal
run_sweep normal
run_sweep normal
unit_state="$(systemctl --user show "$unit_superseded" --property=ActiveState --value 2>/dev/null || true)"
if [[ "$unit_state" != "" && "$unit_state" != "inactive" && "$unit_state" != "failed" ]]; then
  echo "superseded-head hung run still active after sweeps: ${unit_state} (${sha_superseded_old} -> ${sha_superseded_new})" >&2
  exit 1
fi
if [[ -d "$workspace_superseded" ]]; then
  echo "superseded-head hung run workspace still present: ${workspace_superseded}" >&2
  exit 1
fi
if ! find "$work/runs/local--${owner}--${repo}/pr${pr_superseded}" -name '*.reaped-*' | grep -q .; then
  echo "superseded-head hung run left no reaped evidence directory" >&2
  exit 1
fi
echo "ok: superseded-head hung run is reaped with workspace deletion"

cp "$work/adaptations/prepare-workspace" "$work/prepare-workspace.good"
cat >"$work/adaptations/prepare-workspace" <<'EOF'
#!/usr/bin/env sh
echo "transient prepare-workspace failure" >&2
exit 1
EOF
chmod +x "$work/adaptations/prepare-workspace"
kill "$receiver_pid" >/dev/null 2>&1 || true
PUMP19_STUB_MODE=normal "${root}/pump19" receive --config "$work/config" >"$work/logs/receiver-transient-wrap.log" 2>&1 &
receiver_pid="$!"
pr_transient="$(create_branch_and_pr transient-wrap "transient wrap")"
sha_transient="$(head_sha "$pr_transient")"
wait_for_call "transient wrap failure became visible" status_is "$sha_transient" "pump19/review" error
cp "$work/prepare-workspace.good" "$work/adaptations/prepare-workspace"
sleep 3
run_sweep normal
run_sweep normal
run_sweep normal
wait_for_call "transient wrap failure recovered" status_is "$sha_transient" "pump19/review" success
echo "ok: transient wrap failure recovers"

pr_status_tie="$(create_branch_and_pr same-second-status "same second status")"
sha_status_tie="$(head_sha "$pr_status_tie")"
set_status "$sha_status_tie" "pump19/review" success "written first"
set_status "$sha_status_tie" "pump19/review" error "written second"
service_status="$(status_state "$sha_status_tie" "pump19/review")"
forge_status="$(combined_status_state "$sha_status_tie")"
if [[ "$service_status" != "$forge_status" ]]; then
  echo "same-second status disagreement: service reads ${service_status}, forge combined view is ${forge_status}" >&2
  exit 1
fi
echo "ok: same-second service status read agrees with forge combined view"

kill "$receiver_pid" >/dev/null 2>&1 || true
PUMP19_STUB_MODE=normal "${root}/pump19" receive --config "$work/config" >"$work/logs/receiver-ready-poison.log" 2>&1 &
receiver_pid="$!"
pr_poison="$(create_branch_and_pr ready-poison "ready poison")"
sha_poison="$(head_sha "$pr_poison")"
wait_for_call "ready-poison review status" status_is "$sha_poison" "pump19/review" success
api_with_token "$mallory_token" POST "/api/v1/repos/${owner}/${repo}/issues/${pr_poison}/labels" '{"labels":["Ready"]}' >/dev/null
assert_no_status_after "unauthorised Ready poisoning does not fire finish" "$sha_poison" "pump19/finish"
if [[ "$(label_actor "$pr_poison" Ready)" != "mallory" ]]; then
  echo "ready-poison setup did not attribute Ready to mallory" >&2
  exit 1
fi
run_sweep normal
wait_for_call "unauthorised Ready is cleared by sweep" label_lacks "$pr_poison" Ready
api_with_token "$ben_token" POST "/api/v1/repos/${owner}/${repo}/issues/${pr_poison}/labels" '{"labels":["Ready"]}' >/dev/null
wait_for_call "authorised Ready re-add fires finish after clearance" status_is "$sha_poison" "pump19/finish" success
pr_authorised_ready="$(create_branch_and_pr authorised-ready-survives "authorised Ready survives")"
sha_authorised_ready="$(head_sha "$pr_authorised_ready")"
wait_for_call "authorised-ready-survives review status" status_is "$sha_authorised_ready" "pump19/review" success
api_with_token "$ben_token" POST "/api/v1/repos/${owner}/${repo}/issues/${pr_authorised_ready}/labels" '{"labels":["Ready"]}' >/dev/null
wait_for_call "authorised-ready-survives finish status" status_is "$sha_authorised_ready" "pump19/finish" success
run_sweep normal
if ! label_has "$pr_authorised_ready" Ready; then
  echo "sweep cleared an authorised Ready label" >&2
  exit 1
fi
echo "ok: unauthorised Ready poisoning is cleared and authorised Ready survives"

# Spike A capture tail: fire the extra Forgejo 14.0.5 webhook shapes the
# normaliser needs to know about. These do not participate in the journeys.
timeline_pr="$(create_branch_and_pr fixture-timeline "fixture timeline")"
api_with_token "$ben_token" POST "/api/v1/repos/${owner}/${repo}/issues/${timeline_pr}/comments" '{"body":"fixture issue comment"}' >/dev/null
api_with_token "$ben_token" POST "/api/v1/repos/${owner}/${repo}/issues/${timeline_pr}/labels" '{"labels":["Ready"]}' >/dev/null
capture_timeline_fixture "$timeline_pr" "timeline-ready-added"
encoded_ready="$(printf '%s' Ready | jq -sRr @uri)"
api_with_token "$ben_token" DELETE "/api/v1/repos/${owner}/${repo}/issues/${timeline_pr}/labels/${encoded_ready}" >/dev/null
capture_timeline_fixture "$timeline_pr" "timeline-ready-removed"
api_with_token "$ben_token" POST "/api/v1/repos/${owner}/${repo}/issues/${timeline_pr}/labels" '{"labels":["Partial Coverage"]}' >/dev/null
capture_timeline_fixture "$timeline_pr" "timeline-partial-coverage-added"
encoded_partial="$(printf '%s' "Partial Coverage" | jq -sRr @uri)"
api_with_token "$ben_token" DELETE "/api/v1/repos/${owner}/${repo}/issues/${timeline_pr}/labels/${encoded_partial}" >/dev/null
capture_timeline_fixture "$timeline_pr" "timeline-partial-coverage-removed"
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
