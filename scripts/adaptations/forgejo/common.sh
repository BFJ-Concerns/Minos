#!/usr/bin/env sh
set -eu

: "${MINOS_API_BASE:?MINOS_API_BASE is required}"
: "${MINOS_FORGE_TOKEN:?MINOS_FORGE_TOKEN is required}"

api() {
  method="$1"
  path="$2"
  shift 2
  curl -fsS \
    -X "$method" \
    -H "Authorization: token ${MINOS_FORGE_TOKEN}" \
    -H "Accept: application/json" \
    "$@" \
    "${MINOS_API_BASE%/}${path}"
}

# api_with_status is for decisions where HTTP absence is materially different
# from an unreadable forge. It returns non-zero only for transport failure and
# exposes the confirmed HTTP status and body through global variables.
api_with_status() {
  method="$1"
  path="$2"
  shift 2
  api_status_file="$(mktemp)"
  if api_status_code="$(curl -sS \
    -o "$api_status_file" \
    -w '%{http_code}' \
    -X "$method" \
    -H "Authorization: token ${MINOS_FORGE_TOKEN}" \
    -H "Accept: application/json" \
    "$@" \
    "${MINOS_API_BASE%/}${path}")"; then
    api_status_body="$(cat "$api_status_file")"
    rm -f "$api_status_file"
    return 0
  fi
  rm -f "$api_status_file"
  # Callers consume these shared status fields after this sourced function
  # returns; ShellCheck cannot see those reads while analysing the library.
  # shellcheck disable=SC2034
  api_status_code=""
  # shellcheck disable=SC2034
  api_status_body=""
  return 1
}

json_string_array() {
  jq -r '[.[]] | @json'
}

# Some Forgejo collection endpoints accept a page parameter but return the
# complete collection for every value. Stop when the forge repeats the prior
# non-empty response so those endpoints terminate without duplicating records,
# while genuinely paginated endpoints continue until their empty page.
repeats_previous_page() {
  [ "$1" = "$2" ]
}

write_result() {
  outcome="$1"
  reason="${2:-}"
  sha="${3:-}"
  jq -nc --arg outcome "$outcome" --arg reason "$reason" --arg sha "$sha" \
    '{outcome:$outcome} + (if $reason == "" then {} else {reason:$reason} end) + (if $sha == "" then {} else {sha:$sha} end)'
}

urlencode() {
  jq -rn --arg value "$1" '$value | @uri'
}

# guard_open_pull_request is deliberately repeated inside every consequential
# verb. A caller's earlier snapshot is evidence for reasoning, not permission
# for a later mutation.
guard_open_pull_request() {
  guard_owner="$1"
  guard_repo="$2"
  guard_pr="$3"
  guard_expected_head="$4"
  guard_expected_target="$5"
  guard_expected_login="$6"
  guard_reason=""

  guard_user_json="$(api GET "/api/v1/user")" || return 2
  guard_actual_login="$(printf '%s' "$guard_user_json" | jq -r '.login // .username // ""')"
  if [ "$guard_actual_login" != "$guard_expected_login" ]; then
    guard_reason="authenticated forge identity is ${guard_actual_login:-missing}, expected ${guard_expected_login}"
    return 1
  fi

  guard_pr_json="$(api GET "/api/v1/repos/${guard_owner}/${guard_repo}/pulls/${guard_pr}")" || return 2
  guard_base_ref="$(printf '%s' "$guard_pr_json" | jq -r '.base.ref // ""')"
  guard_base_json="$(api GET "/api/v1/repos/${guard_owner}/${guard_repo}/branches/$(urlencode "$guard_base_ref")")" || return 2
  guard_actual_target="$(printf '%s' "$guard_base_json" | jq -r '.commit.id // .commit.sha // ""')"

  if ! printf '%s' "$guard_pr_json" | jq -e \
    --arg repository "${guard_owner}/${guard_repo}" \
    --argjson pr "$guard_pr" \
    '.number == $pr and .base.repo.full_name == $repository' >/dev/null; then
    guard_reason="pull request identity no longer matches"
    return 1
  fi
  if [ "$(printf '%s' "$guard_pr_json" | jq -r '.state')" != "open" ] || [ "$(printf '%s' "$guard_pr_json" | jq -r '.merged // false')" = "true" ]; then
    guard_reason="pull request is no longer open"
    return 1
  fi
  if [ "$(printf '%s' "$guard_pr_json" | jq -r '.head.sha // ""')" != "$guard_expected_head" ]; then
    guard_reason="head moved"
    return 1
  fi
  if [ "$guard_actual_target" != "$guard_expected_target" ]; then
    # The guarded caller reports this shared rejection reason.
    # shellcheck disable=SC2034
    guard_reason="target moved"
    return 1
  fi
  return 0
}
