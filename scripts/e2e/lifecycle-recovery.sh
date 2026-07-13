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

ledger_incident_json() {
  local pr="$1" result
  result="$(sqlite3 -json "$work/runs/coordination.db" \
    "SELECT category,retry_count,observed_head,observed_target,log_location FROM incidents WHERE forge='local' AND owner='${owner}' AND repo='${repo}' AND pr='${pr}' AND category='lifecycle-hard-kill'")"
  printf '%s\n' "${result:-[]}"
}

operator_incident_json() {
  local pr="$1"
  find "$work/incidents" -maxdepth 1 -type f -name '*.json' -print0 |
    xargs -0 -r jq -s --arg pr "$pr" '[.[] | select(.key.pr == $pr and .key.category == "lifecycle-hard-kill")]'
}

hard_kill_incident_is() {
  local pr="$1" updates="$2"
  [[ "$(ledger_incident_json "$pr" | jq -r 'length')" -eq 1 ]] &&
    [[ "$(ledger_incident_json "$pr" | jq -r '.[0].retry_count')" -eq "$updates" ]] &&
    [[ "$(operator_incident_json "$pr" | jq -r 'length')" -eq 1 ]] &&
    [[ "$(operator_incident_json "$pr" | jq -r '.[0].updates')" -eq "$updates" ]]
}

retryable_backoff_json() {
  local pr="$1" result
  result="$(sqlite3 -json "$work/runs/coordination.db" \
    "SELECT attempt,last_failure_at,next_due_at FROM backoff WHERE forge='local' AND owner='${owner}' AND repo='${repo}' AND pr='${pr}'")"
  printf '%s\n' "${result:-[]}"
}

retryable_ledger_incident_json() {
  local pr="$1" result
  result="$(sqlite3 -json "$work/runs/coordination.db" \
    "SELECT category,retry_count,observed_head,observed_target,log_location FROM incidents WHERE forge='local' AND owner='${owner}' AND repo='${repo}' AND pr='${pr}' AND category='stale-oauth'")"
  printf '%s\n' "${result:-[]}"
}

retryable_operator_incident_json() {
  local pr="$1"
  find "$work/incidents" -maxdepth 1 -type f -name '*.json' -print0 |
    xargs -0 -r jq -s --arg pr "$pr" '[.[] | select(.key.pr == $pr and .key.category == "stale-oauth")]'
}

retryable_failure_is_recorded() {
  local pr="$1"
  [[ "$(retryable_backoff_json "$pr" | jq -r '.[0].attempt // 0')" -eq 1 ]] &&
    [[ "$(retryable_ledger_incident_json "$pr" | jq -r 'length')" -eq 1 ]] &&
    [[ "$(retryable_operator_incident_json "$pr" | jq -r 'length')" -eq 1 ]] &&
    [[ "$(retryable_operator_incident_json "$pr" | jq -r '.[0].updates')" -eq 1 ]] &&
    [[ "$(retryable_operator_incident_json "$pr" | jq -r '.[0].retry_disposition')" == retry\ after\ * ]]
}

retryable_backoff_absent() {
  [[ "$(retryable_backoff_json "$1")" == "[]" ]]
}

cleanup_obligation_json() {
  local pr="$1" result
  result="$(sqlite3 -json "$work/runs/coordination.db" \
    "SELECT merged_head,branch,attempts FROM cleanup_obligations WHERE forge='local' AND owner='${owner}' AND repo='${repo}' AND pr='${pr}'")"
  printf '%s\n' "${result:-[]}"
}

cleanup_obligation_is() {
  local pr="$1" attempts="$2"
  [[ "$(cleanup_obligation_json "$pr" | jq -r 'length')" -eq 1 ]] &&
    [[ "$(cleanup_obligation_json "$pr" | jq -r '.[0].attempts')" -eq "$attempts" ]]
}

cleanup_obligation_absent() {
  [[ "$(cleanup_obligation_json "$1")" == "[]" ]]
}

cleanup_ledger_incident_json() {
  local pr="$1" result
  result="$(sqlite3 -json "$work/runs/coordination.db" \
    "SELECT category,retry_count,observed_head,observed_target,log_location FROM incidents WHERE forge='local' AND owner='${owner}' AND repo='${repo}' AND pr='${pr}' AND category='cleanup-unsafe'")"
  printf '%s\n' "${result:-[]}"
}

cleanup_operator_incident_json() {
  local pr="$1"
  find "$work/incidents" -maxdepth 1 -type f -name '*.json' -print0 |
    xargs -0 -r jq -s --arg pr "$pr" '[.[] | select(.key.pr == $pr and .key.category == "cleanup-unsafe")]'
}

