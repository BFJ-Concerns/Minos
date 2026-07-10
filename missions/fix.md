# Pump-19 fix mission

Own one fix run from claim to final forge state. Read the general skill at
`$PUMP19_SKILL` in full and follow it — for a fix run the launcher points that
at the service's fix skill, and it, composed over the general debugging skill,
owns the fix method: how to read the loop, answer the findings, and land the
change. This mission supplies only Pump-19 service facts and publication
mechanics.

A fix run is a separate session from the review whose findings it fixes — a
fact of the spawn, and the loop's independence guarantee. Treat each posted
finding as a verified repair input: an independent review session has already
judged and verified it, so this separate fix run focuses on answering the
finding rather than repeating that review judgement.

## Run facts

- `PUMP19_FORGE`, `PUMP19_OWNER`, `PUMP19_REPO_NAME`, and `PUMP19_PR` identify
  the pull request.
- `PUMP19_HEAD_SHA` is the head this fix run answers. Your landing moves the
  head past it; every status and marker you write names this head, the one the
  fix acted against.
- `PUMP19_BASE_REF` is the base ref.
- `PUMP19_WORKSPACE` is the prepared head checkout — the tree you edit to make
  the fix. Run PR-controlled commands only through
  `pump19 ws-exec --config "$PUMP19_CONFIG" …`. You never push from inside it;
  landing is a service-credentialled adaptation (below).
- `PUMP19_DIFF` is the prepared base-to-head diff.
- `PUMP19_RUN_DIR` holds writable session evidence. The wrapper captures
  diagnostics in `run.log`; the sweep uses its activity as the liveness signal.
- `pump19 adapt …` selects the configured forge adaptation from `PUMP19_FORGE`
  and `PUMP19_CONFIG`, then invokes its trusted script with the dedicated forge
  credential — which never enters the workspace.
- `PUMP19_CONFIG` is the service configuration root.

`PUMP19_PINS`, `PUMP19_FIX_AUTHOR_NAME`, and `PUMP19_FIX_AUTHOR_EMAIL` are
deployment-static: the pins file and the service identity a landed fix is
attributed to.

## Head writability

Run `pump19 adapt get-pr-facts "$PUMP19_OWNER" "$PUMP19_REPO_NAME" "$PUMP19_PR"`
and read `HEAD_BRANCH`, `HEAD_REPO`, and `BASE_REPO`. The head is writable only
when `HEAD_BRANCH` is non-empty and `HEAD_REPO` equals `BASE_REPO`; a fork PR or
an empty head branch is an **unwritable head** with no fix path. If the head is
unwritable, do not edit or push: record the `unwritable` outcome below and exit.

## Integrity and claim

Run `pump19 run-guard --config "$PUMP19_CONFIG" begin`. A command failure is a
run failure. Continue only when it prints `claimed`; `yield-terminal` (a fix
already ran for this head) and `yield-head` (the head has moved) mean exit
successfully without acting. `claimed` adds the `Fixing` label.

After `claimed`, run `pump19 run-guard --config "$PUMP19_CONFIG" release` on
every controlled exit path. Abrupt termination is recovered by the sweep.
Release keeps `Fixing` when a newer live fix owns it.

`pump19 run-guard --config "$PUMP19_CONFIG" current` prints `current` while this
run still owns the live head and `stale` once a newer head exists. Because a
landed fix deliberately moves the head, use `current` differently from a review:
require it *immediately before you push*, and if it prints `stale`, discard the
fix and exit successfully without pushing — a newer head has superseded you.
For the fruitless and unwritable outcomes, which move no head, require `current`
before each forge write as a review would.

## Reading the loop

- `pump19 adapt list-review-comments "$PUMP19_OWNER" "$PUMP19_REPO_NAME" "$PUMP19_PR"`
  writes a JSON array of the posted inline comments. The verified material
  findings you answer are those whose trailing marker binds them to
  `head=$PUMP19_HEAD_SHA`, each carrying a stable `finding=F-XXXX` handle.
- `pump19 adapt list-reviews "$PUMP19_OWNER" "$PUMP19_REPO_NAME" "$PUMP19_PR"`
  writes the PR's review history — prior verdicts and pass count — for the
  skill to weigh how many times a finding has already survived a fix.

## Landing a fix

Edit the workspace to answer the findings, then land through the adaptation —
never a push from inside the workspace:

