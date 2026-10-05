#!/usr/bin/env sh
set -eu

: "${MINOS_API_BASE:?MINOS_API_BASE is required}"
: "${MINOS_FORGE_CREDENTIAL:?MINOS_FORGE_CREDENTIAL is required}"

# Minos acts on GitHub as a GitHub App. The forge's credential file is the
# App's registration — a JSON object naming the App id, the installation
# id on the reviewed repositories and the path of the App's private key —
# and every request runs on a short-lived installation token this file
# mints from it. The service and the run body each hand that record over
# as MINOS_FORGE_CREDENTIAL; nothing outside this adaptation sees a token.
credential_field() {
  printf '%s' "$MINOS_FORGE_CREDENTIAL" | jq -er --arg key "$1" '
    if type != "object" then error("credential is not a JSON object") else
    (.[$key] // "" | tostring) end' 2>/dev/null
}
app_id="$(credential_field "app-id")" || app_id=""
installation_id="$(credential_field "installation-id")" || installation_id=""
private_key_file="$(credential_field "private-key-file")" || private_key_file=""
if [ -z "$app_id" ] || [ -z "$installation_id" ] || [ -z "$private_key_file" ]; then
  echo 'github: MINOS_FORGE_CREDENTIAL must be a JSON object carrying "app-id", "installation-id" and "private-key-file"' >&2
  exit 1
fi
if [ ! -r "$private_key_file" ]; then
  echo "github: the App private key $private_key_file is not readable" >&2
  exit 1
fi

github_request() {
  request_method="$1"
  request_path="$2"
  request_auth="$3"
  shift 3
  curl -fsS \
    -X "$request_method" \
    -H "Authorization: ${request_auth}" \
    -H "Accept: application/vnd.github+json" \
    -H "X-GitHub-Api-Version: 2022-11-28" \
    "$@" \
    "${MINOS_API_BASE%/}${request_path}"
}

base64url() {
  openssl base64 -A | tr '+/' '-_' | tr -d '='
}

# app_jwt signs a ten-minute App JWT (RS256) with the registration's key:
# the only credential that may call /app and mint installation tokens.
app_jwt() {
  jwt_now="$(date +%s)"
  jwt_header="$(printf '{"alg":"RS256","typ":"JWT"}' | base64url)"
  # -j: no trailing newline, so both segments are built the same way.
  jwt_claims="$(jq -ncj --argjson iat "$((jwt_now - 60))" --argjson exp "$((jwt_now + 540))" --arg iss "$app_id" \
    '{iat:$iat, exp:$exp, iss:$iss}' | base64url)"
  jwt_signature="$(printf '%s.%s' "$jwt_header" "$jwt_claims" | openssl dgst -sha256 -sign "$private_key_file" -binary | base64url)"
  printf '%s.%s.%s' "$jwt_header" "$jwt_claims" "$jwt_signature"
}

# mint_installation_token asks GitHub for the installation's token and the
# App's slug, and prints one record: {token, expires_at, login}. The login
# is what the forge shows on every write the token makes — the App's slug
# with GitHub's [bot] suffix — so the guards compare the configured
# bot-login against it.
mint_installation_token() {
  mint_jwt="$(app_jwt)" || return 1
  mint_app="$(github_request GET /app "Bearer ${mint_jwt}")" || return 1
  mint_slug="$(printf '%s' "$mint_app" | jq -er '.slug // empty')" || return 1
  mint_response="$(github_request POST "/app/installations/${installation_id}/access_tokens" "Bearer ${mint_jwt}")" || return 1
  printf '%s' "$mint_response" | jq -ec --arg slug "$mint_slug" '
    if (.token | type) != "string" or (.expires_at | type) != "string"
    then error("installation token response lacks token or expires_at")
    else {token, expires_at, login: ($slug + "[bot]")} end'
}

# Installation tokens live an hour; one is cached per API base, App and
# installation under the runtime directory (MINOS_GITHUB_TOKEN_CACHE names
# another root), written with owner-only permissions and replaced when
# fewer than two minutes remain. A token the forge has revoked early fails
# its request as any other forge error and is re-minted after expiry.
token_cache_root="${MINOS_GITHUB_TOKEN_CACHE:-${XDG_RUNTIME_DIR:-${TMPDIR:-/tmp}}}"
token_cache_key="$(printf '%s %s %s' "$MINOS_API_BASE" "$app_id" "$installation_id" | sha256sum | cut -c1-32)"
token_cache_file="${token_cache_root%/}/minos-github-${token_cache_key}.json"

load_installation_token() {
  load_now="$(date +%s)"
  if [ -r "$token_cache_file" ]; then
    load_cached="$(cat "$token_cache_file" 2>/dev/null)" || load_cached=""
    load_expires="$(printf '%s' "$load_cached" | jq -er '.expires_at // empty' 2>/dev/null)" || load_expires=""
    if [ -n "$load_expires" ] && load_expires_epoch="$(date -u -d "$load_expires" +%s 2>/dev/null)" \
      && [ "$load_expires_epoch" -gt "$((load_now + 120))" ]; then
      printf '%s' "$load_cached"
      return 0
    fi
  fi
  load_fresh="$(mint_installation_token)" || return 1
  mkdir -p "$token_cache_root" 2>/dev/null || true
  if (umask 077 && printf '%s\n' "$load_fresh" >"${token_cache_file}.$$"); then
    mv -f "${token_cache_file}.$$" "$token_cache_file" 2>/dev/null || rm -f "${token_cache_file}.$$"
  fi
  printf '%s' "$load_fresh"
}

token_record="$(load_installation_token)" || {
  echo "github: could not mint an installation token for App ${app_id}, installation ${installation_id}" >&2
  exit 1
}
MINOS_GITHUB_TOKEN="$(printf '%s' "$token_record" | jq -r '.token')"
MINOS_GITHUB_LOGIN="$(printf '%s' "$token_record" | jq -r '.login')"

api() {
  api_method="$1"
  api_path="$2"
  shift 2
  github_request "$api_method" "$api_path" "Bearer ${MINOS_GITHUB_TOKEN}" "$@"
}

# api_with_status is for decisions where HTTP absence or refusal is
# materially different from an unreadable forge. It returns non-zero only
# for transport failure and exposes the confirmed HTTP status and body
# through global variables.
api_with_status() {
  method="$1"
  path="$2"
  shift 2
  api_status_file="$(mktemp)"
  if api_status_code="$(curl -sS \
    -o "$api_status_file" \
    -w '%{http_code}' \
    -X "$method" \
    -H "Authorization: Bearer ${MINOS_GITHUB_TOKEN}" \
    -H "Accept: application/vnd.github+json" \
    -H "X-GitHub-Api-Version: 2022-11-28" \
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

# collect prints every page of a GitHub collection endpoint as one array.
# GitHub pages by per_page and page and answers [] past the last page; a
# page shorter than the limit is the last, so it is not fetched twice.
collect() {
  collect_path="$1"
  collect_query="${2:-}"
  collect_all='[]'
  collect_page=1
  while :; do
    collect_current="$(api GET "${collect_path}?per_page=100&page=${collect_page}${collect_query:+&$collect_query}")" || return 1
    collect_count="$(printf '%s' "$collect_current" | jq -e 'if type == "array" then length else error("expected a JSON array") end')" || return 1
    [ "$collect_count" -gt 0 ] || break
    collect_all="$(jq -cn --argjson all "$collect_all" --argjson page "$collect_current" '$all + $page')"
    [ "$collect_count" -ge 100 ] || break
    collect_page=$((collect_page + 1))
  done
  printf '%s' "$collect_all"
}

write_result() {
  outcome="$1"
  reason="${2:-}"
  jq -nc --arg outcome "$outcome" --arg reason "$reason" \
    '{outcome:$outcome} + (if $reason == "" then {} else {reason:$reason} end)'
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
# not invalidate. The identity check needs no request: the installation
# token's login is fixed by the App whose key minted it.
guard_open_pull_request() {
  guard_owner="$1"
  guard_repo="$2"
  guard_pr="$3"
  guard_expected_head="$4"
  # $5 is the caller's pinned target, accepted so every guarded script
  # passes the same coordinates; the guard does not compare it.
  guard_expected_login="$6"
  guard_reason=""

  if [ "$MINOS_GITHUB_LOGIN" != "$guard_expected_login" ]; then
    guard_reason="authenticated forge identity is ${MINOS_GITHUB_LOGIN:-missing}, expected ${guard_expected_login}"
    return 1
  fi

  guard_pr_json="$(api GET "/repos/${guard_owner}/${guard_repo}/pulls/${guard_pr}")" || return 2

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

# The same projection serves snapshots and commit-status readers.
commit_statuses() {
  status_records="$(collect "/repos/$1/$2/commits/$3/statuses")" || return 1
  printf '%s' "$status_records" | jq --arg provider github '[.[] |
    if ((.id | type) != "number" or .id <= 0) then error("GitHub status is missing a positive id") else
      {id, provider:$provider, context, state, creator:(.creator.login // ""), description:(.description // ""), target_url:(.target_url // "")}
    end] | sort_by(.id) | reverse'
}

web_base() {
  case "${MINOS_API_BASE%/}" in
    https://api.github.com) printf '%s' https://github.com ;;
    http://api.github.com) printf '%s' http://github.com ;;
    *) printf '%s' "${MINOS_API_BASE%/}" | sed 's|/api/v3$||' ;;
  esac
}

# Lookup helpers distinguish absence (1) from an unreadable forge (2).
reaction_id() {
  reaction_records="$(collect "/repos/$1/$2/issues/$3/reactions" 2>/dev/null)" || return 2
  reaction_found="$(printf '%s' "$reaction_records" | jq -er --arg content "$4" --arg login "$5" '
    if any(.[]; type != "object") then error("expected reaction objects") else
      [.[] | select(.content == $content and .user.login == $login)] |
      if length == 0 then empty else
        .[0].id | if type == "number" and . > 0 then . else error("reaction lacks positive id") end
      end
    end')" || { reaction_status=$?; [ "$reaction_status" -eq 4 ] && return 1; return 2; }
  printf '%s\n' "$reaction_found"
}

reaction_present() {
  reaction_id "$@" >/dev/null
}

# The page anchor of the newest review this login left on the given head,
# as GitHub itself reports it — the fragment of the review's `html_url`
# (`pullrequestreview-<id>`): returns 0 and prints the fragment without its
# `#`, 1 when the login has no anchored review on that head, 2 when the
# reviews could not be read.
latest_own_review_anchor() {
  own_reviews="$(collect "/repos/$1/$2/pulls/$3/reviews" 2>/dev/null)" || return 2
  own_review_anchor="$(printf '%s' "$own_reviews" | jq -er --arg head "$4" --arg login "$5" '
    if any(.[]; type != "object") then error("expected review objects") else
      [.[] | select(.commit_id == $head and .user.login == $login)] |
      if length == 0 then empty else
        max_by(.id) | (.html_url // "" | if type == "string" then . else error("review html_url is not a string") end) |
        split("#") | .[1:] | join("#") | if . == "" then empty else . end
      end
    end')" || { own_review_status=$?; [ "$own_review_status" -eq 4 ] && return 1; return 2; }
  printf '%s\n' "$own_review_anchor"
}

label_id() {
  label_records="$(collect "$1" 2>/dev/null)" || return 2
  label_found="$(printf '%s' "$label_records" | jq -er --arg name "$2" '
    if any(.[]; type != "object") then error("expected label objects") else
      [.[] | select(.name == $name)] |
      if length == 0 then empty else
        .[0].id | if type == "number" and . > 0 then . else error("label lacks positive id") end
      end
    end')" || { label_status=$?; [ "$label_status" -eq 4 ] && return 1; return 2; }
  printf '%s\n' "$label_found"
}

issue_label_id() {
  label_id "/repos/$1/$2/issues/$3/labels" "$4"
}

defined_label_id() {
  label_id "/repos/$1/$2/labels" "$3"
}

missing_label_reason() {
  printf 'label %s is not defined on %s/%s; create it there or configure this marker as a reaction' "$3" "$1" "$2"
}

# Only marker removal accepts a merged PR, with the same head and writer.
guard_merged_pull_request() {
  guard_reason=""
  if [ "$MINOS_GITHUB_LOGIN" != "$5" ]; then
    guard_reason="authenticated forge identity is ${MINOS_GITHUB_LOGIN:-missing}, expected $5"
    return 1
  fi
  guard_pr_json="$(api GET "/repos/$1/$2/pulls/$3")" || return 2
  if printf '%s' "$guard_pr_json" | json_match --arg repository "$1/$2" --argjson pr "$3" --arg head "$4" '
    if type != "object" then error("expected pull request object") else
      .number == $pr and .base.repo.full_name == $repository and .head.sha == $head and (.merged // false)
    end'; then
    return 0
  else
    guard_status=$?
    [ "$guard_status" -eq 1 ] || return 2
    # The calling marker script reports this sourced helper's reason.
    # shellcheck disable=SC2034
    guard_reason="merged pull request identity no longer matches"
    return 1
  fi
}