cleanup_incident_is() {
  local pr="$1" updates="$2"
  [[ "$(cleanup_ledger_incident_json "$pr" | jq -r 'length')" -eq 1 ]] &&
    [[ "$(cleanup_ledger_incident_json "$pr" | jq -r '.[0].retry_count')" -eq "$updates" ]] &&
    [[ "$(cleanup_operator_incident_json "$pr" | jq -r 'length')" -eq 1 ]] &&
    [[ "$(cleanup_operator_incident_json "$pr" | jq -r '.[0].updates')" -eq "$updates" ]]
}

cleanup_incomplete_incident_is() {
  local pr="$1"
  [[ "$(sqlite3 -json "$work/runs/coordination.db" \
    "SELECT category FROM incidents WHERE forge='local' AND owner='${owner}' AND repo='${repo}' AND pr='${pr}' AND category='cleanup-incomplete'" | jq -r 'length')" -eq 1 ]] &&
    [[ "$(find "$work/incidents" -maxdepth 1 -type f -name '*.json' -print0 |
      xargs -0 -r jq -s --arg pr "$pr" '[.[] | select(.key.pr == $pr and .key.category == "cleanup-incomplete" and .state == "open")] | length')" -eq 1 ]]
}

branch_sha() {
  api GET "/api/v1/repos/${owner}/${repo}/branches/$1" | jq -r '.commit.id // .commit.sha // ""'
}

pr_surface_digest() {
  local pr="$1" head="$2"
  jq -Scn \
    --argjson reviews "$(api GET "/api/v1/repos/${owner}/${repo}/pulls/${pr}/reviews")" \
    --argjson comments "$(api GET "/api/v1/repos/${owner}/${repo}/issues/${pr}/comments")" \
    --argjson statuses "$(api GET "/api/v1/repos/${owner}/${repo}/commits/${head}/statuses")" \
    --argjson reactions "$(api GET "/api/v1/repos/${owner}/${repo}/issues/${pr}/reactions")" '
      {
        reviews: [($reviews // [])[] | {id,state,commit_id,body}],
        comments: [($comments // [])[] | {id,body}],
        statuses: [($statuses // [])[] | {id,context,status:(.status // .state),description}],
        reactions: [($reactions // [])[] | {id,content,user:(.user.login // .user.username)}]
      }' | sha256sum | awk '{print $1}'
}

prepare_cleanup_fixture() {
  local branch="$1" pr_var="$2" sha_var="$3" pr sha
  pr="$(create_pr "$branch")"
  sha="$(api GET "/api/v1/repos/${owner}/${repo}/pulls/${pr}" | jq -r '.head.sha')"
  journey_units+=("minos-run-${owner}-${repo}-pr${pr}-${sha:0:12}.service")
  send_opened_hook "$pr"
  wait_until "${branch} merged" pr_merged "$pr"
  wait_until "${branch} status merged" status_is "$sha" Merged
  wait_until "${branch} cleanup obligation is durable" cleanup_obligation_is "$pr" 0
  wait_until "${branch} eyes removed" eyes_absent "$pr"
  wait_until "${branch} lease released" lease_absent "$pr"
  require "${branch} used one lifecycle" attempt_count_is "$pr" 1
  printf -v "$pr_var" '%s' "$pr"
  printf -v "$sha_var" '%s' "$sha"
}

run_post_merge_cleanup_journey() {
  local unchanged_pr unchanged_sha unchanged_surface
  local absent_pr absent_sha absent_surface
  local advanced_pr advanced_sha advanced_branch_sha advanced_pr_head advanced_pr_ref advanced_surface
  local stranded_pr stranded_sha stranded_unit stranded_lease stranded_workspace

  prepare_cleanup_fixture cleanup-unchanged unchanged_pr unchanged_sha
  unchanged_surface="$(pr_surface_digest "$unchanged_pr" "$unchanged_sha")"
  "$root/minos" sweep --config "$work/config"
  require 'unchanged cleanup deletes the guarded source branch' branch_absent cleanup-unchanged
  require 'unchanged cleanup settles its obligation' cleanup_obligation_absent "$unchanged_pr"
  require 'unchanged cleanup writes nothing to the PR' test \
    "$(pr_surface_digest "$unchanged_pr" "$unchanged_sha")" = "$unchanged_surface"
  require 'unchanged cleanup starts no successor lifecycle' attempt_count_is "$unchanged_pr" 1
  record_evidence sweep cleanup unchanged applied "pr=${unchanged_pr}; branch=absent; obligation=absent; pr-surface=unchanged"

  prepare_cleanup_fixture cleanup-already-absent absent_pr absent_sha
  absent_surface="$(pr_surface_digest "$absent_pr" "$absent_sha")"
  (
    cd "$work/subject"
    git push -q origin --delete cleanup-already-absent
  )
  require 'already-absent fixture removes the source branch before sweep' branch_absent cleanup-already-absent
  "$root/minos" sweep --config "$work/config"
  require 'already-absent cleanup settles as applied' cleanup_obligation_absent "$absent_pr"
  require 'already-absent cleanup writes nothing to the PR' test \
    "$(pr_surface_digest "$absent_pr" "$absent_sha")" = "$absent_surface"
  require 'already-absent cleanup starts no successor lifecycle' attempt_count_is "$absent_pr" 1
  record_evidence sweep cleanup absent applied "pr=${absent_pr}; branch=absent-before-sweep; obligation=absent; pr-surface=unchanged"

  prepare_cleanup_fixture cleanup-advanced advanced_pr advanced_sha
  advanced_surface="$(pr_surface_digest "$advanced_pr" "$advanced_sha")"
  (
    cd "$work/subject"
    git checkout -q cleanup-advanced
    printf '\n// Branch advanced after its pull request merged.\n' >>ready.go
    git add ready.go
    git commit -q -m 'test: advance merged source branch'
    git push -q origin cleanup-advanced
  )
  advanced_branch_sha="$(branch_sha cleanup-advanced)"
  require 'advanced fixture no longer points at the merged head' test "$advanced_branch_sha" != "$advanced_sha"
  advanced_pr_head="$(api GET "/api/v1/repos/${owner}/${repo}/pulls/${advanced_pr}" | jq -r '.head.sha')"
  advanced_pr_ref="$(api GET "/api/v1/repos/${owner}/${repo}/pulls/${advanced_pr}" | jq -r '.head.ref')"
  record_evidence forge observe advanced observed \
    "pr=${advanced_pr}; merged-head=${advanced_sha}; branch-ref=${advanced_pr_ref}; pr-head=${advanced_pr_head}; branch-head=${advanced_branch_sha}"

  send_opened_hook "$advanced_pr" "$work/logs/cleanup-advanced-receiver.out"
  require 'advanced webhook is reconciled as cleanup' grep -Fxq cleanup "$work/logs/cleanup-advanced-receiver.out"
  require 'receiver cleanup preserves the advanced branch' test \
    "$(branch_sha cleanup-advanced)" = "$advanced_branch_sha"
  require 'receiver cleanup settles its unsafe obligation' cleanup_obligation_absent "$advanced_pr"
  require 'receiver cleanup raises one ledger and operator incident' cleanup_incident_is "$advanced_pr" 1
  require 'receiver cleanup writes nothing to the PR' test \
    "$(pr_surface_digest "$advanced_pr" "$advanced_sha")" = "$advanced_surface"
  require 'receiver cleanup starts no successor lifecycle' attempt_count_is "$advanced_pr" 1
  require 'receiver cleanup preserves merged product truth' pr_merged "$advanced_pr"
  record_evidence receiver cleanup advanced rejected \
    "pr=${advanced_pr}; branch=${advanced_branch_sha}; obligation=absent; ledger-incidents=1; operator-incidents=1; updates=1; pr-surface=unchanged"

  "$root/minos" sweep --config "$work/config"
  require 'repeated sweep cleanup preserves the advanced branch' test \
    "$(branch_sha cleanup-advanced)" = "$advanced_branch_sha"
  require 'repeated sweep finds no unsafe cleanup obligation' cleanup_obligation_absent "$advanced_pr"
  require 'settled unsafe cleanup retains one operator incident' cleanup_incident_is "$advanced_pr" 1
  require 'repeated cleanup writes nothing to the PR' test \
    "$(pr_surface_digest "$advanced_pr" "$advanced_sha")" = "$advanced_surface"
  require 'repeated cleanup starts no successor lifecycle' attempt_count_is "$advanced_pr" 1
  require 'repeated cleanup preserves merged product truth' pr_merged "$advanced_pr"
  record_evidence sweep cleanup advanced rejected \
    "pr=${advanced_pr}; branch=${advanced_branch_sha}; obligation=absent; ledger-incidents=1; operator-incidents=1; updates=1; pr-surface=unchanged"

  stranded_pr="$(create_pr cleanup-stranded-state)"
  stranded_sha="$(api GET "/api/v1/repos/${owner}/${repo}/pulls/${stranded_pr}" | jq -r '.head.sha')"
  stranded_unit="minos-run-${owner}-${repo}-pr${stranded_pr}-${stranded_sha:0:12}.service"
  journey_units+=("$stranded_unit")
  send_opened_hook "$stranded_pr"
  wait_until 'stranded fixture reaches post-merge hold' test -e "$work/control/cleanup-stranded-ready"
  require 'stranded fixture is merged' pr_merged "$stranded_pr"
  require 'stranded fixture has merged product status' status_is "$stranded_sha" Merged
  require 'stranded fixture has durable cleanup' cleanup_obligation_is "$stranded_pr" 0
  require 'stranded fixture retains eyes before process death' eyes_present "$stranded_pr"
  stranded_lease="$(lease_json "$stranded_pr")"
  stranded_workspace="$(jq -r '.[0].workspace' <<<"$stranded_lease")"
  require 'stranded fixture retains its lease before process death' test "$stranded_lease" != '[]'

  systemctl --user kill --kill-whom=all --signal=SIGKILL "$stranded_unit"
  wait_until 'stranded fixture process is dead' unit_not_running "$stranded_unit"
  sleep 7
  mkdir -p "$work/faults"
  : >"$work/faults/retry-delete-branch"

  send_opened_hook "$stranded_pr" "$work/logs/cleanup-stranded-receiver.out"
  require 'stranded receiver keeps cleanup first' grep -Fxq cleanup "$work/logs/cleanup-stranded-receiver.out"
  require 'first retryable refusal retains cleanup' cleanup_obligation_is "$stranded_pr" 1
  require 'first retryable refusal retains stale lease' test "$(lease_json "$stranded_pr")" != '[]'
  require 'first retryable refusal retains eyes' eyes_present "$stranded_pr"

  "$root/minos" sweep --config "$work/config"
  require 'second retryable refusal retains cleanup' cleanup_obligation_is "$stranded_pr" 2
  require 'second retryable refusal retains stale lease' test "$(lease_json "$stranded_pr")" != '[]'

  "$root/minos" sweep --config "$work/config"
  require 'bounded cleanup preserves the source branch' test "$(branch_sha cleanup-stranded-state)" = "$stranded_sha"
  require 'bounded cleanup settles the obligation' cleanup_obligation_absent "$stranded_pr"
  require 'bounded cleanup raises an operator signal' cleanup_incomplete_incident_is "$stranded_pr"
  require 'same reconciliation reaps the stale lease' lease_absent "$stranded_pr"
  require 'same reconciliation removes eyes' eyes_absent "$stranded_pr"
  require 'same reconciliation removes the stale workspace' test ! -e "$stranded_workspace"
  require 'stranded convergence starts no successor lifecycle' attempt_count_is "$stranded_pr" 1
  require 'stranded convergence retains merged product truth' pr_merged "$stranded_pr"
  require 'stranded convergence writes no operational chatter' no_operational_chatter "$stranded_pr" "$stranded_sha"
  record_evidence sweep cleanup stranded preserved \
    "pr=${stranded_pr}; retries=3; branch=preserved; obligation=absent; eyes=absent; lease=absent; workspace=absent; operator-signal=open"
  rm -f "$work/faults/retry-delete-branch"
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

run_retryable_exit_journey() {
  local retry_pr retry_sha retry_unit receiver_result
  retry_pr="$(create_pr retryable-exit-lifecycle)"
  retry_sha="$(api GET "/api/v1/repos/${owner}/${repo}/pulls/${retry_pr}" | jq -r '.head.sha')"
  retry_unit="minos-run-${owner}-${repo}-pr${retry_pr}-${retry_sha:0:12}.service"
  journey_units+=("$retry_unit")

  send_opened_hook "$retry_pr"
  wait_until 'retryable exit is declared' test -e "$work/control/retryable-exit-attempted"
  wait_until 'retryable exit releases its lease' lease_absent "$retry_pr"
  wait_until 'retryable exit removes eyes' eyes_absent "$retry_pr"
  require 'retryable exit leaves the working presentation for reconciliation' status_is "$retry_sha" 'Reviewing changes'
  require 'retryable exit records backoff and one categorised incident' retryable_failure_is_recorded "$retry_pr"
  require 'retryable exit publishes no review' count_is "$(review_count "$retry_pr")" 0
  require 'retryable exit writes no operational chatter to the PR' no_operational_chatter "$retry_pr" "$retry_sha"
  require 'retryable exit completes exactly one attempt before reconciliation' attempt_count_is "$retry_pr" 1
  record_evidence wrapper retryable-exit predecessor applied \
    "pr=${retry_pr}; status=working; lease=absent; eyes=absent; backoff-attempt=1; incident=stale-oauth"

  receiver_result="$work/logs/retryable-exit-receiver.out"
  send_opened_hook "$retry_pr" "$receiver_result"
  require 'receiver defers admission during retry backoff' grep -Fxq nothing "$receiver_result"
  wait_until 'receiver returns ownerless working to queued' status_is "$retry_sha" 'Waiting for review'
  require 'receiver starts no attempt before retry is due' attempt_count_is "$retry_pr" 1
  record_evidence receiver reconcile predecessor applied \
    "pr=${retry_pr}; status=queued; lease=absent; retry=deferred"

  sqlite3 "$work/runs/coordination.db" \
    "UPDATE backoff SET next_due_at='1970-01-01T00:00:00Z' WHERE forge='local' AND owner='${owner}' AND repo='${repo}' AND pr='${retry_pr}'"
  "$root/minos" sweep --config "$work/config"
  wait_until 'due retry launches and reaches a settled product state' status_is "$retry_sha" 'Review stopped; findings remain'
  wait_until 'due retry publishes its current-head review' review_on_head "$retry_pr" "$retry_sha"
  wait_until 'due retry releases its lease' lease_absent "$retry_pr"
  wait_until 'successful successor clears operational backoff' retryable_backoff_absent "$retry_pr"
  require 'due retry uses exactly one fresh successor' attempt_count_is "$retry_pr" 2
  require 'retry incident remains deduplicated' test "$(retryable_operator_incident_json "$retry_pr" | jq -r 'length')" -eq 1
  require 'retry recovery writes no operational chatter to the PR' no_operational_chatter "$retry_pr" "$retry_sha"
  record_evidence sweep retry successor applied \
    "pr=${retry_pr}; attempts=2; status=stopped; backoff=absent; incident-records=1"
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
  mkdir -p "$work/faults"
  : >"$work/faults/fail-remove-reaction-once"
  "$root/minos" sweep --config "$work/config"
  require 'first hard-kill observation retains predecessor after eyes failure' lease_token_is "$hardkill_pr" "$predecessor_token"
  require 'first hard-kill observation retains predecessor eyes' eyes_present "$hardkill_pr"
  require 'first hard-kill observation raises one incident' hard_kill_incident_is "$hardkill_pr" 1
  record_evidence incident raise predecessor applied "category=lifecycle-hard-kill; ledger-retries=1; operator-updates=1; successor=absent"
  "$root/minos" sweep --config "$work/config"
  wait_until 'hard-kill successor is held before claiming eyes' test -e "$work/control/hard-kill-successor-before-begin"
  successor_token="$(<"$work/control/hard-kill-successor-before-begin")"
  require 'hard-kill successor receives a higher token' token_greater_than "$successor_token" "$predecessor_token"
  require 'hard-kill successor owns the lease' lease_token_is "$hardkill_pr" "$successor_token"
  require 'predecessor eyes are absent before successor begins' eyes_absent "$hardkill_pr"
  require 'repeated hard-kill observation updates one incident' hard_kill_incident_is "$hardkill_pr" 2
  require 'hard-kill incident retains predecessor pair' test \
    "$(ledger_incident_json "$hardkill_pr" | jq -r '.[0].observed_head + ":" + .[0].observed_target')" = \
    "${hardkill_sha}:$(jq -r '.[0].observed_target' <<<"$predecessor_lease")"
  require 'operator incident retains predecessor attempt' test \
    "$(operator_incident_json "$hardkill_pr" | jq -r '.[0].attempt')" = "$predecessor_token"
  record_evidence sweep replace successor applied "predecessor=${predecessor_token}; successor=${successor_token}; target=${hardkill_target}; eyes-before-successor=absent; incident-updates=2"
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
  require 'final hard-kill sweep does not duplicate the incident' hard_kill_incident_is "$hardkill_pr" 2
  record_evidence wrapper close successor applied "terminal=stopped; eyes=absent; lease=absent; attempts=2; pr-operational-chatter=absent; incident-updates=2"
  author_api PATCH "/api/v1/repos/${owner}/${repo}/pulls/${hardkill_pr}" '{"state":"closed"}' >/dev/null
}

run_durable_kill_journey() {
  local terminal="$1" terminal_description
  local durable_pr durable_sha durable_unit durable_token durable_lease heartbeat_before
  local statuses_before statuses_after workspace
  case "$terminal" in
    stopped) terminal_description='Review stopped; findings remain' ;;
    blocked) terminal_description='Changes need attention' ;;
    *) printf 'unknown durable-kill terminal: %s\n' "$terminal" >&2; return 1 ;;
  esac
  durable_pr="$(create_pr "durable-${terminal}-kill-lifecycle")"
  durable_sha="$(api GET "/api/v1/repos/${owner}/${repo}/pulls/${durable_pr}" | jq -r '.head.sha')"
  durable_unit="minos-run-${owner}-${repo}-pr${durable_pr}-${durable_sha:0:12}.service"
  journey_units+=("$durable_unit")
  send_opened_hook "$durable_pr"
  wait_until "durable-${terminal}-kill attempt publishes terminal product" test -e "$work/control/durable-${terminal}-kill-ready"
  durable_token="$(<"$work/control/durable-${terminal}-kill-ready")"
  durable_lease="$(lease_json "$durable_pr")"
  workspace="$(jq -r '.[0].workspace' <<<"$durable_lease")"
  require 'durable-kill attempt owns its lease' lease_token_is "$durable_pr" "$durable_token"
  require 'durable-kill attempt publishes eyes' eyes_present "$durable_pr"
  require 'durable review is visible exactly once' count_is "$(review_count "$durable_pr")" 1
  require "durable ${terminal} status is visible exactly once" count_is "$(status_description_count "$durable_sha" "$terminal_description")" 1
  heartbeat_before="$(lease_heartbeat "$durable_pr")"
  wait_until 'durable-kill attempt renews after product write' heartbeat_differs "$durable_pr" "$heartbeat_before"
  record_evidence forge durable-product predecessor applied "pr=${durable_pr}; token=${durable_token}; terminal=${terminal}; reviews=1; terminal-statuses=1"

  systemctl --user kill --kill-whom=all --signal=SIGKILL "$durable_unit"
  wait_until 'durable-kill predecessor unit is not running' unit_not_running "$durable_unit"
  require 'durable-kill lease survives process death' lease_token_is "$durable_pr" "$durable_token"
  sleep 7
  "$root/minos" sweep --config "$work/config"
  wait_until 'served terminal reap releases the dead lease' lease_absent "$durable_pr"
  require 'served terminal reap removes predecessor eyes' eyes_absent "$durable_pr"
  require 'served terminal reap launches no successor' attempt_count_is "$durable_pr" 1
  require 'served terminal reap removes the workspace' test ! -e "$workspace"
  require 'served terminal reap raises one incident' hard_kill_incident_is "$durable_pr" 1
  require 'served terminal incident retains observed pair' test \
    "$(ledger_incident_json "$durable_pr" | jq -r '.[0].observed_head + ":" + .[0].observed_target')" = \
    "${durable_sha}:$(jq -r '.[0].observed_target' <<<"$durable_lease")"
  require 'served terminal incident reports no-successor disposition' test \
    "$(operator_incident_json "$durable_pr" | jq -r '.[0].diagnostic')" = \
    'stale lifecycle reaped; current terminal product retained without a successor'
  require 'durable review remains exactly once after reap' count_is "$(review_count "$durable_pr")" 1
  require "durable ${terminal} status remains exactly once after reap" count_is "$(status_description_count "$durable_sha" "$terminal_description")" 1

  statuses_before="$(api GET "/api/v1/repos/${owner}/${repo}/commits/${durable_sha}/statuses" | jq 'length')"
  if env \
    MINOS_CONFIG="$work/config" MINOS_FORGE=local MINOS_OWNER="$owner" MINOS_REPO_NAME="$repo" \
    MINOS_PR="$durable_pr" MINOS_HEAD_SHA="$durable_sha" MINOS_TARGET_SHA="$(jq -r '.[0].observed_target' <<<"$durable_lease")" \
    MINOS_ATTEMPT_TOKEN="$durable_token" \
    "$root/minos" forge status partial >"$work/logs/durable-stale.out" 2>"$work/logs/durable-stale.err"; then
    printf 'failed: reaped terminal predecessor mutation unexpectedly succeeded\n' >&2
    exit 1
  fi
  statuses_after="$(api GET "/api/v1/repos/${owner}/${repo}/commits/${durable_sha}/statuses" | jq 'length')"
  [[ "$statuses_after" == "$statuses_before" ]]
  require 'served terminal reap leaves no operational PR chatter' no_operational_chatter "$durable_pr" "$durable_sha"
  "$root/minos" sweep --config "$work/config"
  require 'served terminal final sweep launches no successor' attempt_count_is "$durable_pr" 1
  require 'served terminal final sweep preserves one incident update' hard_kill_incident_is "$durable_pr" 1
  record_evidence sweep reap predecessor applied "terminal=${terminal}; attempts=1; successor=absent; eyes=absent; lease=absent; workspace=absent; incident-updates=1; stale-write=rejected"
  author_api PATCH "/api/v1/repos/${owner}/${repo}/pulls/${durable_pr}" '{"state":"closed"}' >/dev/null
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

post_raw_hook() {
  local event="$1" payload="$2" signature="$3" output="$4"
  curl -sS -o "$output" -w '%{http_code}' -X POST \
    -H 'Content-Type: application/json' \
    -H "X-Forgejo-Event: ${event}" \
    -H "X-Forgejo-Signature: ${signature}" \
    --data "$payload" "http://127.0.0.1:${hook_port}/hooks/local"
}

signed_payload() {
  printf '%s' "$1" | openssl dgst -sha256 -hmac "$secret" -hex | awk '{print $NF}'
}

all_attempts_absent() {
  [[ ! -d "$work/runs/attempts" ]] || [[ -z "$(find "$work/runs/attempts" -mindepth 2 -maxdepth 2 -type d -print -quit)" ]]
}

run_ingress_failure_journeys() {
  local payload signature status
  payload='{"action":"opened","repository":{"owner":{"login":"Minos"},"name":"subject"},"pull_request":{"number":99}}'
  status="$(post_raw_hook pull_request "$payload" deadbeef "$work/logs/invalid-signature.body")"
  require 'invalid signature returns unauthorised' count_is "$status" 401
  require 'invalid signature creates no attempt' all_attempts_absent
  require 'invalid signature creates no coordination database' test ! -e "$work/runs/coordination.db"
  record_evidence receiver authenticate none rejected "case=invalid-signature; http=401; attempts=0; ledger=absent"

  payload='{"action":"opened","repository":{"owner":{"login":"Other"},"name":"elsewhere"},"pull_request":{"number":1,"head":{"sha":"head"},"base":{"ref":"main"},"user":{"login":"author"}}}'
  signature="$(signed_payload "$payload")"
  status="$(post_raw_hook pull_request "$payload" "$signature" "$work/logs/unconfigured-repository.body")"
  require 'unconfigured repository is acknowledged without admission' count_is "$status" 202
  require 'unconfigured repository response is explicit' grep -Fxq 'not opted in' "$work/logs/unconfigured-repository.body"
  require 'unconfigured repository creates no attempt' all_attempts_absent
  require 'unconfigured repository creates no coordination database' test ! -e "$work/runs/coordination.db"
  record_evidence receiver route none skipped "case=unconfigured-repository; http=202; attempts=0; ledger=absent"
}

run_unmapped_event_journey() {
  local payload signature status
  payload='{"action":"pushed","repository":{"owner":{"login":"Minos"},"name":"subject"}}'
  signature="$(signed_payload "$payload")"
  status="$(post_raw_hook push "$payload" "$signature" "$work/logs/unmapped-event.body")"
  require 'authenticated unmapped event is acknowledged' count_is "$status" 202
  require 'unmapped event response is explicit' grep -Fxq 'unmapped event' "$work/logs/unmapped-event.body"
  require 'unmapped event creates no attempt' all_attempts_absent
  require 'unmapped event creates no coordination database' test ! -e "$work/runs/coordination.db"
  record_evidence receiver normalise none skipped "case=unmapped-event; http=202; attempts=0; ledger=absent"

  payload='{"action":"opened","repository":{"owner":{"login":"Minos"},"name":"subject"}}'
  signature="$(signed_payload "$payload")"
  status="$(post_raw_hook pull_request "$payload" "$signature" "$work/logs/mapped-missing-identity.body")"
  require 'mapped event with missing PR identity is rejected' count_is "$status" 400
  require 'mapped missing-identity response names normalisation failure' grep -Fxq 'normalise failed' "$work/logs/mapped-missing-identity.body"
  require 'mapped missing-identity event creates no attempt' all_attempts_absent
  require 'mapped missing-identity event creates no coordination database' test ! -e "$work/runs/coordination.db"
  record_evidence receiver normalise none rejected "case=mapped-missing-identity; http=400; attempts=0; ledger=absent"
}

run_capacity_journey() {
  local first_pr first_sha second_pr second_sha
  first_pr="$(create_pr capacity-holder-one)"
  first_sha="$(api GET "/api/v1/repos/${owner}/${repo}/pulls/${first_pr}" | jq -r '.head.sha')"
  journey_units+=("minos-run-${owner}-${repo}-pr${first_pr}-${first_sha:0:12}.service")
  send_opened_hook "$first_pr"
  wait_until 'capacity holder owns the only slot' test -e "$work/control/capacity-${first_pr}-ready"
  require 'capacity holder has one lease' lease_token_is "$first_pr" "$(lease_json "$first_pr" | jq -r '.[0].token')"
  require 'capacity holder publishes eyes' eyes_present "$first_pr"

  second_pr="$(create_pr capacity-holder-two)"
  second_sha="$(api GET "/api/v1/repos/${owner}/${repo}/pulls/${second_pr}" | jq -r '.head.sha')"
  journey_units+=("minos-run-${owner}-${repo}-pr${second_pr}-${second_sha:0:12}.service")
  send_opened_hook "$second_pr"
  require 'capacity-full PR has no lease' lease_absent "$second_pr"
  require 'capacity-full PR has no attempt' attempt_count_is "$second_pr" 0
  require 'capacity-full PR has no eyes' eyes_absent "$second_pr"
  require 'capacity-full PR has no product status' no_product_status "$second_sha"
  record_evidence ledger admit none skipped "case=capacity-full; held-pr=${first_pr}; deferred-pr=${second_pr}; deferred-attempts=0"

  : >"$work/control/capacity-${first_pr}-release"
  wait_until 'capacity holder releases its lease' lease_absent "$first_pr"
  author_api PATCH "/api/v1/repos/${owner}/${repo}/pulls/${first_pr}" '{"state":"closed"}' >/dev/null
  "$root/minos" sweep --config "$work/config"
  wait_until 'deferred PR is admitted by a later sweep' test -e "$work/control/capacity-${second_pr}-ready"
  require 'deferred PR owns the released slot' eyes_present "$second_pr"
  require 'deferred PR launches exactly once' attempt_count_is "$second_pr" 1
  record_evidence sweep admit successor applied "case=capacity-released; pr=${second_pr}; attempts=1"
  : >"$work/control/capacity-${second_pr}-release"
  wait_until 'deferred PR settles stopped' status_is "$second_sha" 'Review stopped; findings remain'
  wait_until 'deferred PR removes eyes' eyes_absent "$second_pr"
  wait_until 'deferred PR releases lease' lease_absent "$second_pr"
  require 'deferred PR remains exactly one attempt' attempt_count_is "$second_pr" 1
  record_evidence wrapper close successor applied "case=capacity-released; terminal=stopped; eyes=absent; lease=absent; attempts=1"
  author_api PATCH "/api/v1/repos/${owner}/${repo}/pulls/${second_pr}" '{"state":"closed"}' >/dev/null
}

run_closed_before_sweep_journey() {
  local closed_pr closed_sha
  closed_pr="$(create_pr closed-before-sweep-lifecycle)"
  closed_sha="$(api GET "/api/v1/repos/${owner}/${repo}/pulls/${closed_pr}" | jq -r '.head.sha')"
  author_api PATCH "/api/v1/repos/${owner}/${repo}/pulls/${closed_pr}" '{"state":"closed"}' >/dev/null
  record_evidence forge close none applied "case=closed-before-sweep; pr=${closed_pr}; webhook=suppressed"
  "$root/minos" sweep --config "$work/config"
  require 'closed-before-sweep PR has no lease' lease_absent "$closed_pr"
  require 'closed-before-sweep PR has no attempt' attempt_count_is "$closed_pr" 0
  require 'closed-before-sweep PR has no eyes' eyes_absent "$closed_pr"
  require 'closed-before-sweep PR has no product status' no_product_status "$closed_sha"
  record_evidence sweep reconcile none skipped "case=closed-before-sweep; attempts=0; lease=absent; eyes=absent; status=absent"
}

run_receiver_boundary_journeys() {
  local status
  status="$(curl -sS -o "$work/logs/method-not-allowed.body" -w '%{http_code}' \
    "http://127.0.0.1:${hook_port}/hooks/local")"
  require 'non-POST webhook request is rejected' count_is "$status" 405
  require 'method rejection creates no attempt' all_attempts_absent
  record_evidence receiver method none rejected "case=non-post; http=405; attempts=0"

  status="$(curl -sS -o "$work/logs/unknown-forge.body" -w '%{http_code}' -X POST \
    --data '{}' "http://127.0.0.1:${hook_port}/hooks/unknown")"
  require 'unknown forge route is rejected' count_is "$status" 404
  require 'unknown forge route creates no attempt' all_attempts_absent
  record_evidence receiver route none rejected "case=unknown-forge; http=404; attempts=0"
}
