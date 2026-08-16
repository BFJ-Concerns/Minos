# Prepare the reviewed repository

Work only in the supplied workspace. You have two possible responsibilities:
resolve a local target-reconciliation conflict, and ready the environment that
the configured build and test commands require.

## Reconciliation

Resolve only the listed conflicted files. Preserve both parents' intent; this is
reconciliation, not an opportunity to redesign or re-implement either side.
Do not introduce logic found in neither parent. Stage every accepted resolution
and leave every unconflicted repository file untouched.

Do not commit or push. Minos records the merge only after the lead has checked
your staged resolutions.

When lead objections are supplied, address only those objections in the named
paths. A reconciliation-only retry performs no environment work.

## Environment

Use the configured build and test commands, repository manifests, version
files, and lockfiles to determine the tools and dependencies that are actually
required. Empty configured commands require nothing. Prefer verification:
working tools that satisfy the repository must not be churned.

Respect explicit repository pins. Install per-checkout dependencies through
the repository's locked mechanism. When no repository pin exists and a global
toolchain must be installed, select its current stable release; do not upgrade
an already working toolchain merely to chase current. Install a missing global
toolchain only when the configured commands and repository evidence require
it. Do not make speculative or language-specific installations.

Every worker inherits persistent per-repository build-cache locations prepared
outside the workspace:

- `$MINOS_SHARED_CACHE_DIR` is the cache root.
- Rust compiler-cache state belongs in `$SCCACHE_DIR`. Keep each worktree's
  Cargo `target/` separate: one shared target directory serialises concurrent
  builds. For a Rust repository, provision `sccache` with `cargo install` or
  the system package manager when appropriate, then configure the run-local
  Cargo home to use it. If `sccache` provisioning fails, warm the workspace's
  own target, leave it intact for later worktrees to copy as untracked
  artefacts instead, and report the fallback and source path. After that warm
  build succeeds, write the target directory's absolute path and a newline to
  `$MINOS_RUN_DIR/rust-target-fallback`. Do not create that record when
  `sccache` is active or the fallback build did not succeed.
- Go build and module state belong in `$GOCACHE` and `$GOMODCACHE`.
- npm's content cache belongs in `$npm_config_cache`; do not share
  `node_modules` or repository build output between worktrees.

In full mode, use the repository's configured commands and manifests to choose
which caches to warm, then run each configured build and test command exactly
once to completion. The one sanctioned exception is an evidenced environment
repair: when a command's run fails on an environment fault you then prove and
repair — a missing tool, a broken cache, a bad toolchain install — re-run that
command after the repair, and report each repair and re-run in your evidence.
Free-form retries — re-running without a proven, repaired environment fault
between attempts — remain forbidden. Return the final exit statuses as
evidence so the lead can
avoid repeating successful verification. In reconciliation-only mode, resolve
the rejected conflicts without inspecting, provisioning, or changing the
environment and without running either configured command; return null exit
statuses for both commands.

The caches persist across Minos runs of this repository and are shared with
their fix worktrees. `$XDG_STATE_HOME` remains separate state, not a build
cache. Provisioned executables must land in an inherited PATH location:
`$HOME/.cargo/bin` or `$HOME/.local/bin`. Passwordless `sudo` is available when
an evidenced system package is the appropriate installation.

Never modify a repository file, regenerate a lockfile, invent a build or test
command, commit, or push. If the environment cannot be made ready, report the
exact requirement, attempted action, and failure.
