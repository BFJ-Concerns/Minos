# Deploy `pump19-daemon`

Updated: 2026-07-05

This guide deploys the command-backed Forgejo daemon shipped in this repository.
The baseline deployment assets are examples, not hidden defaults: copy them,
replace the placeholders, and point the daemon at the copy.

## Build

```sh
cargo build --release -p pump19-daemon
```

The binary is `target/release/pump19-daemon`.

## Prerequisites

- Rust matching the workspace `rust-version`.
- `/usr/bin/podman` for disposable review, fix and finish workspaces.
- Node.js and an Ensemble launcher at `ensemble.launcher_path`.
- Authenticated engine CLIs on the daemon host:
  - `claude` for the shipped review lead.
  - `codex` for the shipped fixer and the Codex reviewer/verifier/bar-check targets.
  - `opencode` only when a copied pack adds OpenRouter/opencode breadth.
- `curl`, `jq`, `git` and `tar` for the baseline Forgejo and preparation commands.
- A workspace image named by `workspace.image`, containing subject-repository
  build tools but no forge credentials.

## Example Layout

Start from [`examples/deployment/pump19-daemon.toml`](../examples/deployment/pump19-daemon.toml).
Paths in that file resolve relative to the TOML file unless absolute.

The example uses these checked-in assets:

- `adaptations/trigger/triggers.toml`: the five-rule trigger pack.
- `adaptations/prompt/prompt-pack.toml`: contract `2.0` prompt pack with
  mission prompts, markdown review briefs and slot-keyed workflow scripts.
- `adaptations/mechanical/mechanical.toml`: source preparation, merge readiness
  and comment formatting commands.
- `commands/pump19-forgejo-poll`: Forgejo polling snapshot command.
- `commands/pump19-forgejo-operation`: credentialed comment, label, merge and
  fix-push command.
- `commands/pump19-prepare-source`: git archive preparation command.
- `commands/pump19-merge-readiness`: finish-run build/test check.
- `commands/pump19-format-comments`: finding comment body renderer.

Regenerate the baseline assets after changing their generator:

```sh
cargo run -p pump19-adaptations --example write_baseline_packs -- examples/deployment
```

The test suite checks that regenerated assets match the checked-in examples.

## Contract 2.0 Flow

The shipped packs and daemon use contract `2.0`. The judge/decision vocabulary is
gone. A review run now records:

- candidate findings with `title`, `explanation`, optional `suggestion`,
  `priority`, producer provenance and per-finding verification;
- a `CoverageRecord` with the lead session's visited/unvisited account;
- one `ReviewVerdict`: `Converged`, `FindingsPosted`, `StandingFindings`,
  `BarFailed`, `BarCheckDegraded` or `PartialCoverage`;
- typed `session_archives` for the lead transcript and every workflow agent
  session that actually ran.

Publication is derived from contract state. Only verified material findings post
to the PR. Material means `verification.status == verified` and priority at or
above the repository policy threshold; the shipped daemon policy is P0-P1. A
review can converge only when there are zero verified material findings, coverage
is complete, and the review-bar check passed.

## Trigger Pack

The baseline trigger pack contains five independent rules:

| Rule | Fires | Run |
|------|-------|-----|
| `review-on-pr-change` | PR opened or updated, and ready | Review |
| `fix-after-material-review` | Successful review with verified material findings | Fix |
| `review-after-noop-fix` | Fix run completed with `no_op` | Review |
| `finish-on-label` | Finish label applied, state converged, clean and current | Finish |
| `review-after-degraded-bar-check` | Review completed with `BarCheckDegraded` | Review |

Runs do not call each other directly. The loop emerges from these rules and the
per-PR run state.

## Prompt Pack

The prompt pack has one manifest, one brief directory and five workflow slots.

`prompt-pack.toml` declares:

- `review-lead-mission`, the lead review session mission.
- `fix-material-findings`, the writable fix-session mission.
- baseline markdown briefs in `briefs/`.
- workflow scripts by slot:
  `specialist-fanout`, `verify-findings`, `assemble-review`, `bar-check`,
  `repair-output`.

The review frame materialises a run directory under `ensemble.archive_root`:

```text
frame-runs/<run-id>/
  manifest.json
  mission.md
  bin/pump19-workflow
  bin/pump19-exec
  workflows/
  out/
  archive/
```

`pump19-workflow <slot> <input.json>` runs only a configured slot script and
records the Ensemble archive under the run archive. `pump19-exec <cmd...>` is
the sanctioned command an agent uses for PR-code execution inside the workspace
container.

## Review And Fix Bodies

Review runs use `LeadSessionReviewBody`:

