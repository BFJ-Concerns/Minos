#!/usr/bin/env bash

# Recovery journeys share the disposable lifecycle rig assembled by
# lifecycle-smoke.sh. Keeping their controls here makes each new drill additive
# without creating a second forge or coordination authority.

attempt_count() {
  [[ -d "$work/runs/attempts" ]] || { printf '0\n'; return; }
  find "$work/runs/attempts" -mindepth 2 -maxdepth 2 -type d -path "*--pr$1/*" | wc -l
}

attempt_dir() {
  local pr="$1" token="$2"
  find "$work/runs/attempts" -mindepth 2 -maxdepth 2 -type d -path "*--pr${pr}/${token}" -print -quit
}

lease_json() {
  local pr="$1" result
  [[ -e "$work/runs/coordination.db" ]] || { printf '[]\n'; return; }
  result="$(sqlite3 -json "$work/runs/coordination.db" \
    "SELECT token,observed_head,observed_target,heartbeat_at,unit,workspace FROM leases WHERE forge='local' AND owner='${owner}' AND repo='${repo}' AND pr='${pr}'")"
  printf '%s\n' "${result:-[]}"
}

lease_absent() {
  [[ "$(lease_json "$1")" == "[]" ]]
}

lease_token_is() {
  [[ "$(lease_json "$1" | jq -r '.[0].token // 0')" == "$2" ]]
}

lease_heartbeat() {
  lease_json "$1" | jq -r '.[0].heartbeat_at // ""'
}

heartbeat_differs() {
  [[ -n "$(lease_heartbeat "$1")" && "$(lease_heartbeat "$1")" != "$2" ]]
}

no_product_status() {
  [[ -z "$(status_description "$1")" ]]
}

no_operational_chatter() {
  local pr="$1" head="$2" combined
  combined="$({
    api GET "/api/v1/repos/${owner}/${repo}/pulls/${pr}/reviews" | jq -r '.[].body // ""'
    api GET "/api/v1/repos/${owner}/${repo}/issues/${pr}/comments" | jq -r '.[].body // ""'
    api GET "/api/v1/repos/${owner}/${repo}/commits/${head}/statuses" | jq -r '.[].description // ""'
  } 2>/dev/null)"
  ! grep -Eqi 'hard[- ]?kill|cgroup|stale lease|attempt token|incident|stack trace|credential failure' <<<"$combined"
}

eyes_present() {
  api GET "/api/v1/repos/${owner}/${repo}/issues/$1/reactions" |
    jq -e 'any(.[]; .content == "eyes" and (.user.login // .user.username) == "Minos")' >/dev/null
}

unit_not_running() {
  local state
  state="$(systemctl --user show "$1" --property=ActiveState --value 2>/dev/null || true)"
  [[ -z "$state" || "$state" == inactive || "$state" == failed ]]
}

unit_state() {
  systemctl --user show "$1" --property=ActiveState --property=SubState --property=ControlGroup 2>/dev/null |
    tr '\n' ';' || true
}

attempt_target_is() {
  local dir
  dir="$(attempt_dir "$1" "$2")"
  [[ -n "$dir" ]] && grep -Fxq "MINOS_TARGET_SHA=$3" "$dir/meta.env"
}

attempt_count_is() {
  [[ "$(attempt_count "$1")" -eq "$2" ]]
}

token_greater_than() {
  (( $1 > $2 ))
}

review_count() {
  api GET "/api/v1/repos/${owner}/${repo}/pulls/$1/reviews" |
    jq --arg login Minos '[.[] | select((.user.login // .user.username) == $login)] | length'
}

status_description_count() {
  api GET "/api/v1/repos/${owner}/${repo}/commits/$1/statuses" |
    jq --arg description "$2" '[.[] | select(.context == "Minos" and .description == $description)] | length'
}

count_is() {
  [[ "$1" -eq "$2" ]]
}

