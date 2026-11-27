# shellcheck shell=sh
# Git against the forge, for every Minos script that clones: the forge's
# adaptation answers each forge question through `$MINOS_BIN forge`, so
# nothing here names a forge's routes or reads its credential file.
# Sourced by setup-workspace and describe-body; messages carry the calling
# script's name.

# clone_url prints the clone URL the forge reports for owner/name.
clone_url() {
  "$MINOS_BIN" forge repository-metadata "${1%%/*}" "${1#*/}" | jq -er '.clone_url'
}

# authenticated_git OWNER/NAME GIT-ARGUMENTS... runs git with the
# credential for that repository. Each network operation uses the credential
# for its own repository. An owner-only temporary config under
# $MINOS_RUN_DIR gives Git the header without putting it in argv.
authenticated_git() (
  git_repository="$1"
  shift
  if ! clone_credential="$("$MINOS_BIN" forge clone-credential "${git_repository%%/*}" "${git_repository#*/}")"; then
    echo "${0##*/}: the forge supplied no clone credential for $git_repository" >&2
    exit 1
  fi
  if ! git_authorisation="$(printf '%s' "$clone_credential" | jq -er '
    if .anonymous == true then ""
    elif (.username | type) == "string" and .username != "" and (.password | type) == "string" and .password != ""
    then "Authorization: Basic " + ((.username + ":" + .password) | @base64)
    else error("malformed") end' 2>/dev/null)"; then
    echo "${0##*/}: the forge's clone credential for $git_repository is malformed" >&2
    exit 1
  fi
  export GIT_TERMINAL_PROMPT=0
  if [ -z "$git_authorisation" ]; then
    git "$@"
  else
    git_config="$(mktemp "$MINOS_RUN_DIR/.git-auth.XXXXXX")" || exit 1
    trap 'rm -f "$git_config"' 0
    printf '[http]\n\textraHeader = %s\n' "$git_authorisation" >"$git_config" || exit 1
    git -c "include.path=$git_config" "$@"
  fi
)
