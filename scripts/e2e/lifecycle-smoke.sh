#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
work="${MINOS_E2E_DIR:-$(mktemp -d)}"
live="${MINOS_E2E_LIVE:-0}"
liveness_threshold="${MINOS_E2E_LIVENESS_THRESHOLD:-10m}"
journeys="${MINOS_E2E_JOURNEYS:-}"
if [[ (",${journeys}," == *",hard-kill,"* || ",${journeys}," == *",durable-kill,"* ||
  ",${journeys}," == *",restart,"*) &&
  -z "${MINOS_E2E_LIVENESS_THRESHOLD:-}" ]]; then
  liveness_threshold="6s"
fi
wait_attempts=300
[[ "$live" = 0 ]] || wait_attempts=900
source "$root/scripts/e2e/resources.sh"
# shellcheck source=scripts/e2e/lifecycle-recovery.sh
source "$root/scripts/e2e/lifecycle-recovery.sh"
declare -a MINOS_E2E_RESOURCE_LOCK_FDS=()
declare -a journey_units=()
evidence_sequence=0
minos_e2e_allocate_resources
container="$MINOS_E2E_RESOLVED_CONTAINER"
port="$MINOS_E2E_RESOLVED_FORGEJO_PORT"
hook_port="$MINOS_E2E_RESOLVED_HOOK_PORT"
image="${MINOS_FORGEJO_IMAGE:-codeberg.org/forgejo/forgejo:14.0.5}"
owner="Minos"
repo="subject"
secret="minos-lifecycle-secret"

cleanup() {
  set +e
  for unit in "${journey_units[@]}"; do
    systemctl --user stop "$unit" >/dev/null 2>&1
  done
  [[ -n "${receiver_pid:-}" ]] && kill "$receiver_pid" >/dev/null 2>&1
  docker rm -f "$container" >/dev/null 2>&1
}
trap cleanup EXIT

api() {
  local method="$1" path="$2" data="${3:-}"
  if [[ -n "$data" ]]; then
    curl -fsS -X "$method" -H "Authorization: token ${token}" -H 'Content-Type: application/json' -d "$data" "http://127.0.0.1:${port}${path}"
  else
    curl -fsS -X "$method" -H "Authorization: token ${token}" "http://127.0.0.1:${port}${path}"
  fi
}

author_api() {
  local method="$1" path="$2" data="${3:-}"
  if [[ -n "$data" ]]; then
    curl -fsS -X "$method" -H "Authorization: token ${author_token}" -H 'Content-Type: application/json' -d "$data" "http://127.0.0.1:${port}${path}"
  else
    curl -fsS -X "$method" -H "Authorization: token ${author_token}" "http://127.0.0.1:${port}${path}"
  fi
}

wait_until() {
  local description="$1"
  shift
  for _ in $(seq 1 "$wait_attempts"); do
    if "$@" >/dev/null 2>&1; then
      printf 'ok: %s\n' "$description"
      return 0
    fi
    sleep 1
  done
  printf 'timed out: %s\n' "$description" >&2
  return 1
}

require() {
  local description="$1"
  shift
  if "$@" >/dev/null 2>&1; then
    printf 'ok: %s\n' "$description"
    return 0
  fi
  printf 'failed: %s\n' "$description" >&2
  return 1
}

journey_enabled() {
  [[ ",${journeys}," == *",$1,"* ]]
}

record_evidence() {
  local source="$1" operation="$2" lineage="$3" outcome="$4" detail="${5:-}"
  evidence_sequence=$((evidence_sequence + 1))
  jq -nc \
    --argjson sequence "$evidence_sequence" \
    --arg monotonic "$(cut -d ' ' -f 1 /proc/uptime)" \
    --arg source "$source" \
    --arg operation "$operation" \
    --arg lineage "$lineage" \
    --arg outcome "$outcome" \
    --arg detail "$detail" \
    '{sequence:$sequence,monotonic_seconds:$monotonic,source:$source,operation:$operation,attempt_lineage:$lineage,outcome:$outcome,detail:$detail}' \
    >>"$work/evidence.jsonl"
}

