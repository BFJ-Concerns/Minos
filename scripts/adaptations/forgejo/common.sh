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
