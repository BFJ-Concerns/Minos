#!/usr/bin/env sh
set -eu

: "${MINOS_API_BASE:?MINOS_API_BASE is required}"
: "${MINOS_FORGE_CREDENTIAL:?MINOS_FORGE_CREDENTIAL is required}"

api() {
  method="$1"
  path="$2"
  shift 2
  curl -fsS \
    -X "$method" \
    -H "Authorization: token ${MINOS_FORGE_CREDENTIAL}" \
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
    -H "Authorization: token ${MINOS_FORGE_CREDENTIAL}" \
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
  jq -nc --arg outcome "$outcome" --arg reason "$reason" \
    '{outcome:$outcome} + (if $reason == "" then {} else {reason:$reason} end)'
}

# Whether the login's reaction with this content is on the pull request:
# returns 0 present, 1 absent, 2 when the reactions could not be read. Pages
# until an empty page (array or null), or until the forge repeats the previous page.
reaction_present() {
  reaction_path="/api/v1/repos/$1/$2/issues/$3/reactions"
  reaction_page=1
  reaction_previous=''
  while :; do
    reaction_current="$(api GET "${reaction_path}?limit=50&page=${reaction_page}" 2>/dev/null)" || return 2
    if printf '%s' "$reaction_current" | json_match --arg content "$4" --arg login "$5" '
      (if . == null then [] else . end) |
      if type != "array" or any(.[]; type != "object") then error("expected reactions array") else
        any(.[]; .content == $content and (.user.login // .user.username // "") == $login)
      end
    ' >/dev/null; then
      return 0
    else
      reaction_status=$?
      [ "$reaction_status" -eq 1 ] || return 2
    fi
    ! repeats_previous_page "$reaction_current" "$reaction_previous" || return 1
    [ "$(printf '%s' "$reaction_current" | jq 'length')" -gt 0 ] || return 1
    reaction_previous="$reaction_current"
    reaction_page=$((reaction_page + 1))
  done
}

# jq's false result is confirmed absence; any decoding, shape or evaluation
# failure leaves the forge state unknown. Callers preserve this distinction.
json_match() {
  if jq -e "$@" >/dev/null; then
    return 0
  else
    json_match_status=$?
    [ "$json_match_status" -ne 1 ] || return 1
    return 2
  fi
}

urlencode() {
  jq -rn --arg value "$1" '$value | @uri'
}

# guard_open_pull_request is deliberately repeated inside every consequential
# verb. A caller's earlier snapshot is evidence for reasoning, not permission
# for a later mutation.
#
# The guard binds the head, never the target: every guarded write is a
# statement about the change under review, which the target advancing does
# not invalidate.
guard_open_pull_request() {
  guard_owner="$1"
  guard_repo="$2"
  guard_pr="$3"
  guard_expected_head="$4"
  # $5 is the caller's pinned target, accepted so every guarded script
  # passes the same coordinates; the guard does not compare it.
  guard_expected_login="$6"
  guard_reason=""

  guard_user_json="$(api GET "/api/v1/user")" || return 2
  guard_actual_login="$(printf '%s' "$guard_user_json" | jq -er 'if type != "object" then error("expected user object") else .login // .username // "" end')" || return 2
  if [ "$guard_actual_login" != "$guard_expected_login" ]; then
    guard_reason="authenticated forge identity is ${guard_actual_login:-missing}, expected ${guard_expected_login}"
    return 1
  fi

  guard_pr_json="$(api GET "/api/v1/repos/${guard_owner}/${guard_repo}/pulls/${guard_pr}")" || return 2

  if printf '%s' "$guard_pr_json" | json_match \
    --arg repository "${guard_owner}/${guard_repo}" \
    --argjson pr "$guard_pr" \
    'if type != "object" then error("expected pull request object") else .number == $pr and .base.repo.full_name == $repository end' >/dev/null; then
    :
  else
    guard_status=$?
    [ "$guard_status" -eq 1 ] || return 2
    guard_reason="pull request identity no longer matches"
    return 1
  fi
  guard_state="$(printf '%s' "$guard_pr_json" | jq -r '.state')" || return 2
  guard_merged="$(printf '%s' "$guard_pr_json" | jq -r '.merged // false')" || return 2
  if [ "$guard_state" != "open" ] || [ "$guard_merged" = "true" ]; then
    guard_reason="pull request is no longer open"
    return 1
  fi
  guard_head="$(printf '%s' "$guard_pr_json" | jq -r '.head.sha // ""')" || return 2
  if [ "$guard_head" != "$guard_expected_head" ]; then
    # The guarded caller reports this shared rejection reason.
    # shellcheck disable=SC2034
    guard_reason="head moved"
    return 1
  fi
  return 0
}

# guard_merged_pull_request protects cleanup which necessarily happens after
# the target branch has advanced. The merged pull request still binds the
# repository, source head and authenticated writer exactly.
guard_merged_pull_request() {
  guard_owner="$1"
  guard_repo="$2"
  guard_pr="$3"
  guard_expected_head="$4"
  guard_expected_login="$5"
  guard_reason=""

  guard_user_json="$(api GET "/api/v1/user")" || return 2
  guard_actual_login="$(printf '%s' "$guard_user_json" | jq -er 'if type != "object" then error("expected user object") else .login // .username // "" end')" || return 2
  if [ "$guard_actual_login" != "$guard_expected_login" ]; then
    guard_reason="authenticated forge identity is ${guard_actual_login:-missing}, expected ${guard_expected_login}"
    return 1
  fi

  guard_pr_json="$(api GET "/api/v1/repos/${guard_owner}/${guard_repo}/pulls/${guard_pr}")" || return 2
  if printf '%s' "$guard_pr_json" | json_match \
    --arg repository "${guard_owner}/${guard_repo}" \
    --arg head "$guard_expected_head" \
    --argjson pr "$guard_pr" '
      if type != "object" then error("expected pull request object") else
      .number == $pr and
      .base.repo.full_name == $repository and
      .head.sha == $head and
      (.merged // false) end
    ' >/dev/null; then
    :
  else
    guard_status=$?
    [ "$guard_status" -eq 1 ] || return 2
    # Callers report this shared rejection reason after the sourced helper returns.
    # shellcheck disable=SC2034
    guard_reason="merged pull request identity no longer matches"
    return 1
  fi
  return 0
}

# issue_label_id prints the id of the named label the pull request carries.
# It returns 1 when the pull request does not carry it and 2 when the
# forge could not be read. A label has no author, so presence is the pull
# request's, not the writer's.
issue_label_id() {
  label_issue_json="$(api GET "/api/v1/repos/${1}/${2}/issues/${3}/labels" 2>/dev/null)" || return 2
  label_found="$(printf '%s' "$label_issue_json" | jq -r --arg name "$4" 'first(.[]? | select(.name == $name) | .id) // ""')" || return 2
  [ -n "$label_found" ] || return 1
  printf '%s\n' "$label_found"
}

# defined_label_id prints the id of the named label as the repository
# defines it, or else as its owning organisation does. It returns 1 when
# neither defines it — Minos never creates labels on someone's forge — and
# 2 when the forge could not be read.
defined_label_id() {
  label_page=1
  label_previous=''
  while :; do
    label_current="$(api GET "/api/v1/repos/${1}/${2}/labels?limit=50&page=${label_page}" 2>/dev/null)" || return 2
    if repeats_previous_page "$label_current" "$label_previous"; then
      break
    fi
    label_found="$(printf '%s' "$label_current" | jq -r --arg name "$3" 'first(.[]? | select(.name == $name) | .id) // ""')" || return 2
    if [ -n "$label_found" ]; then
      printf '%s\n' "$label_found"
      return 0
    fi
    [ "$(printf '%s' "$label_current" | jq 'length')" -gt 0 ] || break
    label_previous="$label_current"
    label_page=$((label_page + 1))
  done
  label_page=1
  label_previous=''
  while :; do
    api_with_status GET "/api/v1/orgs/${1}/labels?limit=50&page=${label_page}" || return 2
    case "$api_status_code" in
      404) return 1 ;;
      2??) ;;
      *) return 2 ;;
    esac
    if repeats_previous_page "$api_status_body" "$label_previous"; then
      return 1
    fi
    label_found="$(printf '%s' "$api_status_body" | jq -r --arg name "$3" 'first(.[]? | select(.name == $name) | .id) // ""')" || return 2
    if [ -n "$label_found" ]; then
      printf '%s\n' "$label_found"
      return 0
    fi
    [ "$(printf '%s' "$api_status_body" | jq 'length')" -gt 0 ] || return 1
    label_previous="$api_status_body"
    label_page=$((label_page + 1))
  done
}

# missing_label_reason is the refusal an operator acts on: create the label,
# or configure the marker as a reaction.
missing_label_reason() {
  printf 'label %s is not defined on %s/%s or its organisation; create it there or configure this marker as a reaction' "$3" "$1" "$2"
}