status_description() {
  local sha="$1"
  api GET "/api/v1/repos/${owner}/${repo}/commits/${sha}/statuses" |
    jq -r '[.[] | select(.context == "Minos")] | if length == 0 then "" else (max_by(.id) | .description) end'
}

status_is() {
  [[ "$(status_description "$1")" == "$2" ]]
}

pr_merged() {
  [[ "$(api GET "/api/v1/repos/${owner}/${repo}/pulls/$1" | jq -r '.merged')" == true ]]
}

review_on_head() {
  local pr="$1" head="$2"
  api GET "/api/v1/repos/${owner}/${repo}/pulls/${pr}/reviews" |
    jq -e --arg head "$head" 'any(.[]; .commit_id == $head and (.user.login // .user.username) == "Minos")' >/dev/null
}

branch_absent() {
  ! api GET "/api/v1/repos/${owner}/${repo}/branches/$1" >/dev/null 2>&1
}

cleanup_reconciled() {
  # An uncertain immediate deletion is deliberately retained for the sweep.
  # Drive that ordinary recovery path before checking desired forge state.
  "$root/minos" sweep --config "$work/config" >/dev/null 2>&1 || true
  branch_absent "$1"
}

eyes_absent() {
  ! api GET "/api/v1/repos/${owner}/${repo}/issues/$1/reactions" |
    jq -e 'any(.[]; .content == "eyes" and (.user.login // .user.username) == "Minos")' >/dev/null
}

attempt_for_pr() {
  local pr="$1"
  find "$work/runs/attempts" -mindepth 2 -maxdepth 2 -type d -path "*--pr${pr}/*" -print -quit
}

instrumented() {
  local attempt
  attempt="$(attempt_for_pr "$1")"
  [[ -n "$attempt" ]] || return 1
  jq -e '.schema == 1 and .artefact_files > 0 and .artefact_bytes > 0 and ((.lead.compaction_events // 0) == 0)' "$attempt/instrumentation.json" >/dev/null || return 1
  [[ "$live" = 0 ]] || [[ -s "$attempt/lifecycle-index.md" ]]
}

stage_admitted() {
  local attempt
  attempt="$(attempt_for_pr "$1")"
  [[ -n "$attempt" ]] && jq -e '.admitted == true' "$attempt/$2-model-admission.json" >/dev/null
}

