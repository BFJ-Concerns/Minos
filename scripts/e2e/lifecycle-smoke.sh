#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
work="${MINOS_E2E_DIR:-$(mktemp -d)}"
live="${MINOS_E2E_LIVE:-0}"
liveness_threshold="${MINOS_E2E_LIVENESS_THRESHOLD:-10m}"
journeys="${MINOS_E2E_JOURNEYS:-}"
if [[ (",${journeys}," == *",hard-kill,"* || ",${journeys}," == *",durable-kill,"* ||
  ",${journeys}," == *",restart,"* || ",${journeys}," == *",post-merge-cleanup,"*) &&
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

pr_status_is() {
  local pr="$1" description="$2" head
  head="$(api GET "/api/v1/repos/${owner}/${repo}/pulls/${pr}" | jq -r '.head.sha')"
  status_is "$head" "$description"
}

bar_digests_differ() {
  local pending="$1" confirmed="$2"
  [[ "$(jq -r .manifest_sha256 "$pending")" != "$(jq -r .manifest_sha256 "$confirmed")" ]]
}

worker_calls_have_one_verification_and_two_bars() {
  local calls="$1"
  [[ "$(grep -c '^verification$' "$calls")" -eq 1 && "$(grep -c '^bar:' "$calls")" -eq 2 ]]
}

pr_merged() {
  [[ "$(api GET "/api/v1/repos/${owner}/${repo}/pulls/$1" | jq -r '.merged')" == true ]]
}

review_on_head() {
  local pr="$1" head="$2"
  api GET "/api/v1/repos/${owner}/${repo}/pulls/${pr}/reviews" |
    jq -e --arg head "$head" 'any(.[]; .commit_id == $head and (.user.login // .user.username) == "Minos")' >/dev/null
}

review_has_complete_disposition_record() {
  local pr="$1" head="$2"
  api GET "/api/v1/repos/${owner}/${repo}/pulls/${pr}/reviews" |
    jq -e --arg head "$head" 'any(.[];
      .commit_id == $head and
      (.user.login // .user.username) == "Minos" and
      (.body | contains("<!-- Minos-Disposition: ")) and
      (.body | test("<!-- Minos: [^\\n]+ -->\\s*$")))' >/dev/null
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
  local pr="$1" output="${2:-/dev/null}" payload signature
  payload="$(jq -nc \
    --argjson pull_request "$(api GET "/api/v1/repos/${owner}/${repo}/pulls/${pr}")" \
    --argjson repository "$(api GET "/api/v1/repos/${owner}/${repo}")" \
    '{action:"opened",pull_request:$pull_request,repository:$repository,sender:{login:"Minos"}}')"
  signature="$(printf '%s' "$payload" | openssl dgst -sha256 -hmac "$secret" -hex | awk '{print $NF}')"
  curl -fsS -X POST \
    -H 'Content-Type: application/json' \
    -H 'X-Forgejo-Event: pull_request' \
    -H "X-Forgejo-Signature: ${signature}" \
    --data "$payload" "http://127.0.0.1:${hook_port}/hooks/local" >"$output"
}

create_pr() {
  local branch="$1"
  (
    cd "$work/subject"
    git checkout -q main
    # Earlier fixture merges move the disposable target. Start every new PR
    # from that current target rather than the clone's original local main.
    git fetch -q origin main
    git reset -q --hard origin/main
    git checkout -q -b "$branch"
    if [[ "$branch" == stopped-* || "$branch" == bar-feedback-* || "$branch" == destination-* || "$branch" == material-destination-* || "$branch" == retryable-exit-* || "$branch" == missed-webhook-* || "$branch" == hard-kill-* ||
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

mkdir -p "$work"/{adaptations,bin,config/repos,destination,forgejo/gitea/conf,incidents,logs,runs}
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
mv "$work/adaptations/delete-branch" "$work/adaptations/delete-branch-real"
cat >"$work/adaptations/delete-branch" <<'EOF'
#!/usr/bin/env sh
set -eu
fault="$(dirname "$0")/../faults/retry-delete-branch"
if [ -e "$fault" ]; then
  printf '%s\n' '{"outcome":"retryable","reason":"branch deletion should be retried"}'
  exit 0
fi
exec "$(dirname "$0")/delete-branch-real" "$@"
EOF
chmod +x "$work/adaptations/delete-branch"
cat >"$work/destination/discover" <<'PY'
#!/usr/bin/env python3
import json
import pathlib
import sys

json.load(sys.stdin)
root = pathlib.Path(__file__).resolve().parent
with (root / "operations.log").open("a", encoding="utf-8") as log:
    log.write("discover\n")
state = root / "record.json"
if not state.exists():
    print(json.dumps({"schema_version": 1, "outcome": "not-found"}))
else:
    record = json.loads(state.read_text(encoding="utf-8"))
    receipt = record["receipt"]
    print(json.dumps({
        "schema_version": 1,
        "outcome": "found",
        "matches": [{
            "provider_record_id": "fixture-record-1",
            "occurrence_id": receipt["occurrence_id"],
            "payload_sha256": receipt["payload_sha256"],
        }],
    }))
PY
cat >"$work/destination/create" <<'PY'
#!/usr/bin/env python3
import json
import pathlib
import sys

request = json.load(sys.stdin)
root = pathlib.Path(__file__).resolve().parent
with (root / "operations.log").open("a", encoding="utf-8") as log:
    log.write("create\n")
(root / "record.json").write_text(json.dumps(request["record"]), encoding="utf-8")
print(json.dumps({"schema_version": 1, "outcome": "created", "provider_record_id": "fixture-record-1"}))
PY
cat >"$work/destination/read" <<'PY'
#!/usr/bin/env python3
import json
import pathlib
import sys

json.load(sys.stdin)
root = pathlib.Path(__file__).resolve().parent
with (root / "operations.log").open("a", encoding="utf-8") as log:
    log.write("read\n")
record = json.loads((root / "record.json").read_text(encoding="utf-8"))
print(json.dumps({
    "schema_version": 1,
    "outcome": "found",
    "record": record,
    "authenticated_principal": "minos-e2e",
    "observed_at": "2026-07-15T12:00:00Z",
}))
PY
chmod +x "$work/destination"/{discover,create,read}
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
[finding-destinations.backlog]
adaptation = "${work}/destination"
endpoint = "fixture://finding-backlog"
credential-file = "${work}/destination.token"
expected-principal = "minos-e2e"
EOF
printf 'disposable-destination-token\n' >"$work/destination.token"

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
[finding-disposition]
mode = "publish-through-p3"
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
MINOS_REVIEW_PANEL_SCRIPTS="${root}/skills/foundry/review-panel/scripts"
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
manifest="$MINOS_RUN_DIR/disposition-manifest.json"
bar="$MINOS_RUN_DIR/bar-attestation.json"

prepare_decision() {
  priority="${1:-}"
  verification="$MINOS_RUN_DIR/verification-result.json"
  if [[ -n "$priority" ]]; then
    line="$(grep -nEm1 'return false|ready := false|const ready = false' "$MINOS_WORKSPACE/ready.go" | cut -d: -f1)"
    python3 - "$MINOS_WORKSPACE/ready.go" "$line" "$priority" >"$verification" <<'PY'
import json
import sys

path, line_raw, priority = sys.argv[1:]
line = int(line_raw)
quote = open(path, encoding="utf-8").read().splitlines()[line - 1]
producer = "ready[1/1]@claude"
candidate = {
    "brief": "ready", "file": "ready.go", "line": line, "side": "RIGHT",
    "priority": priority, "title": "Preserve readiness behaviour",
    "message": "The changed Ready function now returns false, contradicting its tested contract.",
    "code_quote": quote, "producer": producer,
    "producer_identity": producer, "producer_ordinal": 0,
    "assurance": "agent-judgement",
}
verified = dict(candidate)
verified.update({
    "checked_by": "codex", "proposed_priority": priority,
    "verifier_priority": priority, "verification_outcome": "verified",
    "priority_validation": {"agreement": "agreed", "rationale": "The changed behaviour contradicts the trusted test and brief."},
})
print(json.dumps({
    "schema_version": 1,
    "criteria": [{"name": "ready", "path": ".review/ready.md"}],
    "candidates": [{
        "candidate": candidate,
        "producer": {"family": "claude", "id": producer, "ordinal": 0},
        "assurance": "agent-judgement", "outcome": "verified",
        "proposed_priority": priority, "verifier_priority": priority,
        "verified_priority": priority,
        "verification_evidence": {
            "checker_family": "codex", "checker_id": "check:ready[1/1]@claude:0@codex",
            "degraded_pairing": False,
            "rationale": "The changed behaviour contradicts the trusted test and brief.",
        },
        "verified_finding": verified,
    }],
}))
PY
  else
    printf '%s\n' '{"schema_version":1,"criteria":[],"candidates":[]}' >"$verification"
  fi
  "$minos" findings assemble "$verification" >"$manifest"
  digest="$("$minos" findings inspect "$manifest" | jq -r .manifest_sha256)"
  jq -n --arg digest "$digest" '{schema_version:1,manifest_sha256:$digest,verdict:"pass",reasons:[],implicated_briefs:[],candidate_reconsiderations:[],checker:{family:"codex",id:"fixture-bar",degraded_pairing:false}}' >"$bar"
  if [[ -n "$priority" ]]; then
    occurrence="$(jq -r '.findings[0].finding.occurrence_id' "$manifest")"
    jq -n --arg occurrence "$occurrence" --argjson line "$line" '[{occurrence_id:$occurrence,new_position:$line,old_position:0}]' >"$comments"
  else
    printf '[]\n' >"$comments"
  fi
}

publish_material_review() {
  printf 'The changed Ready function now returns false, contradicting its tested contract.\n' >"$body"
  prepare_decision P1
  "$minos" forge review "$body" "$comments" "$manifest" "$bar"
}
if [[ "$branch" == hard-kill-* && ! -e "$control_root/hard-kill-predecessor-token" ]]; then
  printf '%s\n' "$MINOS_ATTEMPT_TOKEN" >"$control_root/hard-kill-predecessor-token"
  printf '%s\n' "$MINOS_ATTEMPT_TOKEN" >"$control_root/hard-kill-predecessor-ready"
  while :; do sleep 1; done
elif [[ "$branch" == restart-* ]]; then
  : >"$control_root/restart-ready"
  while [[ ! -e "$control_root/restart-release" ]]; do sleep 1; done
  publish_material_review
  "$minos" forge status stopped
elif [[ "$branch" == uncertain-review-* ]]; then
  publish_material_review
  : >"$control_root/uncertain-review-applied"
  while [[ ! -e "$control_root/uncertain-review-release" ]]; do sleep 1; done
  "$minos" forge status stopped
elif [[ "$branch" == uncertain-status-* ]]; then
  publish_material_review
  "$minos" forge status stopped
  : >"$control_root/uncertain-status-applied"
  while [[ ! -e "$control_root/uncertain-status-release" ]]; do sleep 1; done
elif [[ "$branch" == capacity-* ]]; then
  : >"$control_root/capacity-${MINOS_PR}-ready"
  while [[ ! -e "$control_root/capacity-${MINOS_PR}-release" ]]; do sleep 1; done
  publish_material_review
  "$minos" forge status stopped
elif [[ "$branch" == durable-*-kill-* ]]; then
  durable_state=stopped
  [[ "$branch" != durable-blocked-kill-* ]] || durable_state=blocked
  publish_material_review
  "$minos" forge status "$durable_state"
  printf '%s\n' "$MINOS_ATTEMPT_TOKEN" >"$control_root/durable-${durable_state}-kill-ready"
  while :; do sleep 1; done
elif [[ "$branch" == retryable-exit-* && ! -e "$control_root/retryable-exit-attempted" ]]; then
  # Reproduce the live attempt shape: no review or terminal product is
  # published, the lead declares a retryable operational failure, and its
  # outer session still returns normally.
  printf '%s\n' "$MINOS_ATTEMPT_TOKEN" >"$control_root/retryable-exit-attempted"
  printf '%s\n' '{"schema":1,"input_tokens":0,"output_tokens":0,"prompt_start_tokens":0,"prompt_peak_tokens":0,"prompt_growth_tokens":0,"compaction_events":0,"stream_bytes":0}' >"$MINOS_RUN_DIR/lead-metrics.json"
  "$minos" run-guard --config "$MINOS_CONFIG" retryable-exit stale-oauth
  exit 0
elif [[ "$branch" == retryable-exit-* ]]; then
  publish_material_review
  "$minos" forge status stopped
elif [[ "$branch" == material-destination-* ]]; then
  publish_material_review
  cp "$manifest" "$MINOS_RUN_DIR/initial-material-manifest.json"
  "$minos" findings repair-plan "$manifest" "$bar" >"$MINOS_RUN_DIR/repair-plan.json"
  old_occurrence="$(jq -r '.findings[0].finding.occurrence_id' "$manifest")"
  old_lineage="$(jq -r '.findings[0].finding.lineage_id' "$manifest")"
  printf 'Preserve the failed readiness result for successor lineage proof.\n' >"$MINOS_RUN_DIR/repair-message.txt"
  "$minos" ws-exec --config "$MINOS_CONFIG" -- sh -lc \
    "sed -i 's/return false/ready := false\\n\\treturn ready/' ready.go"
  push_result="$("$minos" forge push "$branch" 'Minos' 'minos@example.invalid' "$MINOS_RUN_DIR/repair-message.txt")"
  repair_sha="$(jq -r .sha <<<"$push_result")"
  export MINOS_HEAD_SHA="$repair_sha"
  git -C "$MINOS_WORKSPACE" diff "$MINOS_TARGET_SHA...$MINOS_HEAD_SHA" >"$MINOS_DIFF"
  jq --arg repair_sha "$repair_sha" \
    '{schema_version:1,occurrences:[{context:.context,finding:.findings[0].finding,repair_commit_sha:$repair_sha}]}' \
    "$MINOS_RUN_DIR/initial-material-manifest.json" >"$MINOS_RUN_DIR/prior-occurrences.json"
  prepare_decision P1
  jq --arg lineage "$old_lineage" \
    '.candidates[0].candidate.lineage_id=$lineage | .candidates[0].verified_finding.lineage_id=$lineage' \
    "$MINOS_RUN_DIR/verification-result.json" >"$MINOS_RUN_DIR/verification-successor.json"
  "$minos" findings assemble "$MINOS_RUN_DIR/verification-successor.json" "$MINOS_RUN_DIR/prior-occurrences.json" >"$manifest"
  digest="$("$minos" findings inspect "$manifest" | jq -r .manifest_sha256)"
  jq -n --arg digest "$digest" '{schema_version:1,manifest_sha256:$digest,verdict:"pass",reasons:[],implicated_briefs:[],candidate_reconsiderations:[],checker:{family:"codex",id:"fixture-bar-successor",degraded_pairing:false}}' >"$bar"
  new_occurrence="$(jq -r '.findings[0].finding.occurrence_id' "$manifest")"
  test "$old_occurrence" != "$new_occurrence"
  printf 'The repaired head still returns false through a local variable.\n' >"$body"
  "$minos" forge review "$body" "$comments" "$manifest" "$bar"
  "$minos" forge status stopped
elif [[ "$branch" == bar-feedback-* ]]; then
  verification="$MINOS_RUN_DIR/verification-result.json"
  line="$(grep -nEm1 'return false|ready := false|const ready = false' "$MINOS_WORKSPACE/ready.go" | cut -d: -f1)"
  python3 - "$MINOS_WORKSPACE/ready.go" "$line" >"$verification" <<'PY'
import json
import sys

path, line_raw = sys.argv[1:]
line = int(line_raw)
quote = open(path, encoding="utf-8").read().splitlines()[line - 1]

def candidate(producer, priority, title, message):
    return {
        "brief": "ready", "file": "ready.go", "line": line, "side": "RIGHT",
        "priority": priority, "title": title, "message": message,
        "code_quote": quote, "producer": producer,
        "producer_identity": producer, "producer_ordinal": 0,
        "assurance": "agent-judgement",
    }

p2 = candidate(
    "codex-review@codex-cli", "P2", "Expiry remains observable",
    "The direct response path can still expose expired content before the background sweep runs.",
)
p3 = candidate(
    "ready[1/1]@claude", "P3", "Spaced validation form remains accepted",
    "The changed validation claim misses a valid spaced form.",
)
p3_verified = dict(p3)
p3_verified.update({
    "checked_by": "codex", "proposed_priority": "P3",
    "verifier_priority": "P3", "verification_outcome": "verified",
    "priority_validation": {"agreement": "agreed", "rationale": "The minor mismatch is confirmed."},
})
print(json.dumps({
    "schema_version": 1,
    "criteria": [{"name": "ready", "path": ".review/ready.md"}],
    "candidates": [
        {
            "candidate": p2,
            "producer": {"family": "codex", "id": p2["producer"], "ordinal": 0},
            "assurance": "agent-judgement", "outcome": "suppressed",
            "proposed_priority": "P2", "verifier_priority": None,
            "verified_priority": None,
            "verification_evidence": {
                "checker_family": "claude", "checker_id": "check:codex-review@codex-cli:0@claude",
                "degraded_pairing": False,
                "rationale": "The background sweep was incorrectly treated as closing the direct path.",
            },
            "verified_finding": None,
        },
        {
            "candidate": p3,
            "producer": {"family": "claude", "id": p3["producer"], "ordinal": 0},
            "assurance": "agent-judgement", "outcome": "verified",
            "proposed_priority": "P3", "verifier_priority": "P3",
            "verified_priority": "P3",
            "verification_evidence": {
                "checker_family": "codex", "checker_id": "check:ready[1/1]@claude:0@codex",
                "degraded_pairing": False, "rationale": "The minor mismatch is confirmed.",
            },
            "verified_finding": p3_verified,
        },
    ],
}))
PY
  "$minos" findings assemble "$verification" >"$manifest"
  cp "$manifest" "$MINOS_RUN_DIR/initial-disposition-manifest.json"
  initial_digest="$("$minos" findings inspect "$manifest" | jq -r .manifest_sha256)"
  suppressed_candidate="$(jq -r '.candidates[] | select(.outcome == "suppressed") | .candidate.candidate_id' "$manifest")"
  reason='The background sweep cadence does not close the direct response path after expiry.'
  jq -n --arg digest "$initial_digest" --arg candidate "$suppressed_candidate" --arg reason "$reason" \
    '{schema_version:1,manifest_sha256:$digest,verdict:"fail",reasons:[$reason],implicated_briefs:[],candidate_reconsiderations:[{candidate_id:$candidate,reasons:[$reason]}],checker:{family:"codex",id:"fixture-bar-initial",degraded_pairing:false}}' >"$bar"
  cp "$bar" "$MINOS_RUN_DIR/initial-bar-attestation.json"
  jq -n --arg root "$MINOS_WORKSPACE" \
    '{repo_root:$root,mode:"diff",base_ref:"origin/main",warnings:[],briefs:[],codex_review:{name:"codex-review",title:"Codex Review",kind:"external",skip_reason:null,skip_kind:null}}' \
    >"$MINOS_RUN_DIR/panel-plan.json"
  jq -n --slurpfile verification "$verification" --slurpfile manifest "$manifest" --slurpfile attestation "$bar" \
    '{verification_result:$verification[0],disposition_manifest:$manifest[0],bar_attestation:$attestation[0],bar:{ran:true,outcome:"fail",reasons:$attestation[0].reasons,implicated_briefs:[],candidate_reconsiderations:$attestation[0].candidate_reconsiderations},findings:[$verification[0].candidates[]|select(.outcome=="verified")|.verified_finding],suppressed_by_checkers:[{kind:"rejected",finding:$verification[0].candidates[0].candidate}],coverage:[],skipped:[],failures:[],reviews:[],suppressed_by_validator:[],quote_validation:{checked:2,passed:2,suppressed:0,lines_corrected:0},mode:"diff",pr:{}}' \
    >"$MINOS_RUN_DIR/initial-bar-report.json"
  python3 "$MINOS_REVIEW_PANEL_SCRIPTS/plan_remediation.py" \
    "$MINOS_RUN_DIR/panel-plan.json" "$MINOS_RUN_DIR/initial-bar-report.json" \
    >"$MINOS_RUN_DIR/remediation-plan.json"
  printf '%s\n' '{"findings":[],"coverage":[],"skipped":[],"failures":[],"reviews":[],"suppressed_by_validator":[],"quote_validation":{"checked":0,"passed":0,"suppressed":0,"lines_corrected":0}}' \
    >"$MINOS_RUN_DIR/remediation-validated.json"
  python3 "$MINOS_REVIEW_PANEL_SCRIPTS/merge_remediation.py" \
    "$MINOS_RUN_DIR/initial-bar-report.json" "$MINOS_RUN_DIR/remediation-validated.json" \
    >"$MINOS_RUN_DIR/remediation-merged.json"
  python3 "$MINOS_REVIEW_PANEL_SCRIPTS/assemble_verify_input.py" \
    "$MINOS_RUN_DIR/panel-plan.json" "$MINOS_RUN_DIR/remediation-merged.json" \
    >"$MINOS_RUN_DIR/successor-verification-input.json"
  jq -e '.findings | length == 2' "$MINOS_RUN_DIR/successor-verification-input.json" >/dev/null
  jq -e '.candidate_reconsiderations | length == 1' "$MINOS_RUN_DIR/successor-verification-input.json" >/dev/null
  python3 - "$verification" >"$MINOS_RUN_DIR/verification-successor.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    result = json.load(handle)
entry = result["candidates"][0]
candidate = entry["candidate"]
verified = dict(candidate)
verified.update({
    "checked_by": "claude", "proposed_priority": "P2",
    "verifier_priority": "P2", "verification_outcome": "verified",
    "priority_validation": {"agreement": "agreed", "rationale": "Fresh inspection confirms the direct expiry window."},
})
entry.update({
    "outcome": "verified", "verifier_priority": "P2", "verified_priority": "P2",
    "verification_evidence": {
        "checker_family": "claude", "checker_id": "check:codex-review@codex-cli:0@claude",
        "degraded_pairing": False, "rationale": "Fresh inspection confirms the direct expiry window.",
    },
    "verified_finding": verified,
})
print(json.dumps(result))
PY
  "$minos" findings assemble "$MINOS_RUN_DIR/verification-successor.json" >"$manifest"
  successor_digest="$("$minos" findings inspect "$manifest" | jq -r .manifest_sha256)"
  test "$initial_digest" != "$successor_digest"
  jq -n --arg digest "$successor_digest" \
    '{schema_version:1,manifest_sha256:$digest,verdict:"pass",reasons:[],implicated_briefs:[],candidate_reconsiderations:[],checker:{family:"codex",id:"fixture-bar-successor",degraded_pairing:false}}' >"$bar"
  jq '[.findings[] | {occurrence_id:.finding.occurrence_id,new_position:.finding.anchor.display_line,old_position:0}]' "$manifest" >"$comments"
  printf 'The consolidated review retains both independently verified documentation concerns after the bar critique was addressed.\n' >"$body"
  "$minos" forge review "$body" "$comments" "$manifest" "$bar"
  "$minos" forge status clean
elif [[ "$branch" == destination-* ]]; then
  printf 'No material findings. Quiet findings were reconciled with the configured durable destination.\n' >"$body"
  prepare_decision P2
  printf '[]\n' >"$comments"
  cp "$manifest" "$MINOS_RUN_DIR/pending-manifest.json"
  cp "$bar" "$MINOS_RUN_DIR/pending-bar-attestation.json"
  printf 'verification\n' >>"$control_root/destination-worker-calls"
  printf 'bar:%s\n' "$(jq -r .manifest_sha256 "$bar")" >>"$control_root/destination-worker-calls"
  "$minos" findings deliver "$manifest" "$bar" >"$MINOS_RUN_DIR/confirmed-manifest.json"
  "$minos" findings deliver "$manifest" "$bar" >"$MINOS_RUN_DIR/reconciled-manifest.json"
  cmp "$MINOS_RUN_DIR/confirmed-manifest.json" "$MINOS_RUN_DIR/reconciled-manifest.json"
  cp "$MINOS_RUN_DIR/confirmed-manifest.json" "$manifest"
  digest="$("$minos" findings inspect "$manifest" | jq -r .manifest_sha256)"
  jq -n --arg digest "$digest" '{schema_version:1,manifest_sha256:$digest,verdict:"pass",reasons:[],implicated_briefs:[],candidate_reconsiderations:[],checker:{family:"codex",id:"fixture-bar-final",degraded_pairing:false}}' >"$bar"
  printf 'bar:%s\n' "$digest" >>"$control_root/destination-worker-calls"
  "$minos" forge review "$body" "$comments" "$manifest" "$bar"
  "$minos" forge status clean
elif [[ "$branch" == stopped-* || "$branch" == missed-webhook-* || "$branch" == hard-kill-* ]]; then
  if [[ "$branch" == missed-webhook-* ]]; then
    : >"$control_root/missed-webhook-ready"
    while [[ ! -e "$control_root/missed-webhook-release" ]]; do sleep 1; done
  elif [[ "$branch" == hard-kill-* ]]; then
    printf '%s\n' "$MINOS_ATTEMPT_TOKEN" >"$control_root/hard-kill-successor-ready"
    while [[ ! -e "$control_root/hard-kill-successor-release" ]]; do sleep 1; done
  fi
  publish_material_review
  "$minos" forge status stopped
else
  "$minos" ws-exec --config "$MINOS_CONFIG" -- sh -lc "$MINOS_BUILD_CMD"
  "$minos" ws-exec --config "$MINOS_CONFIG" -- sh -lc "$MINOS_TEST_CMD"
  printf 'No material findings. The complete changed file and its test were read.\n' >"$body"
  prepare_decision
  "$minos" forge review "$body" "$comments" "$manifest" "$bar"
  "$minos" forge status clean
  "$minos" run-guard --config "$MINOS_CONFIG" clearance "$MINOS_HEAD_SHA" "$MINOS_TARGET_SHA"
  "$minos" forge merge squash
  # The confirmed service-authored merge advances the target. Carry that
  # movement through the ledger before terminal presentation and teardown.
  merged_snapshot="$($minos forge snapshot)"
  merged_head="$(jq -r .head_sha <<<"$merged_snapshot")"
  merged_target="$(jq -r .target_sha <<<"$merged_snapshot")"
  "$minos" run-guard --config "$MINOS_CONFIG" advance \
    "$MINOS_HEAD_SHA" "$MINOS_TARGET_SHA" "$merged_head" "$merged_target"
  "$minos" forge status merged
  if [[ "$branch" == cleanup-stranded-* ]]; then
    : >"$control_root/cleanup-stranded-ready"
    while :; do sleep 1; done
  fi
  # Cleanup recovery fixtures stop after the merge obligation is durable. The
  # harness then changes the branch state and lets the ordinary sweep decide.
  [[ "$branch" == cleanup-* ]] || "$minos" forge cleanup
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
  journeys="clean,stopped,retryable-exit,bar-feedback,destination,material-destination"
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
  wait_until 'clean review carries complete disposition record' review_has_complete_disposition_record "$clean_pr" "$clean_sha"
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
  wait_until 'findings review carries complete disposition record' review_has_complete_disposition_record "$stopped_pr" "$stopped_sha"
  if [[ "$live" = stopped ]]; then
    wait_until 'reviewer models admitted' stage_admitted "$stopped_pr" review
    wait_until 'verifier models admitted' stage_admitted "$stopped_pr" verify
  fi
  wait_until 'findings eyes removed' eyes_absent "$stopped_pr"
  wait_until 'findings instrumentation written' instrumented "$stopped_pr"
  one_session "$stopped_pr"
fi

if journey_enabled retryable-exit; then
  run_retryable_exit_journey
fi

if journey_enabled bar-feedback; then
  bar_feedback_pr="$(create_pr bar-feedback-lifecycle)"
  bar_feedback_sha="$(api GET "/api/v1/repos/${owner}/${repo}/pulls/${bar_feedback_pr}" | jq -r '.head.sha')"
  journey_units+=("minos-run-${owner}-${repo}-pr${bar_feedback_pr}-${bar_feedback_sha:0:12}.service")
  send_opened_hook "$bar_feedback_pr"
  wait_until 'bar-feedback journey converges after fresh verification and bar' status_is "$bar_feedback_sha" 'Changes approved'
  wait_until 'bar-feedback consolidated review is current' review_on_head "$bar_feedback_pr" "$bar_feedback_sha"
  wait_until 'bar-feedback journey releases its lease' lease_absent "$bar_feedback_pr"
  bar_feedback_attempt="$(attempt_for_pr "$bar_feedback_pr")"
  require 'bar-feedback initial result has the PR27 suppression shape' \
    jq -e '[.candidates[].outcome] == ["suppressed","verified"]' \
    "$bar_feedback_attempt/initial-disposition-manifest.json"
  require 'bar-feedback successor is exhaustive and retains both findings' \
    jq -e '.candidates | length == 2 and ([.[].outcome] == ["verified","verified"])' \
    "$bar_feedback_attempt/disposition-manifest.json"
  require 'bar-feedback successor preserves exact candidate identities' \
    jq -e -s '[.[0].candidates[].candidate.candidate_id] == [.[1].candidates[].candidate.candidate_id]' \
    "$bar_feedback_attempt/initial-disposition-manifest.json" "$bar_feedback_attempt/disposition-manifest.json"
  require 'bar-feedback prior failed bar cannot stand for the successor' \
    bar_digests_differ "$bar_feedback_attempt/initial-bar-attestation.json" "$bar_feedback_attempt/bar-attestation.json"
  require 'bar-feedback successor has a fresh passing attestation' \
    jq -e '.verdict == "pass" and .candidate_reconsiderations == []' \
    "$bar_feedback_attempt/bar-attestation.json"
  require 'bar-feedback current review carries the complete successor index' \
    review_has_complete_disposition_record "$bar_feedback_pr" "$bar_feedback_sha"
fi

if journey_enabled destination || journey_enabled material-destination; then
  destination_profile="$work/config/repos/local--Minos--subject.toml"
  sed -i 's/mode = "publish-through-p3"/mode = "destination"\ndestination = "backlog"\ntarget = "Minos\/subject"/' "$destination_profile"
fi

if journey_enabled destination; then
  destination_pr="$(create_pr destination-lifecycle)"
  destination_sha="$(api GET "/api/v1/repos/${owner}/${repo}/pulls/${destination_pr}" | jq -r '.head.sha')"
  journey_units+=("minos-run-${owner}-${repo}-pr${destination_pr}-${destination_sha:0:12}.service")
  send_opened_hook "$destination_pr"
  wait_until 'destination journey converges after authenticated delivery' status_is "$destination_sha" 'Changes approved'
  wait_until 'destination review carries complete disposition record' review_has_complete_disposition_record "$destination_pr" "$destination_sha"
  wait_until 'destination journey releases its lease' lease_absent "$destination_pr"
  destination_attempt="$(attempt_for_pr "$destination_pr")"
  require 'destination retry creates one durable record' test "$(grep -c '^create$' "$work/destination/operations.log")" -eq 1
  require 'destination retry begins with discovery and authenticated read-back' \
    test "$(grep -c '^discover$' "$work/destination/operations.log")" -eq 3
  require 'destination retry authenticates both complete read-backs' \
    test "$(grep -c '^read$' "$work/destination/operations.log")" -eq 2
  require 'destination confirmation preserves the exhaustive candidate set' \
    jq -e -s '.[0].candidates == .[1].candidates and .[0].findings[0].delivery == "pending" and .[1].findings[0].delivery == "confirmed" and .[1].findings[0].receipt != null' \
    "$destination_attempt/pending-manifest.json" "$destination_attempt/confirmed-manifest.json"
  require 'destination final bar re-attests the changed full manifest digest' \
    bar_digests_differ \
    "$destination_attempt/pending-bar-attestation.json" "$destination_attempt/bar-attestation.json"
  require 'destination second bar does not rerun finding verification' \
    worker_calls_have_one_verification_and_two_bars \
    "$work/control/destination-worker-calls"
fi

if journey_enabled material-destination; then
  material_destination_pr="$(create_pr material-destination-lifecycle)"
  material_destination_initial_sha="$(api GET "/api/v1/repos/${owner}/${repo}/pulls/${material_destination_pr}" | jq -r '.head.sha')"
  journey_units+=("minos-run-${owner}-${repo}-pr${material_destination_pr}-${material_destination_initial_sha:0:12}.service")
  send_opened_hook "$material_destination_pr"
  wait_until 'material destination successor stops on its fresh review' \
    pr_status_is "$material_destination_pr" 'Review stopped; findings remain'
  material_destination_final_sha="$(api GET "/api/v1/repos/${owner}/${repo}/pulls/${material_destination_pr}" | jq -r '.head.sha')"
  require 'material destination repair advances the head' \
    test "$material_destination_initial_sha" != "$material_destination_final_sha"
  wait_until 'material destination successor review is current' \
    review_on_head "$material_destination_pr" "$material_destination_final_sha"
  wait_until 'material destination journey releases its lease' lease_absent "$material_destination_pr"
  material_destination_attempt="$(attempt_for_pr "$material_destination_pr")"
  require 'material repair plan consumes the original decision occurrence' \
    jq -e -s '.[0].findings[0].finding.occurrence_id == .[1].findings[0].occurrence_id' \
    "$material_destination_attempt/initial-material-manifest.json" "$material_destination_attempt/repair-plan.json"
  require 'repair successor keeps lineage and receives a new occurrence' \
    jq -e -s '.[0].findings[0].finding.lineage_id == .[1].findings[0].finding.lineage_id and .[0].findings[0].finding.occurrence_id != .[1].findings[0].finding.occurrence_id' \
    "$material_destination_attempt/initial-material-manifest.json" "$material_destination_attempt/disposition-manifest.json"
  require 'repair successor appends head-bound repair evidence' \
    jq -e --arg prior "$(jq -r '.findings[0].finding.occurrence_id' "$material_destination_attempt/initial-material-manifest.json")" \
    --arg repair "$material_destination_final_sha" \
    ".repair_evidence == [{prior_occurrence_id:\$prior,lineage_id:.findings[0].finding.lineage_id,repair_commit_sha:\$repair}]" \
    "$material_destination_attempt/disposition-manifest.json"
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

if journey_enabled post-merge-cleanup; then
  run_post_merge_cleanup_journey
fi

printf 'Minos lifecycle E2E passed. Evidence: %s\n' "$work"
