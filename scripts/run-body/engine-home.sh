# shellcheck shell=sh
# The engine environment a Minos process gives the agents it starts: the
# gateway credential, the list of provisioned engines and the private home
# each engine runs in. Sourced by run-body, which serves a pull request, and
# by describe-body, which serves a repository; both set failure_stage and
# failure_cause before a step can fail, and these functions keep them
# current, so a caller's exit handler words a failure the same way.

# The gateway settings a process may inherit from the service environment
# and must not pass on unless run-body.env asks for gateway mode.
unset_gateway_environment() {
  unset ANTHROPIC_AUTH_TOKEN ANTHROPIC_BASE_URL ANTHROPIC_DEFAULT_HAIKU_MODEL
  unset CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY CLAUDE_CODE_MAX_CONTEXT_TOKENS
  unset CLAUDE_CODE_AUTO_COMPACT_WINDOW MINOS_ANTHROPIC_CREDENTIAL_FILE
}

# In gateway mode (MINOS_ANTHROPIC_CREDENTIAL_FILE set by run-body.env),
# export the gateway token read from that file; otherwise clear every
# gateway setting. A gateway without its base URL or token exits 1.
load_gateway_credential() {
  if [ -z "${MINOS_ANTHROPIC_CREDENTIAL_FILE:-}" ]; then
    unset_gateway_environment
    return 0
  fi
  if [ -z "${ANTHROPIC_BASE_URL:-}" ]; then
    failure_cause="ANTHROPIC_BASE_URL is required in gateway mode"
    echo "${0##*/}: $failure_cause" >&2
    exit 1
  fi
  if [ ! -f "$MINOS_ANTHROPIC_CREDENTIAL_FILE" ] ||
    [ ! -r "$MINOS_ANTHROPIC_CREDENTIAL_FILE" ]; then
    failure_cause="gateway credential file ($MINOS_ANTHROPIC_CREDENTIAL_FILE) is missing or unreadable"
    echo "${0##*/}: $failure_cause" >&2
    exit 1
  fi
  ANTHROPIC_AUTH_TOKEN=""
  IFS= read -r ANTHROPIC_AUTH_TOKEN <"$MINOS_ANTHROPIC_CREDENTIAL_FILE" || true
  if [ -z "$ANTHROPIC_AUTH_TOKEN" ]; then
    failure_cause="gateway credential file ($MINOS_ANTHROPIC_CREDENTIAL_FILE) is empty"
    echo "${0##*/}: $failure_cause" >&2
    exit 1
  fi
  export ANTHROPIC_AUTH_TOKEN
}

# Sets provisioned_engines to the engines whose configuration seed
# run-body.env names, space-separated: an engine is provisioned exactly
# when its seed is.
resolve_provisioned_engines() {
  provisioned_engines=""
  [ -z "${MINOS_CLAUDE_CONFIG_SEED:-}" ] || provisioned_engines="claude"
  [ -z "${MINOS_CODEX_CONFIG_SEED:-}" ] || provisioned_engines="${provisioned_engines:+$provisioned_engines }codex"
}

# Gives the process a private home under $MINOS_RUN_DIR, seeded from each
# provisioned engine's configuration, and a private temp directory, whose
# path it leaves in run_tmpdir for the caller to remove. Run with errexit:
# a step that fails ends the caller with failure_stage and failure_cause
# naming it.
provision_engine_home() {
  HOME="${MINOS_RUN_DIR%/}/home"
  CLAUDE_CONFIG_DIR="$HOME/.claude"
  CODEX_HOME="$HOME/.codex"
  XDG_STATE_HOME="${MINOS_RUN_DIR%/}/state"
  # Pin the data home to the run-private HOME's default: an ambient
  # XDG_DATA_HOME in the service environment would silently relocate the
  # Ensemble launcher's direct-launch run records outside the run directory,
  # emptying the timing record's worker rows and dodging the archive.
  XDG_DATA_HOME="$HOME/.local/share"
  failure_stage="runtime-home"
  MINOS_RUNS_DIR="$(dirname "${MINOS_RUN_DIR%/}")"
  # Workers write large artefacts to temp; the box's shared tmpfs /tmp cannot
  # hold concurrent runs' output. Keep a short, private temp path on the same
  # disk-backed Minos storage and remove it with the run.
  MINOS_RUN_TMP_ROOT="$(dirname "$MINOS_RUNS_DIR")/tmp"
  failure_cause="could not create run temp directory root $MINOS_RUN_TMP_ROOT"
  mkdir -p "$MINOS_RUN_TMP_ROOT"
  failure_cause="could not secure run temp directory root $MINOS_RUN_TMP_ROOT"
  chmod 700 "$MINOS_RUN_TMP_ROOT"
  failure_cause="could not create isolated run temp directory in $MINOS_RUN_TMP_ROOT"
  run_tmpdir="$(mktemp -d "$MINOS_RUN_TMP_ROOT/t.XXXXXXXXXX")"
  TMPDIR="$run_tmpdir"
  export HOME CLAUDE_CONFIG_DIR CODEX_HOME XDG_STATE_HOME XDG_DATA_HOME \
    TMPDIR
  failure_stage="runtime-home"
  failure_cause="could not create isolated runtime home and state directories"
  mkdir -p "$CLAUDE_CONFIG_DIR" "$CLAUDE_CONFIG_DIR/projects" "$CODEX_HOME" "$XDG_STATE_HOME"
  failure_cause="could not secure isolated runtime home and state directories"
  chmod 700 "$HOME" "$CLAUDE_CONFIG_DIR" "$CLAUDE_CONFIG_DIR/projects" "$CODEX_HOME" "$XDG_STATE_HOME" "$TMPDIR"
  if [ -n "${MINOS_CLAUDE_CONFIG_SEED:-}" ]; then
    failure_stage="claude-seed"
    failure_cause="could not copy Claude configuration seed from $MINOS_CLAUDE_CONFIG_SEED"
    cp -R "${MINOS_CLAUDE_CONFIG_SEED%/}/." "$CLAUDE_CONFIG_DIR/"
  fi
  if [ -n "${MINOS_CODEX_CONFIG_SEED:-}" ]; then
    failure_stage="codex-seed"
    failure_cause="could not copy Codex configuration seed from $MINOS_CODEX_CONFIG_SEED"
    cp -R "${MINOS_CODEX_CONFIG_SEED%/}/." "$CODEX_HOME/"
  fi
  failure_stage="runtime-home"
  failure_cause="could not secure copied Claude and Codex configuration"
  chmod -R go-rwx "$CLAUDE_CONFIG_DIR" "$CODEX_HOME"
  # Claude refuses to start a background session in a workspace its state
  # file has not trusted, and the run-private state starts empty. The lead
  # runs from the run directory, so that directory, under the path Claude
  # resolves it to, is the one workspace the run trusts.
  failure_stage="claude-seed"
  failure_cause="could not resolve the run directory for Claude workspace trust"
  trusted_workspace="$(cd "$MINOS_RUN_DIR" && pwd -P)"
  failure_cause="could not write Claude onboarding and workspace acceptance"
  jq -cn --arg workspace "$trusted_workspace" '{
    hasCompletedOnboarding: true,
    bypassPermissionsModeAccepted: true,
    projects: {($workspace): {hasTrustDialogAccepted: true}}
  }' >"$CLAUDE_CONFIG_DIR/.claude.json"
  failure_cause="could not secure Claude onboarding and workspace acceptance"
  chmod 600 "$CLAUDE_CONFIG_DIR/.claude.json"
}
