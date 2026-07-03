# Deploy `pump19-daemon`

This guide starts from a fresh clone and gets the daemon to the point where it
can watch a Forgejo repository through the command-backed Forgejo seam. Live
Forgejo and live engine runs are deployment-time checks; the repository smoke
checks prove config loading and loud startup failures.

## Prerequisites

- Rust toolchain compatible with the workspace `rust-version` in `Cargo.toml`.
- `/usr/bin/podman`, because workspaces are created through the command runtime
  in `pump19-workspace`.
- Node.js and the ensemble launcher path you set in
  `ensemble.launcher_path`.
- Authenticated engine CLIs available to ensemble:
  `codex` for the Codex/GPT-class reviewer and fixer, `claude` for the
  Claude-class reviewer, and `opencode` configured for the GLM judge model
  `openrouter/z-ai/glm-4.6`.
- `curl`, `jq` and `git` for the generated baseline Forgejo command scripts.
  The example TOML points at the checked-in scripts under
  `examples/deployment/commands/`. Custom commands are an override path, not a
  prerequisite.
- A workspace container image matching `workspace.image`. The image must contain
  the toolchains needed by the repositories being reviewed, but it must not need
  forge credentials.

## Build The Daemon

```sh
cargo build --release -p pump19-daemon
```

The binary is:

```sh
target/release/pump19-daemon
```

## Configuration Set

Use [`examples/deployment/pump19-daemon.toml`](../examples/deployment/pump19-daemon.toml)
as the starting point. Paths in the TOML are resolved relative to the config file
unless absolute.

The example points at generated baseline deployment assets:

- `adaptations/trigger/triggers.toml` composes review on PR open/update, judge
  after review, judge after a no-op fix, fix after a material judge decision, and
  finish on the configured label once the PR is converged, clean and current.
- `adaptations/prompt/prompt-pack.toml` supplies the review, judge and fix prompt
  templates, baseline judgement briefs and ensemble workflow scripts.
- `adaptations/mechanical/mechanical.toml` declares the source preparation command
  `pump19-prepare-source`. That command receives source-preparation JSON on
  stdin and must return `{"tree":"...","revision":"..."}`.
- `commands/pump19-forgejo-poll` reads open pull requests from Forgejo REST and
  emits the daemon's polling snapshot JSON.
- `commands/pump19-forgejo-operation` performs the credentialed write side:
  post/update/resolve comments, apply labels, merge PRs, and append fix commits
  to the PR head branch with a non-force `git push`.

Relative command paths containing `/` are resolved relative to the daemon config
file, just like the pack and state paths. Bare command names still resolve
through `PATH`.

The generated packs carry the current contract version from `pump19-contract`
(`1.4` at this revision).

Regenerate the baseline deployment assets with:

```sh
cargo run -p pump19-adaptations --example write_baseline_packs -- examples/deployment
```

The normal test suite checks that regenerating produces the checked-in pack and
command files. Organisations override baselines by copying the generated pack or
command directories, editing the copy, and pointing `trigger_pack`,
`prompt_pack`, `mechanical_pack`, `forgejo.poll_command.program` or
`forgejo.operation_command.program` at the replacement. The core reads the
contracts, not the directory names.

## Fill Operator Values

Replace every `REPLACE_*` value before starting the daemon:

- `forgejo.repositories`: repository slugs in the form the polling command
  expects, for example `acme/widgets`.
- `FORGEJO_TOKEN`: the token consumed by both Forgejo command adapters.
- `--base-url`: the Forgejo instance URL consumed by the baseline command
  arguments.

If any `REPLACE_*` marker remains, startup fails before loading packs or polling:

```text
unfilled placeholder in daemon config examples/deployment/pump19-daemon.toml at forgejo.repositories[0]: "REPLACE_WITH_OWNER/REPOSITORY"; expected replace every REPLACE_* marker before starting the daemon
```

## Forgejo Token

Forgejo scoped tokens group permissions by route. Pump-19's command adapters need
the token to cover:

- `read:repository` to read repository and pull-request state.
- `write:repository` to push non-force fix commits to PR head branches and, when
  finish policy allows it, merge PRs.
- `write:issue` to create/update/resolve PR comments and apply labels.

Limit the token to the repositories Pump-19 watches when your Forgejo instance
supports repository-scoped tokens. Keep the token in the command environment, as
shown by `FORGEJO_TOKEN` in the example TOML; do not bake it into the workspace
image or adaptation packs. Forgejo's own scope reference is at
<https://forgejo.org/docs/latest/user/token-scope/>.

## Forgejo Command Contracts

The baseline scripts use Forgejo's REST API under `/api/v1`, authenticate with
`Authorization: token $FORGEJO_TOKEN`, and expect repository slugs such as
`acme/widgets`. Forgejo's API authentication guide is at
<https://forgejo.org/docs/latest/user/api-usage/>.

The polling command is executed as:

```text
<program> <configured args> <repository>
```

It writes a JSON array of `ForgejoPullRequestSnapshot` objects to stdout. The
daemon uses these fields:

```json
{
  "repository": "acme/widgets",
  "id": "42",
  "head_sha": "abc123",
  "base_sha": "main123",
  "branch_currency": "current",
  "cleanliness": "dirty",
  "mergeability": "unknown",
  "labels": [],
  "actor_permissions": []
}
```

