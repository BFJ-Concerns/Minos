# Shared SSH transport to the archive host. Sourced, never executed: both the
# archive delivery and the timing listing reach the same account with the same
# batch-mode, pinned-identity, pinned-known-hosts settings, and one definition
# keeps them from drifting apart.
#
# Each call would otherwise pay a full connect, authenticate and disconnect
# cycle, which the archive host records as a login. Multiplexing collapses the
# calls in one window onto a single authenticated session, so a caller that
# reaches the host repeatedly costs one login rather than one per call.
#
# Sessions sharing a control path share a connection; sessions under different
# paths do not. That separation is deliberate — a long archive upload must not
# ride the connection a short listing may tear down, and vice versa — so a
# caller opens the transport under its own tag.

# archive_transport_open TAG PERSIST_SECONDS prepares multiplexing for the
# calls that follow. The control socket needs a private directory: without one
# it would have to live somewhere another local account could reach, so an
# unavailable directory silently leaves each call on its own connection rather
# than trading the transport's integrity for the saving.
#
# Keep TAG short. A Unix socket path is length-limited and the connection hash
# %C already spends 32 characters of it, so a long tag would push the socket
# past the limit and break the calls it was meant to make cheaper.
archive_transport_open() {
  archive_control_path=""
  archive_control_persist="$2"
  archive_control_dir="${MINOS_ARCHIVE_CONTROL_DIR:-${XDG_RUNTIME_DIR:-}}"
  [ -n "$archive_control_dir" ] || return 0
  archive_control_dir="$archive_control_dir/minos-archive-control"
  mkdir -p "$archive_control_dir" 2>/dev/null || return 0
  chmod 0700 "$archive_control_dir" 2>/dev/null || return 0
  archive_control_path="$archive_control_dir/$1-%C"
}

# archive_ssh REMOTE_COMMAND runs one command on the archive host, passing this
# process's stdin and stdout through to it.
archive_ssh() {
  archive_remote_command="$1"
  set -- -T -o BatchMode=yes -o ConnectTimeout=10 -o ConnectionAttempts=1 \
    -o ServerAliveInterval=5 -o ServerAliveCountMax=2 \
    -o IdentitiesOnly=yes -o "IdentityFile=$MINOS_ARCHIVE_IDENTITY_FILE" \
    -o "UserKnownHostsFile=$MINOS_ARCHIVE_KNOWN_HOSTS"
  if [ -n "${archive_control_path:-}" ]; then
    set -- "$@" -o ControlMaster=auto \
      -o "ControlPath=$archive_control_path" \
      -o "ControlPersist=${archive_control_persist:-30}"
  fi
  ssh "$@" "$MINOS_ARCHIVE_HOST" "$archive_remote_command"
}
