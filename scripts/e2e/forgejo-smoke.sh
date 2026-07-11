#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
work="${PUMP19_E2E_DIR:-$(mktemp -d)}"
source "$root/scripts/e2e/resources.sh"
declare -a PUMP19_E2E_RESOURCE_LOCK_FDS=()
pump19_e2e_allocate_resources
container="$PUMP19_E2E_RESOLVED_CONTAINER"
image="${PUMP19_FORGEJO_IMAGE:-codeberg.org/forgejo/forgejo:14.0.5}"
port="$PUMP19_E2E_RESOLVED_FORGEJO_PORT"
hook_port="$PUMP19_E2E_RESOLVED_HOOK_PORT"
capture_port="$PUMP19_E2E_RESOLVED_CAPTURE_PORT"
secret="pump19-secret"
owner="pump19"
repo="subject"
fixture_dir="${PUMP19_E2E_FIXTURE_DIR:-${work}/fixtures}"
run_body_records="${work}/logs/run-body-records.tsv"
recording_skill="${work}/skills/review.md"
recording_body="${work}/recording-run-body"

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

assert_run_body_record() {
  local description="$1"
  local kind="$2"
  local sha="$3"
  local occasion="$4"
  local skill="$5"
  local body="$6"
  local pr="$7"
  if [[ ! -f "$run_body_records" ]] || ! awk -F '\t' \
    -v kind="$kind" -v sha="$sha" -v occasion="$occasion" -v skill="$skill" -v body="$body" -v pr="$pr" \
    -v runs="$work/runs/" -v adaptation="$work/adaptations" -v config="$work/config" \
    '$1 == kind && $2 == sha && $3 == occasion && $4 == skill && $5 == body &&
     index($6, runs) == 1 && $7 == "local" && $8 == "pump19/subject" &&
     $9 == "pump19" && $10 == "subject" && $11 == pr && $12 == "main" &&
     $13 != "" && $14 == $6 "/diff.patch" && $15 == adaptation &&
     $16 == ".review" && $17 == config && $18 != "" && NF == 18 { found = 1 }
     END { exit !found }' \
    "$run_body_records"; then
    echo "${description}: run body did not observe the expected dispatch contract" >&2
    [[ -f "$run_body_records" ]] && sed -n '1,40p' "$run_body_records" >&2
    return 1
  fi
  echo "ok: ${description}"
}

assert_run_body_count() {
  local description="$1"
  local kind="$2"
  local sha="$3"
  local expected="$4"
  local actual
  actual="$(awk -F '\t' -v kind="$kind" -v sha="$sha" '$1 == kind && $2 == sha { count++ } END { print count + 0 }' "$run_body_records")"
  if [[ "$actual" != "$expected" ]]; then
    echo "${description}: expected ${expected} body starts, observed ${actual}" >&2
    sed -n '1,40p' "$run_body_records" >&2
    return 1
  fi
  echo "ok: ${description}"
}

