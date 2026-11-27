#!/usr/bin/env sh
set -eu

: "${MINOS_API_BASE:?MINOS_API_BASE is required}"
: "${MINOS_FORGE_CREDENTIAL:?MINOS_FORGE_CREDENTIAL is required}"

# The registration supplies the App identity and signing key. The requested
# repository determines which installation can act on it.
credential_field() {
  printf '%s' "$MINOS_FORGE_CREDENTIAL" | jq -er --arg key "$1" '
    if type != "object" then error("credential is not a JSON object") else
    (.[$key] // "" | tostring) end' 2>/dev/null
}
app_id="$(credential_field "app-id")" || app_id=""
private_key_file="$(credential_field "private-key-file")" || private_key_file=""
if [ -z "$app_id" ] || [ -z "$private_key_file" ]; then
  echo 'github: MINOS_FORGE_CREDENTIAL must be a JSON object carrying "app-id" and "private-key-file"' >&2
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
  if [ -n "$request_auth" ]; then
    github_curl -H @/dev/fd/3 "$@" -X "$request_method" "${MINOS_API_BASE%/}${request_path}" 3<<EOF
Authorization: $request_auth
EOF
  else
    github_curl "$@" -X "$request_method" "${MINOS_API_BASE%/}${request_path}"
  fi
}

github_curl() {
  curl -sS \
    -H "Accept: application/vnd.github+json" \
    -H "X-GitHub-Api-Version: 2022-11-28" \
    "$@"
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
  [ -n "$jwt_signature" ] || return 1
  printf '%s.%s.%s' "$jwt_header" "$jwt_claims" "$jwt_signature"
}

# mint_installation_token asks GitHub for the installation's token and the
# App's slug, and prints one record: {token, expires_at, login}. The login
# is what the forge shows on every write the token makes — the App's slug
# with GitHub's [bot] suffix — so the guards compare the configured
# bot-login against it.
mint_installation_token() {
  mint_app="$(github_request GET /app "Bearer ${installation_jwt}" -f)" || return 1
  mint_slug="$(printf '%s' "$mint_app" | jq -er '.slug // empty')" || return 1
  mint_response="$(github_request POST "/app/installations/${installation_id}/access_tokens" "Bearer ${installation_jwt}" -f)" || return 1
  printf '%s' "$mint_response" | jq -ec --arg slug "$mint_slug" '
    if (.token | type) != "string" or (.token | length == 0 or test("[\r\n]"))
      or (.expires_at | type) != "string"
    then error("installation token response lacks token or expires_at")
    else {token, expires_at, login: ($slug + "[bot]")} end'
}

# Installation tokens live an hour; one is cached per API base, App and
# installation under the runtime directory (MINOS_GITHUB_TOKEN_CACHE names
# another root), written with owner-only permissions and replaced when
# fewer than two minutes remain. A token the forge has revoked early fails
# its request as any other forge error and is re-minted after expiry.
token_cache_root="${MINOS_GITHUB_TOKEN_CACHE:-${XDG_RUNTIME_DIR:-${TMPDIR:-/tmp}}}"
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
  if load_tmp="$(mktemp "${token_cache_file}.XXXXXX" 2>/dev/null)"; then
    if ! printf '%s\n' "$load_fresh" >"$load_tmp" || ! mv -f "$load_tmp" "$token_cache_file"; then
      rm -f "$load_tmp"
    fi
  fi
  printf '%s' "$load_fresh"
}

# The optional public mode is for clone-related reads. A missing installation
# may mean a public fork, so the caller can try an unauthenticated lookup.
github_authenticate() {
  installation_owner="${1:?repository owner is required}"
  installation_repo="${2:?repository name is required}"
  installation_jwt="$(app_jwt)" || return 1
  request_with_status GET "/repos/${installation_owner}/${installation_repo}/installation" "Bearer ${installation_jwt}" || return 1
  if [ "$api_status_code" = 404 ] && [ "${3:-}" = public ]; then
    github_authorisation=""
    return 0
  fi
  if [ "$api_status_code" != 200 ]; then
    echo "github: installation lookup for ${installation_owner}/${installation_repo} returned HTTP ${api_status_code}" >&2
    return 1
  fi
  installation_id="$(printf '%s' "$api_status_body" | jq -er '.id | select(type == "number" and . > 0 and . == floor)')" || {
    echo "github: repository installation lookup did not return an installation ID" >&2
    return 1
  }
  token_cache_key="$(printf '%s\n' "${MINOS_API_BASE%/}" "$app_id" "$installation_id" | sha256sum | cut -c1-32)"
  token_cache_file="${token_cache_root%/}/minos-github-${token_cache_key}.json"
  token_record="$(load_installation_token)" || {
    echo "github: could not mint an installation token for App ${app_id}, installation ${installation_id}" >&2
    return 1
  }
  MINOS_GITHUB_TOKEN="$(printf '%s' "$token_record" | jq -er '.token | select(type == "string" and length > 0 and (test("[\r\n]") | not))')" || return 1
  MINOS_GITHUB_LOGIN="$(printf '%s' "$token_record" | jq -er '.login | select(type == "string" and length > 0)')" || return 1
  github_authorisation="Bearer ${MINOS_GITHUB_TOKEN}"
}

