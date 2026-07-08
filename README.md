# Pump-19

Agent-native code verification. The first deliverable is an automated,
independent PR review-and-fix service — see `AGENTS.md` for the corrected
architecture stance.

This repository is between builds: the previous implementation was removed
under a containment episode (see `CONTAINMENT.md`), and the rebuild works from
the corrected commission in the sibling annexe. It remains readable in git
history as evidence, not as a base.

## Service Shell

The rebuilt shell is deliberately small:

- `pump19 receive --config /etc/pump19` listens for forge webhooks at
  `/hooks/{forge}`.
- `pump19 sweep --config /etc/pump19` is the cron-runnable reconciliation pass.
- `pump19 run-wrap --config /etc/pump19` owns run mechanics: atomic run claim,
  log and metadata creation, workspace preparation, body execution, and
  workspace cleanup.
- `pump19 stub-run` is this unit's stand-in body. Later run-skill units replace
  it with real review, fix, and finish agents without changing the wrapper
  contract.
- `pump19 ws-exec --config /etc/pump19 -- command ...` runs PR-controlled build
  or test commands inside `PUMP19_WORKSPACE` with configured service credentials
  scrubbed from the environment.

The Go binary does not construct forge API URLs. Forge-specific reads and writes
live in executable adaptation scripts under `scripts/adaptations/forgejo/`; a
future forge swap supplies a different script directory and configuration.

## Vocabulary

In-flight labels are `Reviewing`, `Fixing`, and `Finishing`. Verdict/control
labels are `Converged`, `Standing Findings`, `Partial Coverage`, and `Ready`.

Commit status contexts are:

- `pump19/review`
- `pump19/fix`
- `pump19/finish`

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
PUMP19_RUN_KIND       review | fix | finish
PUMP19_OCCASION       triggering occasion, or reconcile
PUMP19_FORGE          configured forge name
PUMP19_REPO           owner/name
PUMP19_PR             pull request number
PUMP19_HEAD_SHA       full head SHA the run serves
PUMP19_BASE_REF       base branch
PUMP19_WORKSPACE      prepared clone-shaped checkout
PUMP19_DIFF           PR diff path
PUMP19_ADAPTATION     forge adaptation scripts directory
PUMP19_SKILL          run skill path
PUMP19_BRIEFS         brief directory
PUMP19_CONFIG         configuration root
```

The run directory path is the `(PR, head SHA, run kind)` claim. `run-wrap`
creates it with atomic `mkdir`; a loser exits cleanly and touches nothing. The
run body owns forge-visible state: it re-reads PR facts, applies and removes its
in-flight label, guards against head changes before posting, writes the terminal
status for the served head, and keeps `run.log` warm. The sweep reads only labels,
statuses, and run-log mtimes; it never reads prose or log content as loop state.

Reaping is fail-closed. A stale run directory is renamed aside only after the
transient unit has been stopped and verified gone; `.reaped-*` directories are
evidence and never live claims.

## End-To-End Harness

`make e2e` is the stable disposable-Forgejo journey gate. It builds `pump19`,
starts Forgejo 14.0.5 in Docker, installs a local webhook through a
capture-forward endpoint, and drives:

- PR opened -> receiver -> `Reviewing` -> stub review status/log -> label clear.
- Duplicate delivery replay -> no duplicate review status.
- Hung stub killed mid-flight -> sweep reap -> label clear -> re-fire.
- Head updated mid-run -> stale output discarded -> new head completes.
- Listener down during delivery -> sweep reconciles the missed review.

Set `PUMP19_E2E_UPDATE_FIXTURES=1 make e2e` to refresh
`internal/shell/testdata/forgejo14/` from the same run. The normalisation tests
bind to those captured Forgejo 14.0.5 payloads and headers.