- `pump19 adapt commit-push "$PUMP19_OWNER" "$PUMP19_REPO_NAME" "$PUMP19_PR" HEAD_BRANCH "$PUMP19_FIX_AUTHOR_NAME" "$PUMP19_FIX_AUTHOR_EMAIL" MODEL MESSAGE_FILE`
  commits the workspace changes — attributed to the deployment's service fixing
  identity (`PUMP19_FIX_AUTHOR_NAME` / `PUMP19_FIX_AUTHOR_EMAIL`) with a
  `Pump-19-Model: MODEL` trailer — and fast-forwards the head branch. The commit
  runs credential-free with hooks disabled inside the workspace; the push runs
  from a trusted context outside it, so the credential never executes against
  workspace-controlled git config, hooks, or filters. It never force-pushes.
  Use the resolved lead model (below) for `MODEL`.
- `commit-push` reports the outcome as a **first-word token on stdout**, exiting
  zero for every handled outcome: `landed SHA` (committed and pushed),
  `fruitless` (nothing to commit), or `unwritable` (the remote refused the push
  — a protected head in the same repository, the second route to the unwritable
  outcome besides the fork check above). Read that token to set your outcome. A
  **non-zero** exit is an infrastructure failure and so a run failure. On that
  non-zero path, `pump19 adapt` exposes a generic command failure rather than the
  adaptation's stdout, so classify it from the exit status, not an outcome token.

## Model provenance

The capture wrapper validated the served lead model against its pin in
`$PUMP19_PINS` and wrote `$PUMP19_RUN_DIR/resolved-lead.json`; a mismatch has
already failed the run loudly, so a fix that reaches this point serves the
pinned model. Read the lead model from that file for the commit trailer and the
summary. A fix run is a single accountable session with no Ensemble workers, so
there is no worker archive and no provenance assembly — the lead record is the
whole provenance.

Run every long command — builds, test suites — in the foreground and stay
with it. This session ends the moment you stop with no tool call in flight,
so a backgrounded task ends the run unfinished; nothing can notify you
afterwards.

## Output contract

A fix run publishes exactly one outcome as machine state: a single PR comment
carrying the summary and a trailing marker, a terminal `pump19/fix` status, and
the release of its `Fixing` label. It does not publish a review, because review
judgement belongs to the independent review runs. The marker is:

`Pump-19: head=FULL_SHA outcome=landed|fruitless|unwritable run=fix`

- **landed** — one or more fixes were committed and pushed. Require `current`,
  run `commit-push`, then record the outcome: the push has moved the head, so
  the summary comment and the `pump19/fix` status are bound to
  `$PUMP19_HEAD_SHA` and are written without a further `current` check (the fix
  did complete for that head). The pushed PR update re-triggers review on its own;
  you neither call nor await it.
- **fruitless** — the findings were worked and no genuine change answers them.
  Change nothing on the branch. The standing findings remain the PR's verdict
  and the loop stops here.
- **unwritable** — the head cannot be written. The findings stand.

Post the summary with
`pump19 adapt post-comment "$PUMP19_OWNER" "$PUMP19_REPO_NAME" "$PUMP19_PR" BODY_FILE`,
where `BODY_FILE` ends with exactly one marker line. Format the marker with
`pump19 marker format head="$PUMP19_HEAD_SHA" outcome=OUTCOME run=fix`. Then set
the terminal status:
`pump19 adapt set-status "$PUMP19_OWNER" "$PUMP19_REPO_NAME" "$PUMP19_HEAD_SHA" pump19/fix success DESCRIPTION`.
All three outcomes are honest completions and take `success`; the substance
lives in the marker. Finally release `Fixing`.

Any non-zero mechanical, provenance, or forge command is a run failure unless
this mission classifies its result as a successful yield. (`commit-push` reports
its handled outcomes — including fruitless and unwritable — as a stdout token at
exit zero, so those are not command failures; only a genuine infrastructure
failure exits non-zero.) Before the run claim exists, exit non-zero; the wrapper
records the failure internally and the sweep retries it only when no forge write
was attempted. After `claimed`, use `pump19 run-guard --config
"$PUMP19_CONFIG" release` for the controlled exit, then run `pump19
run-terminal --reason controlled-failure`, then exit non-zero. If release itself
fails, exit non-zero. Claim/release mutations are replay-safe and do not set the
publication marker: the wrapper retries when no earlier substantive mutation
was attempted and latches when one was. Operational
diagnostics belong in the run log under `$PUMP19_RUN_DIR`, not in PR comments or
error statuses: the PR carries the outcome for people, the run directory carries
the machinery's evidence. A release failure after a terminal status is written is operational
clean-up: log it and leave the label for the sweep; it does not rewrite the
completed fix as an error.

## Skill-sync re-check

<!-- verify-on-arrival: the fix skill composes over the synced general debugging skill; that skill is present under skills/foundry. -->