- prepare a manifest from the PR diff, changed-line map, base-pinned governing
  content, selected briefs, loop history and policy;
- write the run directory and shims;
- make the host lease tree read-only;
- launch the Claude-class lead session;
- repair invalid review output through the `repair-output` workflow within the
  configured attempt budget;
- gate findings on schema validity, changed-line anchors, deduplication,
  verification and no-self-verification;
- reconcile workflow archives to authorised targets before publication.

Fix runs use `LeadSessionFixBody`:

- keep the host lease tree writable;
- snapshot the pristine prepared tree before the fixer starts;
- launch the Codex-class fixer from the authorised `fixer-codex` target;
- compare pristine and post-fix snapshots with `git diff --no-index`;
- return a `PatchChange::UnifiedDiff` for the existing credentialed fix-push path.

The fixer never receives forge credentials. The core-authorised Forgejo operation
path applies the diff, creates attributed commits with provenance trailers and
pushes normally to the PR head branch.

## Containment

The workspace boundary is credential-free, disposable and resource-bounded. The
workspace network is enabled so real builds can fetch dependencies; egress is
audited through session transcripts and `pump19-exec` use, not a packet proxy.

Review agents run on the host and reach into the prepared tree, but the host
tree is made read-only before the review lead starts. Toolchain execution uses
the container copy through `pump19-exec`.

Fix and finish runs may write the host lease tree. Forge writes remain outside
the workspace and happen only through the core-authorised credentialed operation
step.

## Forgejo Commands

Set `FORGEJO_TOKEN` in the daemon environment and inherit it with
`env:FORGEJO_TOKEN`. The baseline scripts avoid passing the token in argv.

The token needs enough Forgejo scope to:

- read repositories and pull requests;
- post, update and resolve PR comments;
- apply the finish label;
- push fix commits to watched PR head branches;
- merge PRs when the finish policy allows it.

The poll command emits normalised PR snapshots. The operation command receives a
single JSON object on stdin with an `operation` tag:

- `post_comment`
- `update_comment`
- `resolve_comment`
- `apply_label`
- `merge`
- `push_fix_commits`

Comment operations and fix pushes are expected-head guarded. If the PR head has
moved, the operation command returns the typed `head_moved` error shape and the
core records a publication refusal instead of writing to a stale head.

## Mechanical Commands

`pump19-prepare-source` clones from `PUMP19_GIT_BASE_URL`, archives the recorded
head commit into a `.git`-free prepared tree, and extracts review-governing
content from the PR base ref into `.pump19/review/governing/`.

`pump19-merge-readiness` is optional but enabled in the example pack. Finish runs
call it after the contract merge gate passes. It checks live head currency,
probes a merge against the current base and runs detected build/test commands in
a throwaway credential-free container. Remove the step from a copied mechanical
pack if the contract merge gate alone should decide finish readiness.

`pump19-format-comments` receives the finding, verification and forge facts, then
returns the comment body. The core still owns whether that body may be posted.

## Operator Values

Replace every `REPLACE_*` marker before startup. Startup fails before polling if
any marker remains.

Common values:

- `forgejo.repositories`: watched repository slugs such as `acme/widgets`.
- `--base-url`: Forgejo web/API root for the baseline poll and operation scripts.
- `PUMP19_GIT_BASE_URL`: clone base URL used by source preparation.
- `forgejo.finish_label`: label that authorises finish runs.
- `forgejo.web_base_url`: optional web root for file permalinks in comments.
- `forgejo.core_applies_finish_label_on_convergence`: false by default; set true
  only for repositories that explicitly accept agent auto-merge.

Optional repository intent lives in the daemon TOML under
`forgejo.repository_intents."<owner>/<repo>"`. Repositories without an entry use
neutral subject text derived from the slug.

## State And Logs

`state_root` stores per-PR JSON run state and `operator.log.jsonl`.

Run state records current head, loop history, findings, verdict, coverage,
patches, publication attempts, typed session archives and independence
degradations. Operator-log JSONL records startup/config warnings, launch
refusals, run failures and policy exhaustion. Operational failures are for the
operator, not PR comments.

Contract `1.x` state is not read by this daemon. Archive old state separately
before starting a `2.0` deployment.

## systemd Sketch

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

Example environment file:

```sh
FORGEJO_TOKEN=replace-with-token
PUMP19_GIT_BASE_URL=https://forgejo.example
# Optional merge-readiness overrides:
# PUMP19_MERGE_READINESS_IMAGE=localhost/pump19-workspace:stable
# PUMP19_MERGE_READINESS_TIMEOUT=3600
```
