# Pump-19

Agent-native code verification. The first deliverable is an automated,
independent PR review-and-fix service — see `AGENTS.md` for the corrected
architecture stance.

The previous implementation was removed under a containment episode (see
`CONTAINMENT.md`), and this rebuild works from the corrected commission in the
sibling annexe. Quarantined code remains readable in git history as evidence,
not as a base.

## Service Shell

The rebuilt shell is deliberately small:

- `pump19 receive --config /etc/pump19` listens for forge webhooks at
  `/hooks/{forge}`.
- `pump19 sweep --config /etc/pump19` is the cron-runnable reconciliation pass.
- `pump19 run-wrap --config /etc/pump19` owns run mechanics: atomic run claim,
  log and metadata creation, workspace preparation, body execution, and
  workspace cleanup.
- `scripts/run-body/run-body` is the deployed agent-session launcher. It serves
  review, fix, finish, and flaky-test repair runs. An empty `PUMP19_RUN_BODY`
  still selects `pump19 stub-run` for mechanical shell tests.
- `pump19 run-guard` exposes the current-head, terminal-status, and superseding-
  run checks used by accountable sessions.
- `pump19 adapt` invokes a configured, service-owned forge adaptation with the
  dedicated forge credential.
- `pump19 review`, `pump19 marker`, `pump19 handle`, and `pump19 provenance`
  expose changed-line and anchor checks, stable finding identities, marker
  formatting, and resolved-model pin verification to the session.
- `pump19 capture-claude` records Claude's JSONL audit stream and validates the
  engine-reported lead model before the session may publish.
- `pump19 ws-exec --config /etc/pump19 -- command ...` runs PR-controlled build
  or test commands inside `PUMP19_WORKSPACE` with configured service credentials
  scrubbed from the environment.

The Go binary does not construct forge API URLs. Forge-specific reads and writes
live in executable adaptation scripts under `scripts/adaptations/forgejo/`; a
future forge swap supplies a different script directory and configuration.

## Vocabulary

In-flight labels are `Reviewing`, `Fixing`, `Finishing`, and
`Repairing Flaky Tests`. Verdict/control labels are `Converged`,
`Standing Findings`, `Partial Coverage`, `Ready`, and the standing safety
condition `Flaky Tests`.

Commit status contexts are:

- `pump19/review`
- `pump19/fix`
- `pump19/finish`
- `pump19/flaky` (successful landed repairs only; failures stay in `run.log`)

Only `success`, `failure`, and `error` are used. Forgejo's `warning` state is
intentionally avoided so the vocabulary survives a GitHub adaptation.

Machine-readable marker lines are trailing prose footers with this grammar:

```text
Pump-19: key=value another-key=value
```

Values contain no spaces. Unknown keys are ignored by consumers.

## Run-Body Contract

Every spawned run receives:

```text
PUMP19_RUN_DIR        run directory containing run.log, meta.env, diff.patch
PUMP19_RUN_KIND       review | fix | finish | flaky
PUMP19_OCCASION       triggering occasion, or reconcile
PUMP19_FORGE          configured forge name
PUMP19_REPO           owner/name
PUMP19_OWNER          repository owner
PUMP19_REPO_NAME      bare repository name
PUMP19_PR             pull request number
PUMP19_HEAD_SHA       full head SHA the run serves
PUMP19_BASE_REF       base branch
PUMP19_WORKSPACE      prepared clone-shaped checkout
PUMP19_DIFF           PR diff path
PUMP19_ADAPTATION     forge adaptation scripts directory
PUMP19_SKILL          run skill/prompt file path
PUMP19_RUN_BODY       run-body executable path; empty means pump19 stub-run
PUMP19_BRIEFS         brief directory
PUMP19_CONFIG         configuration root
PUMP19_UNIT           transient systemd unit name
```

The run directory path is the `(PR, head SHA, run kind)` claim. `run-wrap`
creates it with atomic `mkdir`; a loser exits cleanly and touches nothing.
`PUMP19_WORKSPACE` is a per-run path under the host temp directory, and
`run-wrap` removes it on normal exit. The run body owns forge-visible state: it
re-reads PR facts, applies and removes its in-flight label, guards against head
changes before posting, writes the terminal status for the served head, and
emits diagnostics captured in `run.log`. The sweep reads labels, statuses,
run-log mtimes, and the mechanical `meta.env` and `retry.env` files. It never
reads prose or log content as loop state.

Reaping is fail-closed. A stale run directory is renamed aside only after the
transient unit has been stopped and verified gone; `.reaped-*` directories are
evidence and never live claims. A claim that dies before an in-flight label is
visible is reaped by the same fail-closed path once its `run.log` or directory
mtime is stale.