run_missed_webhook_journey() {
  local missed_pr missed_sha missed_lease
  missed_pr="$(create_pr missed-webhook-lifecycle)"
  missed_sha="$(api GET "/api/v1/repos/${owner}/${repo}/pulls/${missed_pr}" | jq -r '.head.sha')"
  journey_units+=("minos-run-${owner}-${repo}-pr${missed_pr}-${missed_sha:0:12}.service")
  record_evidence harness suppress none observed "PR ${missed_pr} opening webhook deliberately not delivered"
  sleep 2
  require 'missed-webhook quiet interval has no lease' lease_absent "$missed_pr"
  require 'missed-webhook quiet interval has no attempt' attempt_count_is "$missed_pr" 0
  require 'missed-webhook quiet interval has no eyes' eyes_absent "$missed_pr"
  require 'missed-webhook quiet interval has no product status' no_product_status "$missed_sha"
  record_evidence forge observe none observed "head=${missed_sha}; lease=absent; eyes=absent; status=absent"

  "$root/minos" sweep --config "$work/config"
  record_evidence sweep reconcile none applied "manual sweep over suppressed PR ${missed_pr}"
  wait_until 'missed-webhook sweep launches an owner' test -e "$work/control/missed-webhook-ready"
  missed_lease="$(lease_json "$missed_pr")"
  require 'missed-webhook lease is present while work is held' lease_token_is "$missed_pr" "$(jq -r '.[0].token' <<<"$missed_lease")"
  require 'missed-webhook eyes are present only while held' eyes_present "$missed_pr"
  record_evidence ledger admit successor applied "${missed_lease}"
  : >"$work/control/missed-webhook-release"
  wait_until 'missed-webhook journey stops normally' status_is "$missed_sha" 'Review stopped; findings remain'
  wait_until 'missed-webhook review is on current head' review_on_head "$missed_pr" "$missed_sha"
  wait_until 'missed-webhook eyes are removed' eyes_absent "$missed_pr"
  wait_until 'missed-webhook lease is released' lease_absent "$missed_pr"
  require 'missed-webhook has exactly one attempt' attempt_count_is "$missed_pr" 1
  record_evidence wrapper close successor applied "terminal=stopped; eyes=absent; lease=absent; attempts=1"

  "$root/minos" sweep --config "$work/config"
  require 'second missed-webhook sweep does not relaunch' attempt_count_is "$missed_pr" 1
  record_evidence sweep reconcile no-owner skipped "unchanged terminal product remained at one attempt"
  author_api PATCH "/api/v1/repos/${owner}/${repo}/pulls/${missed_pr}" '{"state":"closed"}' >/dev/null
  record_evidence forge close no-owner applied "completed missed-webhook fixture closed before the next drill"
}