one_session() {
  [[ "$(find "$work/runs/attempts" -mindepth 2 -maxdepth 2 -type d -path "*--pr$1/*" | wc -l)" -eq 1 ]]
}

send_opened_hook() {
  local pr="$1" payload signature
  payload="$(jq -nc \
    --argjson pull_request "$(api GET "/api/v1/repos/${owner}/${repo}/pulls/${pr}")" \
    --argjson repository "$(api GET "/api/v1/repos/${owner}/${repo}")" \
    '{action:"opened",pull_request:$pull_request,repository:$repository,sender:{login:"Minos"}}')"
  signature="$(printf '%s' "$payload" | openssl dgst -sha256 -hmac "$secret" -hex | awk '{print $NF}')"
  curl -fsS -X POST \
    -H 'Content-Type: application/json' \
    -H 'X-Forgejo-Event: pull_request' \
    -H "X-Forgejo-Signature: ${signature}" \
    --data "$payload" "http://127.0.0.1:${hook_port}/hooks/local" >/dev/null
}

create_pr() {
  local branch="$1"
  (
    cd "$work/subject"
    git checkout -q main
    git checkout -q -b "$branch"
    if [[ "$branch" == stopped-* || "$branch" == missed-webhook-* || "$branch" == hard-kill-* ||
      "$branch" == restart-* || "$branch" == uncertain-* || "$branch" == capacity-* ||
      "$branch" == durable-*-kill-* ]]; then
      sed -i 's/return true/return false/' ready.go
    else
      printf '\n// Clean lifecycle fixture.\n' >>ready.go
    fi
    git add ready.go
    git commit -q -m "test: seed ${branch}"
    git push -q origin "$branch"
  )
  # The service identity must never review its own change.  Creating the PR as
  # the fixture author keeps the disposable journey faithful to that boundary.
  author_api POST "/api/v1/repos/${owner}/${repo}/pulls" "$(jq -nc --arg head "$branch" '{head:$head,base:"main",title:$head}')" | jq -r '.number'
}

post_prior_finding_review() {
  local pr="$1" head="$2" attempt="$3" body
  body="Ready still returns false after repair attempt ${attempt}; callers remain suppressed. Further automated repair is not justified without new evidence.

<!-- Minos: finding=F-E2E head=${head} -->"
  api POST "/api/v1/repos/${owner}/${repo}/pulls/${pr}/reviews" \
    "$(jq -nc --arg head "$head" --arg body "$body" '{event:"REQUEST_CHANGES",commit_id:$head,body:$body,comments:[]}')" >/dev/null
}

seed_stopped_history() {
  local pr="$1" head
  head="$(api GET "/api/v1/repos/${owner}/${repo}/pulls/${pr}" | jq -r '.head.sha')"
  post_prior_finding_review "$pr" "$head" 1
  (
    cd "$work/subject"
    # Two small, explicit failed repair attempts make recurrence a forge fact.
    # The real lead still reviews the final head and judges whether to chase it.
    sed -i 's/return false/ready := false\n\treturn ready/' ready.go
    git add ready.go
    git commit -q -m 'fix: attempt to preserve readiness result'
    git push -q origin stopped-lifecycle
  )
  head="$(api GET "/api/v1/repos/${owner}/${repo}/pulls/${pr}" | jq -r '.head.sha')"
  post_prior_finding_review "$pr" "$head" 2
  (
    cd "$work/subject"
    sed -i 's/ready := false/const ready = false/' ready.go
    git add ready.go
    git commit -q -m 'fix: make readiness result explicit'
    git push -q origin stopped-lifecycle
  )
}

mkdir -p "$work"/{adaptations,bin,config/repos,forgejo/gitea/conf,incidents,logs,runs}
cp -R "$root/scripts/adaptations/forgejo/." "$work/adaptations/"
mv "$work/adaptations/remove-reaction" "$work/adaptations/remove-reaction-real"
cat >"$work/adaptations/remove-reaction" <<'EOF'
#!/usr/bin/env sh
set -eu
fault="$(dirname "$0")/../faults/fail-remove-reaction-once"
if [ -e "$fault" ]; then
  rm -f "$fault"
  exit 1
fi
exec "$(dirname "$0")/remove-reaction-real" "$@"
EOF
chmod +x "$work/adaptations/remove-reaction"
cat >"$work/adaptations/fault-common.sh" <<'EOF'
#!/usr/bin/env sh
# shellcheck source=common.sh
. "$(dirname "$0")/common.sh"

# The disposable rig can apply one guarded write and then make its response
# unavailable. The unchanged guarded verb must discover the forge effect.
api() {
  method="$1"
  path="$2"
  shift 2
  fault_root="$(dirname "$MINOS_CONFIG")/faults"
  drop_response=false
  case "${method}:${path}" in
    POST:*/reviews)
      if [ -e "$fault_root/drop-review-response" ]; then
        rm -f "$fault_root/drop-review-response"
        drop_response=true
      fi
      ;;
    POST:*/statuses/*)
      if [ -e "$fault_root/drop-status-response" ]; then
        rm -f "$fault_root/drop-status-response"
        drop_response=true
      fi
      ;;
  esac
  if ! command curl -fsS \
    -X "$method" \
    -H "Authorization: token ${MINOS_FORGE_TOKEN}" \
    -H "Accept: application/json" \
    "$@" \
    "${MINOS_API_BASE%/}${path}"; then
    return 1
  fi
  if $drop_response; then
    printf '%s %s\n' "$method" "$path" >>"$(dirname "$MINOS_CONFIG")/logs/forge-faults.log"
    return 1
  fi
  return 0
}
EOF
sed -i 's#/common.sh"#/fault-common.sh"#' \
  "$work/adaptations/guarded-post-review" \
  "$work/adaptations/guarded-set-status"

cat >"$work/forgejo/gitea/conf/app.ini" <<EOF
APP_NAME = Minos lifecycle E2E
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
SECRET_KEY = lifecycle-e2e-secret
INTERNAL_TOKEN = lifecycle-e2e-internal
[service]
DISABLE_REGISTRATION = false
REQUIRE_SIGNIN_VIEW = false
EOF

docker rm -f "$container" >/dev/null 2>&1 || true
docker run -d --name "$container" --network host -v "$work/forgejo:/data" \
  -e USER_UID="$(id -u)" -e USER_GID="$(id -g)" "$image" >/dev/null
wait_until 'Forgejo API ready' curl -fsS "http://127.0.0.1:${port}/api/v1/version"
docker exec -u git "$container" forgejo admin user create --username Minos --password password --email minos@example.invalid --admin --must-change-password=false >/dev/null
docker exec -u git "$container" forgejo admin user create --username FixtureAuthor --password password --email author@example.invalid --must-change-password=false >/dev/null
token="$(docker exec -u git "$container" forgejo admin user generate-access-token --username Minos --token-name lifecycle-e2e --scopes 'write:repository,write:issue,write:user' --raw)"
author_token="$(docker exec -u git "$container" forgejo admin user generate-access-token --username FixtureAuthor --token-name lifecycle-author --scopes 'write:repository,write:issue' --raw)"
printf '%s\n' "$token" >"$work/token"
printf '%s\n' "$secret" >"$work/webhook.secret"
api POST /api/v1/user/repos '{"auto_init":true,"default_branch":"main","name":"subject","private":true}' >/dev/null
api PUT "/api/v1/repos/${owner}/${repo}/collaborators/FixtureAuthor" '{"permission":"write"}' >/dev/null

git clone -q "http://Minos:${token}@127.0.0.1:${port}/${owner}/${repo}.git" "$work/subject"
(
  cd "$work/subject"
  git config user.name 'Minos fixture author'
  git config user.email author@example.invalid
  mkdir -p .review
  cat >go.mod <<'EOF'
module example.invalid/lifecycle

go 1.24
EOF
  cat >ready.go <<'EOF'
package lifecycle

func Ready() bool {
	return true
}
EOF
  cat >ready_test.go <<'EOF'
package lifecycle

import "testing"

func TestReady(t *testing.T) {
	if !Ready() {
		t.Fatal("service is not ready")
	}
}
EOF
  cat >.review/ready.md <<'EOF'
# Readiness behaviour

Changes to `Ready` must preserve the tested true result. Returning false is a
P1 correctness defect because callers would suppress ready work.
EOF
  git add .
  git commit -q -m 'test: seed lifecycle subject'
  git push -q origin main
  git remote set-url origin "http://FixtureAuthor:${author_token}@127.0.0.1:${port}/${owner}/${repo}.git"
)

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
max-concurrent = 1
[sweep]
liveness-threshold = "${liveness_threshold}"
log = "${work}/logs/sweep.log"
[scrub]
vars = ["MINOS_FORGE_TOKEN", "ANTHROPIC_API_KEY", "OPENAI_API_KEY"]
EOF

cat >"$work/config/repos/local--Minos--subject.toml" <<EOF
forge = "local"
owner = "Minos"
repo = "subject"
[adaptation]
build = "go build ./..."
test = "go test ./..."
briefs = ".review"
skill = "${root}/skills/foundry/review-panel/SKILL.md"
run-body = "${work}/run-body"
[policy]
auto-merge = true
[[eligibility]]
authors = ["*"]
EOF

cat >"$work/pins.toml" <<'EOF'
[roles.lead]
id = "lead-claude"
engine = "claude"
model = "claude-opus-4-8"
family = "anthropic"
effort = "high"
EOF

cat >"$work/bin/ensemble" <<EOF
#!/bin/sh
exec node /opt/ensemble/dist/src/cli/ensemble.js "\$@"
EOF
chmod +x "$work/bin/ensemble"

cat >"$work/engine-probe" <<EOF
#!/bin/sh
set -eu
if [ "$live" = 0 ]; then exit 0; fi
exec /usr/local/bin/claude -p --model "\$1" --output-format json 'Return exactly OK.'
EOF
chmod +x "$work/engine-probe"

cat >"$work/config/preflight.toml" <<EOF
[forge]
api-base = "http://127.0.0.1:${port}/api/v1"
credential-file = "${work}/token"
expected-login = "Minos"
permission-repository = "Minos/subject"
required-permissions = ["pull", "push"]
[[engine]]
name = "claude"
command = "${work}/engine-probe"
args = ["{model}"]
models = ["claude-opus-4-8"]
[registry]
enabled = false
[alert]
directory = "${work}/incidents"
[toolchain]
binaries = ["${root}/minos", "/usr/local/bin/claude", "${work}/bin/ensemble", "go", "git", "curl", "jq", "python3", "sh"]
EOF

cat >"$work/config/run-body.env" <<EOF
MINOS_ENGINE_LAUNCH_LEAD="${root}/scripts/run-body/launch-claude-lead"
MINOS_ENSEMBLE_LAUNCH="${root}/scripts/run-body/launch-ensemble"
MINOS_ENSEMBLE="${work}/bin/ensemble"
MINOS_PINS="${work}/pins.toml"
MINOS_REVIEW_SCRIPTS="${root}/scripts/review"
MINOS_WORKER_MODEL_POLICY="${work}/worker-models.json"
MINOS_FIX_SKILL="${root}/skills/service/fix/SKILL.md"
MINOS_ROOT_CAUSE_SKILL="${root}/skills/foundry/root-cause/SKILL.md"
MINOS_BIN="${root}/minos"
MINOS_CLAUDE="/usr/local/bin/claude"
MINOS_LIFECYCLE_INSTRUCTION="${root}/lifecycle/lifecycle.md"
MINOS_FIX_AUTHOR_NAME="Minos"
MINOS_FIX_AUTHOR_EMAIL="minos@example.invalid"
EOF
cp "$root/deploy/etc/minos/worker-models.json" "$work/worker-models.json"

cat >"$work/fixture-run-body" <<'EOF'
#!/bin/bash
set -euo pipefail
# The production body loads this file itself.  Keep the fixture at the same
# environment boundary so it exercises the installed command paths as well.
set -a
. "$MINOS_CONFIG/run-body.env"
set +a
minos="${MINOS_BIN}"
control_root="$(dirname "$MINOS_CONFIG")/control"
mkdir -p "$control_root"
if [[ -e "$control_root/hard-kill-predecessor-token" && ! -e "$control_root/hard-kill-successor-allow-begin" ]]; then
  printf '%s\n' "$MINOS_ATTEMPT_TOKEN" >"$control_root/hard-kill-successor-before-begin"
  while [[ ! -e "$control_root/hard-kill-successor-allow-begin" ]]; do sleep 1; done
fi
"$minos" run-guard --config "$MINOS_CONFIG" begin
snapshot="$($minos forge snapshot)"
branch="$(jq -r .head_branch <<<"$snapshot")"
body="$MINOS_RUN_DIR/review.md"
comments="$MINOS_RUN_DIR/comments.json"
printf '[]\n' >"$comments"
if [[ "$branch" == hard-kill-* && ! -e "$control_root/hard-kill-predecessor-token" ]]; then
  printf '%s\n' "$MINOS_ATTEMPT_TOKEN" >"$control_root/hard-kill-predecessor-token"
  printf '%s\n' "$MINOS_ATTEMPT_TOKEN" >"$control_root/hard-kill-predecessor-ready"
  while :; do sleep 1; done
elif [[ "$branch" == restart-* ]]; then
  : >"$control_root/restart-ready"
  while [[ ! -e "$control_root/restart-release" ]]; do sleep 1; done
  printf 'The changed Ready function now returns false, contradicting its tested contract.\n' >"$body"
  "$minos" forge review material "$body" "$comments"
  "$minos" forge status stopped
elif [[ "$branch" == uncertain-review-* ]]; then
  printf 'The changed Ready function now returns false, contradicting its tested contract.\n' >"$body"
  "$minos" forge review material "$body" "$comments"
  : >"$control_root/uncertain-review-applied"
  while [[ ! -e "$control_root/uncertain-review-release" ]]; do sleep 1; done
  "$minos" forge status stopped
elif [[ "$branch" == uncertain-status-* ]]; then
  printf 'The changed Ready function now returns false, contradicting its tested contract.\n' >"$body"
  "$minos" forge review material "$body" "$comments"
  "$minos" forge status stopped
  : >"$control_root/uncertain-status-applied"
  while [[ ! -e "$control_root/uncertain-status-release" ]]; do sleep 1; done
elif [[ "$branch" == capacity-* ]]; then
  : >"$control_root/capacity-${MINOS_PR}-ready"
  while [[ ! -e "$control_root/capacity-${MINOS_PR}-release" ]]; do sleep 1; done
  printf 'The changed Ready function now returns false, contradicting its tested contract.\n' >"$body"
  "$minos" forge review material "$body" "$comments"
  "$minos" forge status stopped
elif [[ "$branch" == durable-*-kill-* ]]; then
  durable_state=stopped
  [[ "$branch" != durable-blocked-kill-* ]] || durable_state=blocked
  printf 'The changed Ready function now returns false, contradicting its tested contract.\n' >"$body"
  "$minos" forge review material "$body" "$comments"
  "$minos" forge status "$durable_state"
  printf '%s\n' "$MINOS_ATTEMPT_TOKEN" >"$control_root/durable-${durable_state}-kill-ready"
  while :; do sleep 1; done
elif [[ "$branch" == stopped-* || "$branch" == missed-webhook-* || "$branch" == hard-kill-* ]]; then
  if [[ "$branch" == missed-webhook-* ]]; then
    : >"$control_root/missed-webhook-ready"
    while [[ ! -e "$control_root/missed-webhook-release" ]]; do sleep 1; done
  elif [[ "$branch" == hard-kill-* ]]; then
    printf '%s\n' "$MINOS_ATTEMPT_TOKEN" >"$control_root/hard-kill-successor-ready"
    while [[ ! -e "$control_root/hard-kill-successor-release" ]]; do sleep 1; done
  fi
  printf 'The changed Ready function now returns false, contradicting its tested contract.\n' >"$body"
  "$minos" forge review material "$body" "$comments"
  "$minos" forge status stopped
else
  "$minos" ws-exec --config "$MINOS_CONFIG" -- sh -lc "$MINOS_BUILD_CMD"
  "$minos" ws-exec --config "$MINOS_CONFIG" -- sh -lc "$MINOS_TEST_CMD"
  printf 'No material findings. The complete changed file and its test were read.\n' >"$body"
  "$minos" forge review converged "$body" "$comments"
  "$minos" forge status clean
  "$minos" run-guard --config "$MINOS_CONFIG" clearance "$MINOS_HEAD_SHA" "$MINOS_TARGET_SHA"
  "$minos" forge merge squash
  "$minos" forge status merged
  "$minos" forge cleanup
fi
printf '%s\n' '{"schema":1,"input_tokens":0,"output_tokens":0,"prompt_start_tokens":0,"prompt_peak_tokens":0,"prompt_growth_tokens":0,"compaction_events":0,"stream_bytes":0}' >"$MINOS_RUN_DIR/lead-metrics.json"
"$minos" run-guard --config "$MINOS_CONFIG" release
EOF
chmod +x "$work/fixture-run-body"

if [[ "$live" = 0 ]]; then
  cp "$work/fixture-run-body" "$work/run-body"
else
  cp "$root/scripts/run-body/run-body" "$work/run-body"
fi
chmod +x "$work/run-body"

"$root/minos" receive --config "$work/config" >"$work/logs/receiver.log" 2>&1 &
receiver_pid="$!"
wait_until 'receiver ready' grep -q 'receiver listening' "$work/logs/receiver.log"

if [[ -z "$journeys" ]]; then
  journeys="clean,stopped"
  [[ "$live" != clean ]] || journeys="clean"
  [[ "$live" != stopped ]] || journeys="stopped"
fi

if journey_enabled clean; then
  clean_pr="$(create_pr clean-lifecycle)"
  clean_sha="$(api GET "/api/v1/repos/${owner}/${repo}/pulls/${clean_pr}" | jq -r '.head.sha')"
  journey_units+=("minos-run-${owner}-${repo}-pr${clean_pr}-${clean_sha:0:12}.service")
  send_opened_hook "$clean_pr"
  wait_until 'clean journey merged' pr_merged "$clean_pr"
  wait_until 'clean journey status merged' status_is "$clean_sha" Merged
  wait_until 'clean source branch deleted' cleanup_reconciled clean-lifecycle
  wait_until 'clean eyes removed' eyes_absent "$clean_pr"
  wait_until 'clean instrumentation written' instrumented "$clean_pr"
  one_session "$clean_pr"
fi

if journey_enabled stopped; then
  stopped_pr="$(create_pr stopped-lifecycle)"
  [[ "$live" != stopped ]] || seed_stopped_history "$stopped_pr"
  stopped_sha="$(api GET "/api/v1/repos/${owner}/${repo}/pulls/${stopped_pr}" | jq -r '.head.sha')"
  journey_units+=("minos-run-${owner}-${repo}-pr${stopped_pr}-${stopped_sha:0:12}.service")
  send_opened_hook "$stopped_pr"
  wait_until 'findings journey stopped' status_is "$stopped_sha" 'Review stopped; findings remain'
  wait_until 'findings current-head review published' review_on_head "$stopped_pr" "$stopped_sha"
  if [[ "$live" = stopped ]]; then
    wait_until 'reviewer models admitted' stage_admitted "$stopped_pr" review
    wait_until 'verifier models admitted' stage_admitted "$stopped_pr" verify
  fi
  wait_until 'findings eyes removed' eyes_absent "$stopped_pr"
  wait_until 'findings instrumentation written' instrumented "$stopped_pr"
  one_session "$stopped_pr"
fi

if journey_enabled missed-webhook; then
  run_missed_webhook_journey
fi

if journey_enabled hard-kill; then
  run_hard_kill_journey
fi

if journey_enabled durable-kill; then
  run_durable_kill_journey stopped
  run_durable_kill_journey blocked
fi

if journey_enabled restart; then
  run_restart_journey
fi

if journey_enabled uncertain-write; then
  run_uncertain_write_journeys
fi

if journey_enabled ingress-failures; then
  run_ingress_failure_journeys
fi

if journey_enabled unmapped-event; then
  run_unmapped_event_journey
fi

if journey_enabled capacity; then
  run_capacity_journey
fi

if journey_enabled closed-before-sweep; then
  run_closed_before_sweep_journey
fi

if journey_enabled receiver-boundaries; then
  run_receiver_boundary_journeys
fi

printf 'Minos lifecycle E2E passed. Evidence: %s\n' "$work"