api() {
  api_method="$1"
  api_path="$2"
  shift 2
  github_request "$api_method" "$api_path" "$github_authorisation" -f "$@"
}

# Expose confirmed HTTP status and body. Return non-zero on transport failure.
request_with_status() {
  status_method="$1"
  status_path="$2"
  status_auth="$3"
  shift 3
  api_status_file="$(mktemp)" || return 1
  if api_status_code="$(github_request "$status_method" "$status_path" "$status_auth" \
    -o "$api_status_file" -w '%{http_code}' "$@")"; then
    api_status_body="$(cat "$api_status_file")"
    rm -f "$api_status_file"
    return 0
  fi
  rm -f "$api_status_file"
  api_status_code=""
  api_status_body=""
  return 1
}

api_with_status() {
  api_status_method="$1"
  api_status_path="$2"
  shift 2
  request_with_status "$api_status_method" "$api_status_path" "$github_authorisation" "$@"
}

# A payload whose size the forge or the lead decides reaches jq and curl on
# standard input, never as an argument: Linux caps one exec argument at
# 128 KiB (MAX_ARG_STRLEN), and a single page of pull requests with long
# bodies passes that. Shell functions and the printf builtin exec nothing,
# so handing a payload to them as an argument is safe.

# api_json sends $3 as the request's JSON body; api_json_with_status is the
# same over api_with_status. The heredoc ends the body with a newline, which
# JSON ignores.
api_json() {
  api_json_method="$1"
  api_json_path="$2"
  api_json_body="$3"
  api "$api_json_method" "$api_json_path" -H "Content-Type: application/json" --data-binary @- <<EOF
$api_json_body
EOF
}

api_json_with_status() {
  api_json_method="$1"
  api_json_path="$2"
  api_json_body="$3"
  api_with_status "$api_json_method" "$api_json_path" -H "Content-Type: application/json" --data-binary @- <<EOF
$api_json_body
EOF
}

# json_concat prints the JSON arrays $1 and $2 as one array.
json_concat() {
  printf '%s\n%s\n' "$1" "$2" | jq -cs 'if length == 2 then .[0] + .[1] else error("expected two JSON arrays") end'
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
    collect_all="$(json_concat "$collect_all" "$collect_current")" || return 1
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

# How the summary comment opens: the summary record's opening, which
# internal/product names SummaryOpening.
summary_opening='<!-- Minos summary:'

# conversation_comment_records projects a raw listing of the pull
# request's conversation comments ($1) to the protocol's records, oldest
# first: each comment's id, its author's login, its body and the time it was
# written. pull-request-comments prints these, and the snapshot carries them.
conversation_comment_records() {
  printf '%s' "$1" | jq -c 'sort_by((.created_at // ""), (.id // 0)) | map({
    id: (.id // 0),
    user: (.user.login // ""),
    body: (.body // ""),
    created_at: (.created_at // "")
  })'
}

# summary_comment_of prints, from a raw listing of the pull request's
# conversation comments ($1), the summary comment the login ($2) keeps as
# {id, body, updated_at}, or null when it keeps none: the oldest of its
# comments whose body opens with the summary record. updated_at is the
# forge's time of the comment's last edit, which tells one write of the same
# record from another.
summary_comment_of() {
  printf '%s' "$1" | jq -c --arg login "$2" --arg opening "$summary_opening" '
    [.[] | select((.user.login // "") == $login and ((.body // "") | startswith($opening)))] |
    if length == 0 then null else min_by(.id) | {id, body, updated_at: (.updated_at // "")} end'
}

# summary_comment lists the pull request's conversation comments and prints
# the login's summary comment from them (summary_comment_of). Returns 1 when
# the comments could not be read. This is the one place the comment is
# found, for the read that needs it alone and the guarded write that edits
# it; the snapshot finds it in the listing it also carries.
summary_comment() {
  summary_listing="$(collect "/repos/$1/$2/issues/$3/comments")" || return 1
  summary_comment_of "$summary_listing" "$4"
}

# The same projection serves snapshots and commit-status readers.
commit_statuses() {
  status_records="$(collect "/repos/$1/$2/commits/$3/statuses")" || return 1
  printf '%s' "$status_records" | jq --arg provider github '[.[] |
    if ((.id | type) != "number" or .id <= 0) then error("GitHub status is missing a positive id") else
      {id, provider:$provider, context, state, creator:(.creator.login // ""), description:(.description // ""), target_url:(.target_url // ""), created_at:(.created_at // "")}
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
  reaction_id_at "/repos/$1/$2/issues/$3/reactions" "$4" "$5"
}

# reaction_id_at is reaction_id over any reactions listing: the pull
# request's own ($1 its path) or one of its comments'.
reaction_id_at() {
  reaction_records="$(collect "$1" 2>/dev/null)" || return 2
  reaction_found="$(printf '%s' "$reaction_records" | jq -er --arg content "$2" --arg login "$3" '
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

reaction_present_at() {
  reaction_id_at "$@" >/dev/null
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