Operational failures are run evidence, not pull-request outcomes: they write no
comment or `error` commit status. Before every mutating adaptation outside the
run-guard claim/release seam, the shared dispatcher atomically records
`forge-writes-attempted.env` in the run directory. Stage-label, eyes-reaction,
and assignment mutations made by run-guard are idempotent claim state and use an
explicit replay-safe path; mission-driven calls to the same verbs remain
tracked. A pre-body failure, or a body failure with no such record, writes
`retry.env`; the sweep preserves each expired attempt as `<claim>.retry-N` and
admits the next attempt only after the liveness interval. The default cap is
five attempts, configurable per repository. The final attempt receives an
atomic `terminal.env` marker and no longer reconciles for that head and run
kind. A body failure after a forge write was attempted latches immediately,
because publication may be partial. Controlled mission failures, stub crashes,
and stale crashes after an attempted write use the same marker. A new head has
a distinct identity and runs normally; `pump19 re-arm` explicitly removes a
same-head latch while retaining its marker as an audit copy.

Re-arm only the exact identity the operator has inspected:

```sh
pump19 re-arm --config /etc/pump19 --forge FORGE --owner OWNER --repo REPO \
  --pr PR --head FULL_SHA --run review|fix|finish|flaky
```

Forgejo 14.0.5 commit-status writes are ordered by their monotonic `id`; its
status payload has no `created_unix`. The combined-status endpoint renders an
individual `error` as `failure`, so the shell reads per-status state whenever
the `error`/`failure` distinction matters.

The receiver resolves Forgejo `label_updated` deliveries through the issue
timeline before trigger evaluation. Forgejo 14.0.5 records label additions and
removals as timeline rows with `type:"label"`; `body:"1"` means added and
`body:""` means removed. The resulting occasion uses the generic
`label-added:{Name}` or `label-removed:{Name}` vocabulary; for label additions,
actor guards use the actor returned by `label-actor`, binding merge permission
to the act of applying the label. The receiver reads the latest timeline label
event, so a later label write may mask the delivered event; the safe polarity is
delay only, because the sweep's state-derived finish implication recovers a
missed `Ready`, while a masked event must not wrongly trigger a run. On the
reconcile path, fix actors come from the review status creator and finish actors
come from `label-actor(Ready)`. Review actor guards are receiver-path-only
because the open-PR state read has no delivery actor to recover.

## End-To-End Harness

`go test ./internal/shell` includes a hermetic review-run-body journey through
the real `run-wrap` path. Its deterministic engine stand-in proves a new finding
post, stable-handle comment update, clean convergence, partial coverage,
outcome labels/statuses, and a loud resolved-model mismatch without model cost.
Focused tests separately prove base-ref governing extraction and degraded
`model-unknown` provenance.

`make e2e` is the stable disposable-Forgejo journey gate. It builds `pump19`,
starts Forgejo 14.0.5 in Docker, installs a local webhook through a
capture-forward endpoint, and drives:

- PR opened -> receiver -> `Reviewing` -> stub review status/log -> label clear.
- Duplicate delivery replay -> no duplicate review status.
- Hung stub killed mid-flight -> sweep reap -> label clear -> re-trigger.
- Head updated mid-run -> stale output discarded -> new head completes.
- Superseded-head hang -> new head completes -> sweep reaps the abandoned old
  head without reading forge status for the old SHA.
- Listener down during delivery -> sweep reconciles the missed review.
- Unauthorised `Ready` add -> sweep clears it without triggering finish; authorised
  `Ready` re-add -> finish runs.
- Transient `prepare-workspace` failure -> one bounded retry -> review
  completes.
- Persistent review, fix, or finish run-body failure -> `error` status on the
  served head and no sweep re-trigger loop. Flaky-repair failures remain private
  in the run log and leave `Flaky Tests` standing.
- Same-second status writes agree with Forgejo combined-status ordering; the
  live Forgejo timeline and pulls paging assumptions are pinned.

Set `PUMP19_E2E_UPDATE_FIXTURES=1 make e2e` to refresh
`internal/shell/testdata/forgejo14/` from the same run. The normalisation tests
bind to those captured Forgejo 14.0.5 payloads, headers, and timeline fixtures.

## Deployment

The repository carries production-shaped configuration templates, systemd user
units, and the ordered operator procedure in [`docs/go-live.md`](docs/go-live.md).
`scripts/e2e/deployment-smoke.sh` rehearses the receiver, an authenticated
synthetic delivery, the sweep, and unit-file validation without contacting a
real forge.