run_hard_kill_journey() {
  local hardkill_pr hardkill_sha hardkill_unit predecessor_token predecessor_lease
  local first_heartbeat second_heartbeat third_heartbeat hardkill_target successor_token
  local statuses_before statuses_after
  hardkill_pr="$(create_pr hard-kill-lifecycle)"
  hardkill_sha="$(api GET "/api/v1/repos/${owner}/${repo}/pulls/${hardkill_pr}" | jq -r '.head.sha')"
  hardkill_unit="minos-run-${owner}-${repo}-pr${hardkill_pr}-${hardkill_sha:0:12}.service"
  journey_units+=("$hardkill_unit")
  send_opened_hook "$hardkill_pr"
  record_evidence receiver delivery none applied "authenticated opened webhook for PR ${hardkill_pr}"
  wait_until 'hard-kill predecessor reaches held checkpoint' test -e "$work/control/hard-kill-predecessor-ready"
  predecessor_token="$(<"$work/control/hard-kill-predecessor-token")"
  require 'hard-kill predecessor owns the lease' lease_token_is "$hardkill_pr" "$predecessor_token"
  require 'hard-kill predecessor publishes eyes' eyes_present "$hardkill_pr"
  first_heartbeat="$(lease_heartbeat "$hardkill_pr")"
  wait_until 'hard-kill predecessor renews first heartbeat' heartbeat_differs "$hardkill_pr" "$first_heartbeat"
  second_heartbeat="$(lease_heartbeat "$hardkill_pr")"
  wait_until 'hard-kill predecessor renews second heartbeat' heartbeat_differs "$hardkill_pr" "$second_heartbeat"
  third_heartbeat="$(lease_heartbeat "$hardkill_pr")"
  predecessor_lease="$(lease_json "$hardkill_pr")"
  record_evidence ledger heartbeat predecessor applied "first=${first_heartbeat}; second=${second_heartbeat}; third=${third_heartbeat}; lease=${predecessor_lease}"

  systemctl --user kill --kill-whom=all --signal=SIGKILL "$hardkill_unit"
  wait_until 'hard-killed predecessor unit is not running' unit_not_running "$hardkill_unit"
  require 'hard-killed predecessor lease survives process death' lease_token_is "$hardkill_pr" "$predecessor_token"
  record_evidence harness kill predecessor applied "unit=${hardkill_unit}; state=$(unit_state "$hardkill_unit"); lease-retained=true"

  (
    cd "$work/subject"
    git checkout -q main
    printf '\n// Target moved while predecessor was dead.\n' >>ready.go
    git add ready.go
    git commit -q -m 'test: advance recovery target'
    git push -q origin main
  )
  hardkill_target="$(api GET "/api/v1/repos/${owner}/${repo}/branches/main" | jq -r '.commit.id')"
  record_evidence forge move-target no-owner applied "target=${hardkill_target}"

  sleep 7
  "$root/minos" sweep --config "$work/config"
  wait_until 'hard-kill successor is held before claiming eyes' test -e "$work/control/hard-kill-successor-before-begin"
  successor_token="$(<"$work/control/hard-kill-successor-before-begin")"
  require 'hard-kill successor receives a higher token' token_greater_than "$successor_token" "$predecessor_token"
  require 'hard-kill successor owns the lease' lease_token_is "$hardkill_pr" "$successor_token"
  require 'predecessor eyes are absent before successor begins' eyes_absent "$hardkill_pr"
  record_evidence sweep replace successor applied "predecessor=${predecessor_token}; successor=${successor_token}; target=${hardkill_target}; eyes-before-successor=absent"
  : >"$work/control/hard-kill-successor-allow-begin"
  wait_until 'hard-kill successor reaches claimed checkpoint' test -e "$work/control/hard-kill-successor-ready"
  require 'hard-kill successor observes the moved target' attempt_target_is "$hardkill_pr" "$successor_token" "$hardkill_target"
  require 'hard-kill successor publishes eyes' eyes_present "$hardkill_pr"
  record_evidence forge claim successor applied "eyes-after-successor=present"

  statuses_before="$(api GET "/api/v1/repos/${owner}/${repo}/commits/${hardkill_sha}/statuses" | jq 'length')"
  if env \
    MINOS_CONFIG="$work/config" MINOS_FORGE=local MINOS_OWNER="$owner" MINOS_REPO_NAME="$repo" \
    MINOS_PR="$hardkill_pr" MINOS_HEAD_SHA="$hardkill_sha" MINOS_TARGET_SHA="$(jq -r '.[0].observed_target' <<<"$predecessor_lease")" \
    MINOS_ATTEMPT_TOKEN="$predecessor_token" \
    "$root/minos" forge status partial >"$work/logs/stale-predecessor.out" 2>"$work/logs/stale-predecessor.err"; then
    printf 'failed: stale predecessor mutation unexpectedly succeeded\n' >&2
    exit 1
  fi
  statuses_after="$(api GET "/api/v1/repos/${owner}/${repo}/commits/${hardkill_sha}/statuses" | jq 'length')"
  [[ "$statuses_after" == "$statuses_before" ]]
  record_evidence forge guarded-status predecessor rejected "status-count-before=${statuses_before}; status-count-after=${statuses_after}; error=$(tr '\n' ' ' <"$work/logs/stale-predecessor.err")"

  : >"$work/control/hard-kill-successor-release"
  wait_until 'hard-kill successor settles stopped product state' status_is "$hardkill_sha" 'Review stopped; findings remain'
  wait_until 'hard-kill successor review is on current head' review_on_head "$hardkill_pr" "$hardkill_sha"
  wait_until 'hard-kill recovery removes eyes' eyes_absent "$hardkill_pr"
  wait_until 'hard-kill recovery releases lease' lease_absent "$hardkill_pr"
  require 'hard-kill recovery has exactly two attempts' attempt_count_is "$hardkill_pr" 2
  require 'hard-kill recovery leaves no operational chatter on the PR' no_operational_chatter "$hardkill_pr" "$hardkill_sha"
  "$root/minos" sweep --config "$work/config"
  require 'final hard-kill sweep is stable' attempt_count_is "$hardkill_pr" 2
  require 'final hard-kill sweep leaves no lease' lease_absent "$hardkill_pr"
  record_evidence wrapper close successor applied "terminal=stopped; eyes=absent; lease=absent; attempts=2; pr-operational-chatter=absent; incident-assertion=deferred"
}

