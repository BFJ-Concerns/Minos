#!/usr/bin/env sh
set -eu

: "${PUMP19_API_BASE:?PUMP19_API_BASE is required}"
: "${PUMP19_FORGE_TOKEN:?PUMP19_FORGE_TOKEN is required}"

api() {
  method="$1"
  path="$2"
  shift 2
  curl -fsS \
    -X "$method" \
    -H "Authorization: token ${PUMP19_FORGE_TOKEN}" \
    -H "Accept: application/json" \
    "$@" \
    "${PUMP19_API_BASE%/}${path}"
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