assert_run_body_order() {
  local description="$1"
  local first_kind="$2"
  local first_sha="$3"
  local second_kind="$4"
  local second_sha="$5"
  if ! awk -F '\t' \
    -v first_kind="$first_kind" -v first_sha="$first_sha" \
    -v second_kind="$second_kind" -v second_sha="$second_sha" \
    '$1 == first_kind && $2 == first_sha && first == 0 { first = NR }
     $1 == second_kind && $2 == second_sha && second == 0 { second = NR }
     END { exit !(first > 0 && second > first) }' "$run_body_records"; then
    echo "${description}: observed run-body order was wrong" >&2
    sed -n '1,40p' "$run_body_records" >&2
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
    jq -r --arg context "$context" '[.[] | select(.context == $context)] | if length == 0 then "" else (max_by(.id) | (.status // .state)) end'
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
  local run_body="${1:-$recording_body}"
  cat >"$work/config/repos/local--${owner}--${repo}.toml" <<EOF
forge = "local"
owner = "${owner}"
repo = "${repo}"

[adaptation]
build = "go build ./..."
test = "go test ./..."
briefs = ".review"
skill = "${recording_skill}"
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

mkdir -p "$work/config/repos" "$work/runs" "$work/logs" "$work/adaptations" "$work/forgejo/gitea/conf" "$work/skills" "$fixture_dir"
cp -R "$root/scripts/adaptations/forgejo/." "$work/adaptations/"
printf '%s\n' 'test-only review skill path fixture' >"$recording_skill"
cat >"$recording_body" <<EOF
#!/usr/bin/env bash
set -euo pipefail

# One append records the body-start order observed beyond systemd and run-wrap.
printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
  "\${PUMP19_RUN_KIND}" \
  "\${PUMP19_HEAD_SHA}" \
  "\${PUMP19_OCCASION}" \
  "\${PUMP19_SKILL}" \
  "\${PUMP19_RUN_BODY}" \
  "\${PUMP19_RUN_DIR}" \
  "\${PUMP19_FORGE}" \
  "\${PUMP19_REPO}" \
  "\${PUMP19_OWNER}" \
  "\${PUMP19_REPO_NAME}" \
  "\${PUMP19_PR}" \
  "\${PUMP19_BASE_REF}" \
  "\${PUMP19_WORKSPACE}" \
  "\${PUMP19_DIFF}" \
  "\${PUMP19_ADAPTATION}" \
  "\${PUMP19_BRIEFS}" \
  "\${PUMP19_CONFIG}" \
  "\${PUMP19_UNIT}" >>"${run_body_records}"

exec "${root}/pump19" stub-run
EOF
chmod +x "$recording_body"

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
  --username Minos --password password --email minos@example.invalid --must-change-password=false >/dev/null
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
[service]
bot-login = "Minos"

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
max-concurrent = 2

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
assert_run_body_record "journey 1 run-body dispatch recorded" review "$sha1" pr-opened "$recording_skill" "$recording_body" "$pr1"
api_with_token "$ben_token" POST "/api/v1/repos/${owner}/${repo}/issues/${pr1}/labels" '{"labels":["Ready"]}' >/dev/null
wait_for_call "journey 1 finish status" status_is "$sha1" "pump19/finish" success
wait_for_call "journey 1 finishing cleared" label_lacks "$pr1" Finishing
assert_run_body_record "journey 1 finish dispatch recorded" finish "$sha1" label-added:Ready "$recording_skill" "$recording_body" "$pr1"
assert_run_body_order "journey 1 body order is review then finish" review "$sha1" finish "$sha1"
run_sweep normal
wait_for_call "authorised Ready survives sweep" label_has "$pr1" Ready

pr_ready="$(create_branch_and_pr ready-removal "ready removal")"
sha_ready="$(head_sha "$pr_ready")"
wait_for_call "ready-removal review status" status_is "$sha_ready" "pump19/review" success
api_with_token "$mallory_token" POST "/api/v1/repos/${owner}/${repo}/issues/${pr_ready}/labels" '{"labels":["Ready"]}' >/dev/null
assert_no_status_after "unauthorised Ready add does not trigger finish" "$sha_ready" "pump19/finish"
run_sweep normal
wait_for_call "unauthorised Ready cleared by sweep" label_lacks "$pr_ready" Ready
assert_no_status_after "unauthorised Ready clearance does not trigger finish" "$sha_ready" "pump19/finish"
api_with_token "$ben_token" POST "/api/v1/repos/${owner}/${repo}/issues/${pr_ready}/labels" '{"labels":["Ready"]}' >/dev/null
wait_for_call "authorised Ready re-add triggers finish" status_is "$sha_ready" "pump19/finish" success
wait_for_call "ready-removal finishing cleared" label_lacks "$pr_ready" Finishing

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
assert_run_body_count "journey 2 duplicate delivery never reaches run body" review "$sha1" 1

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
assert_run_body_count "journey 3 presence-only crash retries once" review "$sha2" 2
journey3_retry_count="$(find "$work/runs/local--${owner}--${repo}/pr${pr2}" -maxdepth 1 -name '*.retry-*' | wc -l | tr -d ' ')"
journey3_terminal_count="$(find "$work/runs/local--${owner}--${repo}/pr${pr2}" -name terminal.env | wc -l | tr -d ' ')"
if [[ "$journey3_retry_count" != "1" || "$journey3_terminal_count" != "0" ]]; then
  echo "journey 3 left retries=${journey3_retry_count} terminals=${journey3_terminal_count}" >&2
  exit 1
fi
echo "ok: journey 3 claim-seam-only crash remains replayable"

kill "$receiver_pid" >/dev/null 2>&1 || true
sleep 1
publication_hang_body="${work}/publication-hang-run-body"
cat >"$publication_hang_body" <<EOF
#!/usr/bin/env sh
set -eu
claim="\$("${root}/pump19" run-guard --config "\$PUMP19_CONFIG" begin)"
test "\$claim" = claimed
"${root}/pump19" adapt add-label "\$PUMP19_OWNER" "\$PUMP19_REPO_NAME" "\$PUMP19_PR" "Partial Coverage" >/dev/null
while :; do sleep 60; done
EOF
chmod +x "$publication_hang_body"
write_repo_config "$publication_hang_body"
"${root}/pump19" receive --config "$work/config" >"$work/logs/receiver-publication-hang.log" 2>&1 &
receiver_pid="$!"
pr_publication="$(create_branch_and_pr publication-hang "publication hang")"
sha_publication="$(head_sha "$pr_publication")"
wait_for_call "publication hang reviewing label" label_has "$pr_publication" Reviewing
wait_for_call "publication hang outcome label" label_has "$pr_publication" "Partial Coverage"
publication_unit="$(find "$work/runs/local--${owner}--${repo}/pr${pr_publication}" -name meta.env -print -quit | xargs -r grep -h '^PUMP19_UNIT=' | tail -n1 | cut -d= -f2-)"
if [[ -n "$publication_unit" ]]; then
  systemctl --user kill --signal=KILL "$publication_unit" >/dev/null 2>&1 || true
fi
sleep 3
run_sweep normal
wait_for_call "publication hang reviewing cleared" label_lacks "$pr_publication" Reviewing
assert_no_status_after "publication hang stays off the PR status surface" "$sha_publication" "pump19/review"
publication_terminal_count="$(find "$work/runs/local--${owner}--${repo}/pr${pr_publication}" -name terminal.env | wc -l | tr -d ' ')"
publication_retry_count="$(find "$work/runs/local--${owner}--${repo}/pr${pr_publication}" -maxdepth 1 -name '*.retry-*' | wc -l | tr -d ' ')"
if [[ "$publication_terminal_count" != "1" || "$publication_retry_count" != "0" ]]; then
  echo "publication hang left retries=${publication_retry_count} terminals=${publication_terminal_count}" >&2
  exit 1
fi
run_sweep normal
wait_for_call "publication hang latch suppresses re-entry" label_lacks "$pr_publication" Reviewing
echo "ok: journey 3b crash after a mission publication latches internally"

kill "$receiver_pid" >/dev/null 2>&1 || true
sleep 1
write_repo_config ""
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
sleep 1
PUMP19_STUB_MODE=hang "${root}/pump19" receive --config "$work/config" >"$work/logs/receiver-superseded-hang.log" 2>&1 &
receiver_pid="$!"
pr_superseded="$(create_branch_and_pr superseded-hang "superseded hang")"
wait_for_call "superseded hang reviewing label" label_has "$pr_superseded" Reviewing
old_meta=""
for _ in {1..60}; do
  if [[ -d "$work/runs/local--${owner}--${repo}/pr${pr_superseded}" ]]; then
    old_meta="$(find "$work/runs/local--${owner}--${repo}/pr${pr_superseded}" -name meta.env -print -quit)"
  fi
  [[ -n "$old_meta" ]] && break
  sleep 1
done
if [[ -z "$old_meta" ]]; then
  echo "superseded hang did not write metadata" >&2
  exit 1
fi
old_run_dir="$(dirname "$old_meta")"
old_unit="$(grep -h '^PUMP19_UNIT=' "$old_meta" | tail -n1 | cut -d= -f2-)"
old_workspace="$(grep -h '^PUMP19_WORKSPACE=' "$old_meta" | tail -n1 | cut -d= -f2-)"
kill "$receiver_pid" >/dev/null 2>&1 || true
sleep 1
"${root}/pump19" receive --config "$work/config" >"$work/logs/receiver-after-superseded-hang.log" 2>&1 &
receiver_pid="$!"
new_superseded_sha="$(push_update superseded-hang "superseded hang update")"
wait_for_call "superseded hang new-head status" status_is "$new_superseded_sha" "pump19/review" success
wait_for_call "superseded hang reviewing cleared" label_lacks "$pr_superseded" Reviewing
sleep 3
run_sweep normal
if [[ -e "$old_run_dir" ]]; then
  echo "superseded abandoned claim still has canonical directory: ${old_run_dir}" >&2
  exit 1
fi
superseded_reaped_count="$(find "$(dirname "$old_run_dir")" -maxdepth 1 -name "$(basename "$old_run_dir").reaped-*" | wc -l | tr -d ' ')"
if [[ "$superseded_reaped_count" != "1" ]]; then
  echo "superseded abandoned claim produced ${superseded_reaped_count} reaped directories" >&2
  exit 1
fi
if [[ -n "$old_workspace" && -e "$old_workspace" ]]; then
  echo "superseded abandoned workspace still exists: ${old_workspace}" >&2
  exit 1
fi
if [[ -n "$old_unit" ]]; then
  old_unit_state="$(systemctl --user show "$old_unit" --property=ActiveState --value 2>/dev/null || true)"
  if [[ -n "$old_unit_state" && "$old_unit_state" != "inactive" && "$old_unit_state" != "failed" ]]; then
    echo "superseded abandoned unit still active: ${old_unit} ${old_unit_state}" >&2
    exit 1
  fi
fi
echo "ok: superseded abandoned run reaped"

kill "$receiver_pid" >/dev/null 2>&1 || true
receiver_pid=""
pr4="$(create_branch_and_pr journey-five "journey five")"
sha4="$(head_sha "$pr4")"
sleep 1
run_sweep normal
wait_for_call "journey 5 sweep-triggered status" status_is "$sha4" "pump19/review" success

prepare_workspace="${work}/adaptations/prepare-workspace"
real_prepare_workspace="${prepare_workspace}.real"
mv "$prepare_workspace" "$real_prepare_workspace"
cat >"$prepare_workspace" <<EOF
#!/usr/bin/env sh
if [ ! -e "${work}/prepare-workspace.failed-once" ]; then
  touch "${work}/prepare-workspace.failed-once"
  exit 51
fi
exec "${real_prepare_workspace}" "\$@"
EOF
chmod +x "$prepare_workspace"
"${root}/pump19" receive --config "$work/config" >"$work/logs/receiver-transient-prepare.log" 2>&1 &
receiver_pid="$!"
pr_transient="$(create_branch_and_pr transient-prepare "transient prepare")"
sha_transient="$(head_sha "$pr_transient")"
transient_retry_marker=""
for _ in {1..60}; do
  if [[ -d "$work/runs/local--${owner}--${repo}/pr${pr_transient}" ]]; then
    transient_retry_marker="$(find "$work/runs/local--${owner}--${repo}/pr${pr_transient}" -path '*.retry-*' -prune -o -name retry.env -print -quit)"
  fi
  [[ -n "$transient_retry_marker" ]] && break
  sleep 1
done
if [[ -z "$transient_retry_marker" ]]; then
  echo "transient prepare failure did not record retry.env" >&2
  exit 1
fi
assert_no_status_after "transient prepare failure does not poison head" "$sha_transient" "pump19/review"
mv "$real_prepare_workspace" "$prepare_workspace"
# The production cadence is deliberately longer than this estate run. Age the
# machine marker rather than sleeping through a quarter-hour integration test.
sed -i 's/^PUMP19_FAILURE_AT=.*/PUMP19_FAILURE_AT=2020-01-01T00:00:00Z/' "$transient_retry_marker"
run_sweep normal
wait_for_call "transient prepare recovers review status" status_is "$sha_transient" "pump19/review" success
wait_for_call "transient prepare reviewing absent after recovery" label_lacks "$pr_transient" Reviewing
transient_retry_count="$(find "$work/runs/local--${owner}--${repo}/pr${pr_transient}" -maxdepth 1 -name '*.retry-*' | wc -l | tr -d ' ')"
if [[ "$transient_retry_count" != "1" ]]; then
  echo "transient prepare left ${transient_retry_count} retry evidence directories" >&2
  exit 1
fi
echo "ok: transient prepare-workspace failure retries once and recovers"
kill "$receiver_pid" >/dev/null 2>&1 || true
receiver_pid=""

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
assert_no_status_after "persistent failure stays off the PR" "$sha_fail" "pump19/review"
failure_run="$work/runs/local--${owner}--${repo}/pr${pr_fail}/$(cut -c1-12 <<<"$sha_fail")-review"
for _ in {1..60}; do
  [[ -f "$failure_run/retry.env" ]] && break
  sleep 1
done
if [[ ! -f "$failure_run/retry.env" ]]; then
  echo "persistent failure did not record its first attempt marker" >&2
  exit 1
fi
run_sweep normal
failure_retry_count="$(find "$work/runs/local--${owner}--${repo}/pr${pr_fail}" -maxdepth 1 -name '*.retry-*' | wc -l | tr -d ' ')"
if [[ "$failure_retry_count" != "0" ]]; then
  echo "persistent failure retried before its backoff elapsed" >&2
  exit 1
fi
sed -i 's/^PUMP19_FAILURE_AT=.*/PUMP19_FAILURE_AT=2020-01-01T00:00:00Z/' "$failure_run/retry.env"
run_sweep normal
for _ in {1..60}; do
  [[ -f "$failure_run/retry.env" ]] && break
  sleep 1
done
assert_no_status_after "backed-off retry stays off the PR" "$sha_fail" "pump19/review"
failure_retry_count="$(find "$work/runs/local--${owner}--${repo}/pr${pr_fail}" -maxdepth 1 -name '*.retry-*' | wc -l | tr -d ' ')"
failure_terminal_count="$(find "$work/runs/local--${owner}--${repo}/pr${pr_fail}" -name terminal.env | wc -l | tr -d ' ')"
if [[ "$failure_retry_count" != "1" || "$failure_terminal_count" != "0" ]]; then
  echo "persistent failure left retries=${failure_retry_count} terminals=${failure_terminal_count}" >&2
  exit 1
fi
echo "ok: persistent failure honours backoff, retries without a terminal cap, and stays off the PR"
kill "$receiver_pid" >/dev/null 2>&1 || true
receiver_pid=""
write_repo_config ""

api POST "/api/v1/repos/${owner}/${repo}/statuses/${sha4}" "$(jq -nc --arg context "pump19/review" '{context:$context,state:"success",description:"same-second success probe"}')" >/dev/null
api POST "/api/v1/repos/${owner}/${repo}/statuses/${sha4}" "$(jq -nc --arg context "pump19/review" '{context:$context,state:"error",description:"same-second error probe"}')" >/dev/null
status_pair="$(api GET "/api/v1/repos/${owner}/${repo}/commits/${sha4}/statuses" | jq '[.[] | select(.context == "pump19/review")][0:2]')"
printf '%s\n' "$status_pair" >"${fixture_dir}/commit-statuses.json"
same_second_count="$(jq '[.[].created_at] | unique | length' <<<"$status_pair")"
if [[ "$same_second_count" != "1" ]]; then
  echo "status ordering probe crossed a second boundary" >&2
  exit 1
fi
latest_review_state="$(PUMP19_API_BASE="http://127.0.0.1:${port}" PUMP19_FORGE_TOKEN="$bot_token" "$work/adaptations/get-statuses" "$owner" "$repo" "$sha4" | jq -r '[.[] | select(.context == "pump19/review")][0].state')"
combined_state="$(api GET "/api/v1/repos/${owner}/${repo}/commits/${sha4}/status" | jq -r '.state // .status // ""')"
case "$latest_review_state" in
  error) service_combined_state="failure" ;;
  *) service_combined_state="$latest_review_state" ;;