run_restart_journey() {
  local restart_pr restart_sha restart_token heartbeat_before heartbeat_after
  restart_pr="$(create_pr restart-lifecycle)"
  restart_sha="$(api GET "/api/v1/repos/${owner}/${repo}/pulls/${restart_pr}" | jq -r '.head.sha')"
  journey_units+=("minos-run-${owner}-${repo}-pr${restart_pr}-${restart_sha:0:12}.service")
  send_opened_hook "$restart_pr"
  wait_until 'restart journey reaches live checkpoint' test -e "$work/control/restart-ready"
  restart_token="$(lease_json "$restart_pr" | jq -r '.[0].token')"
  require 'restart journey owns one live lease' lease_token_is "$restart_pr" "$restart_token"
  require 'restart journey publishes eyes' eyes_present "$restart_pr"
  require 'restart journey has one attempt before restart' attempt_count_is "$restart_pr" 1
  heartbeat_before="$(lease_heartbeat "$restart_pr")"

  kill "$receiver_pid"
  wait "$receiver_pid" 2>/dev/null || true
  mv "$work/logs/receiver.log" "$work/logs/receiver-before-restart.log"
  "$root/minos" receive --config "$work/config" >"$work/logs/receiver.log" 2>&1 &
  receiver_pid="$!"
  wait_until 'restarted receiver is ready' grep -q 'receiver listening' "$work/logs/receiver.log"
  wait_until 'live attempt heartbeat continues across receiver restart' heartbeat_differs "$restart_pr" "$heartbeat_before"
  heartbeat_after="$(lease_heartbeat "$restart_pr")"
  record_evidence receiver restart predecessor applied "token=${restart_token}; heartbeat-before=${heartbeat_before}; heartbeat-after=${heartbeat_after}"

  send_opened_hook "$restart_pr"
  "$root/minos" sweep --config "$work/config"
  require 'duplicate ingress preserves original token' lease_token_is "$restart_pr" "$restart_token"
  require 'duplicate ingress preserves one attempt' attempt_count_is "$restart_pr" 1
  require 'duplicate ingress preserves eyes' eyes_present "$restart_pr"
  record_evidence reconcile live predecessor skipped "duplicate-webhook=true; sweep=true; token=${restart_token}; attempts=1"

  : >"$work/control/restart-release"
  wait_until 'restart journey settles stopped product state' status_is "$restart_sha" 'Review stopped; findings remain'
  wait_until 'restart journey removes eyes' eyes_absent "$restart_pr"
  wait_until 'restart journey releases lease' lease_absent "$restart_pr"
  require 'restart journey closes its original attempt only' attempt_count_is "$restart_pr" 1
  record_evidence wrapper close predecessor applied "terminal=stopped; eyes=absent; lease=absent; attempts=1"
  author_api PATCH "/api/v1/repos/${owner}/${repo}/pulls/${restart_pr}" '{"state":"closed"}' >/dev/null
}

run_uncertain_write_journeys() {
  local review_pr review_sha status_pr status_sha
  mkdir -p "$work/faults"

  review_pr="$(create_pr uncertain-review-lifecycle)"
  review_sha="$(api GET "/api/v1/repos/${owner}/${repo}/pulls/${review_pr}" | jq -r '.head.sha')"
  journey_units+=("minos-run-${owner}-${repo}-pr${review_pr}-${review_sha:0:12}.service")
  : >"$work/faults/drop-review-response"
  send_opened_hook "$review_pr"
  wait_until 'lost-response review is durably visible' test -e "$work/control/uncertain-review-applied"
  require 'lost-response review is posted exactly once' count_is "$(review_count "$review_pr")" 1
  require 'lost-response review retains its live owner' eyes_present "$review_pr"
  record_evidence forge post-review predecessor applied "response=dropped; discovered=true; review-count=1"
  : >"$work/control/uncertain-review-release"
  wait_until 'lost-response review journey settles stopped' status_is "$review_sha" 'Review stopped; findings remain'
  wait_until 'lost-response review journey releases lease' lease_absent "$review_pr"
  require 'lost-response review journey remains one attempt' attempt_count_is "$review_pr" 1
  author_api PATCH "/api/v1/repos/${owner}/${repo}/pulls/${review_pr}" '{"state":"closed"}' >/dev/null

  status_pr="$(create_pr uncertain-status-lifecycle)"
  status_sha="$(api GET "/api/v1/repos/${owner}/${repo}/pulls/${status_pr}" | jq -r '.head.sha')"
  journey_units+=("minos-run-${owner}-${repo}-pr${status_pr}-${status_sha:0:12}.service")
  : >"$work/faults/drop-status-response"
  send_opened_hook "$status_pr"
  wait_until 'lost-response status is durably visible' test -e "$work/control/uncertain-status-applied"
  require 'lost-response stopped status is posted exactly once' count_is "$(status_description_count "$status_sha" 'Review stopped; findings remain')" 1
  require 'lost-response status review is posted exactly once' count_is "$(review_count "$status_pr")" 1
  require 'lost-response status retains its live owner' eyes_present "$status_pr"
  record_evidence forge set-status predecessor applied "response=dropped; discovered=true; stopped-status-count=1; review-count=1"
  : >"$work/control/uncertain-status-release"
  wait_until 'lost-response status journey removes eyes' eyes_absent "$status_pr"
  wait_until 'lost-response status journey releases lease' lease_absent "$status_pr"
  require 'lost-response status journey remains one attempt' attempt_count_is "$status_pr" 1
  record_evidence wrapper close predecessor applied "review-and-status-durable=true; duplicate-writes=0; attempts=1"
  author_api PATCH "/api/v1/repos/${owner}/${repo}/pulls/${status_pr}" '{"state":"closed"}' >/dev/null
}