The baseline poll command fetches open PRs, the current token user, and issue
timeline entries for the configured finish label. It marks a snapshot `clean`
when the finish label is present, records the latest finish-label actor when the
timeline exposes one, and marks branch currency `current` when Forgejo reports a
merge base equal to the PR base SHA. If Forgejo cannot expose a field, the script
uses the contract's conservative `unknown` value.

The operation command receives one JSON object on stdin and returns:

```json
{ "operation_id": "forge-operation-id", "new_head_sha": null }
```

The `operation` tag is one of `post_comment`, `update_comment`,
`resolve_comment`, `apply_label`, `merge`, or `push_fix_commits`. Every operation
includes `metadata` with the observed head SHA, an optional expected head SHA, an
idempotency key and the core's authorisation reason. Comment operations and fix
pushes carry expected-head guards; for `push_fix_commits`, the command also
receives top-level `expected_head_sha` and the commits to append. The command must
reject stale heads instead of force-pushing or publishing against an old PR head.

For the baseline operation command, guarded comment operations and fix pushes
first re-read the PR head. If it has moved, the command exits non-zero and writes
the contract error shape to stderr:

```json
{
  "error": "head_moved",
  "expected_head_sha": "abc123",
  "actual_head_sha": "new456"
}
```

The daemon preserves that as the typed `head_moved` publication refusal in run
state. Other command failures are treated as transport failures and left noisy.

The baseline fix-push path clones the repository, checks out the PR head branch,
applies `unified_diff` patch changes when present, creates empty commits for
description-only changes, and pushes `HEAD:<pr-head-ref>` without force. Forked
or heavily customised Forgejo workflows may need a site-specific override
script; keep the JSON contract unchanged.

## Optional Knobs

`forgejo.core_applies_finish_label_on_convergence` defaults to `false`. Keep it
false for a human merge gate: a person applies `finish_label` after convergence.
Set it true only when the repository policy explicitly accepts auto-merge of
agent-authored code.

`loop_control.poll_interval_ms` defaults to `5000`.
`loop_control.stop_after_quiet_polls` defaults to unset, which means the daemon
keeps polling until SIGINT or SIGTERM. Set it for smoke runs.

The run ceiling is currently stored in per-PR run state (`ceiling`) rather than
daemon TOML. Leaving it absent means no core ceiling is enabled. This is an
operator-surface gap if deployments need a global default ceiling.

## Run State And Publication Records

JSON run state is stored under `state_root`, keyed by repository, PR and commit.
The current state includes:

- `current_head_sha`, used to keep stale events from bypassing the current head.
- `run_history`, where each run record carries `run_id`, `run_kind`, `status`,
  optional `outcome`, optional typed `refusal`, and optional
  `ensemble_archive_path`.
- `publication.attempts`, where each attempted PR side effect records the
  operation, idempotency key, optional `expected_head_sha`, status, receipt, error
  and typed refusal.
- `ceiling`, the optional per-PR runaway guard.

When the launch gate refuses a run because the ceiling is reached, the run is
recorded as `skipped` with a `run_ceiling_reached` refusal. When a required model
family cannot be prepared, the refusal reason is
`required_family_unavailable`. These records are the durable audit trail; the log
line is only the operator-facing symptom.

## systemd Unit

Adjust paths, user and environment file names for the host:

```ini
[Unit]
Description=Pump-19 daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=pump19
WorkingDirectory=/opt/pump19/examples/deployment
EnvironmentFile=/etc/pump19/forgejo.env
ExecStart=/opt/pump19/target/release/pump19-daemon /opt/pump19/examples/deployment/pump19-daemon.toml
Restart=on-failure
RestartSec=10s

[Install]
WantedBy=multi-user.target
```

## Healthy Logs

The daemon writes one JSON object per line to stderr.

Quiet poll with no work:

```json
{"level":"info","event":"quiet_poll","fields":{"quiet_polls":1,"stop_after_quiet_polls":null}}
```

Successful dispatch:

```json
{"level":"info","event":"dispatch_outcomes","fields":{"event_index":1,"launches":1,"outcomes":[{"outcome":"launched","rule_id":"review-on-pr-change","run_id":"review-on-pr-change-1"}],"pending_events":0}}
```

Clean shutdown summary:

```json
{"level":"info","event":"daemon_summary","fields":{"events_processed":1,"launches":1,"quiet_polls":0,"recovered_completion_events":0,"recovered_terminal_events":0,"recovered_stale_running_events":0,"recovery_errors_continued":0,"dispatch_errors_continued":0,"stopped_by_shutdown":true}}
```

## Failing Logs

An unreachable Forgejo polling command is contained as an event-source dispatch
error. With `stop_after_quiet_polls = 1`, the daemon records the error and exits
cleanly after the configured quiet/error poll budget:

```json
{"level":"error","event":"dispatch_error_continued","fields":{"class":"event_source","error":"event source failed: Forgejo activity source failed: command exited with Some(7): connection refused","continued_errors":1,"retry_after_poll_interval":true}}
```

Fatal state-store or serialisation errors stop the daemon:

```json
{"level":"error","event":"daemon_fatal_core_error","fields":{"class":"state_store","error":"state store failed: ..."}}
```

When a required model family cannot be prepared, the daemon classifies the core
error as `required_family_unavailable`; the corresponding run-state record carries
the typed refusal.