esac
if [[ "$service_combined_state" != "$combined_state" ]]; then
  echo "same-second status ordering disagrees: adaptation=${latest_review_state} combined=${combined_state}" >&2
  exit 1
fi
echo "ok: independently observed same-second status ordering agrees with Forgejo combined status"

# Spike A capture tail: send the extra Forgejo 14.0.5 webhook shapes the
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

for index in {1..35}; do
  api_with_token "$ben_token" POST "/api/v1/repos/${owner}/${repo}/issues/${timeline_pr}/comments" "$(jq -nc --arg body "timeline paging probe ${index}" '{body:$body}')" >/dev/null
done
timeline_total="$(api GET "/api/v1/repos/${owner}/${repo}/issues/${timeline_pr}/timeline" | jq 'length')"
if (( timeline_total <= 30 )); then
  echo "timeline endpoint returned only ${timeline_total} events; unpaged assumption is false" >&2
  exit 1
fi
for index in {1..35}; do
  create_branch_and_pr "paging-pr-${index}" "paging pr ${index}" >/dev/null
done
explicit_pulls_total="$(api GET "/api/v1/repos/${owner}/${repo}/pulls?state=open&limit=50" | jq 'length')"
default_pulls_total="$(api GET "/api/v1/repos/${owner}/${repo}/pulls?state=open" | jq 'length')"
if (( explicit_pulls_total <= 30 )); then
  echo "paging setup has only ${explicit_pulls_total} open pulls; cannot prove default cap" >&2
  exit 1
fi
if (( default_pulls_total != 30 )); then
  echo "pulls endpoint returned ${default_pulls_total} rows by default; expected Forgejo cap of 30" >&2
  exit 1
fi
echo "ok: Forgejo paging assumptions pinned"

echo "Forgejo e2e passed. Work dir: ${work}"
