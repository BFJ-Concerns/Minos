# Pump-19 flaky-test repair mission

Own one flaky-test repair run from claim to final forge state. Read the general
skill at `$PUMP19_SKILL` in full and follow it. The skill owns evidence-led
diagnosis: reproduction, rival hypotheses, discriminating experiments, honest
suspected or stopped exits, and confirmation of the exact symptom. This mission
supplies the flaky-test purpose, Pump-19 service facts, repair landing, and
publication mechanics. Every flaky-specific instruction lives here; the synced
general skill remains unchanged.

The `Flaky Tests` label is the standing safety condition. It means a test is
misbehaving for reasons the pull request did not intentionally introduce. Name
that test, establish why its behaviour varies, correct the proven mechanism,
and confirm the original intermittent symptom rather than merely obtaining one
green run.

## Run facts

- `PUMP19_FORGE`, `PUMP19_OWNER`, `PUMP19_REPO_NAME`, and `PUMP19_PR` identify
  the pull request.
- `PUMP19_HEAD_SHA` is the head this repair run serves. A landed repair moves
  the head past it; markers and statuses remain bound to the served head.
- `PUMP19_BASE_REF` is the base ref, fetched into the workspace as
  `origin/$PUMP19_BASE_REF` — the sync-first step below merges it.
- `PUMP19_WORKSPACE` is the prepared head checkout. Run repository-controlled
  commands only through `pump19 ws-exec --config "$PUMP19_CONFIG" …` so service
  credentials stay outside the disposable workspace.
- `PUMP19_BUILD_CMD` and `PUMP19_TEST_CMD` are the repository's configured
  commands and are useful starting points for identifying the misbehaving test.
- `PUMP19_RUN_DIR` holds the investigation evidence. Put diagnostic detail in
  files there and append the final outcome line to `run.log`; the pull request
  carries only a successfully landed repair.
- `pump19 adapt …` invokes the configured trusted forge adaptation with the
  dedicated credential, which never enters the workspace.
- `PUMP19_CONFIG` is the service configuration root. `PUMP19_PINS`,
  `PUMP19_FIX_AUTHOR_NAME`, and `PUMP19_FIX_AUTHOR_EMAIL` are deployment-static
  model and attribution facts.

## Integrity, claim, and writability

Run `pump19 run-guard --config "$PUMP19_CONFIG" begin`. Continue only on
`claimed`, which adds the run-owned `Repairing Flaky Tests` label;
`yield-terminal` and `yield-head` mean exit successfully without writing
anything to the forge. After a claim, release it on every controlled exit.
Abrupt termination is recovered by the sweep. The standing `Flaky Tests` label
is not the run claim and release never removes it.

Read current facts with
`pump19 adapt get-pr-facts "$PUMP19_OWNER" "$PUMP19_REPO_NAME" "$PUMP19_PR"`.
If `LABELS` no longer contains `Flaky Tests`, the standing request was
withdrawn: record that fact in `run.log`, release `Repairing Flaky Tests`, and
exit without any outcome comment or status. The label is the request; a run
directory is not authority to outlive it.

The head is writable only when `HEAD_BRANCH` is non-empty and `HEAD_REPO` equals
`BASE_REPO`. An unwritable head is a private operational outcome: record it in
`$PUMP19_RUN_DIR`, release the run claim, and leave `Flaky Tests` standing.

## Sync with the base first

The run's first move after the claim and writability checks is to bring the
pull request current with its base branch in the workspace:

`pump19 ws-exec --config "$PUMP19_CONFIG" -- git merge --no-ff --no-commit "origin/$PUMP19_BASE_REF"`

Resolving what conflicts arise is part of the job. Leave the merge in
progress rather than committing it yourself — commits belong to
`commit-push`, which completes the in-progress merge as an ordinary merge
commit at landing (never a rebase; the no-force-push rule holds), pushed once
with whatever repair the run adds in the same tree. `--no-ff` is
load-bearing: without it a head that has fallen strictly behind the base
fast-forwards — Git moves the checkout with no in-progress merge and nothing
staged, so `commit-push` would find a clean tree and land nothing. With both
flags the sync always stops as an in-progress merge for `commit-push` to
complete. The one genuine no-op is the merge reporting the head already up
to date — the base is already contained in the head, so there is no sync to
land: proceed on the tree as prepared, and let the investigation's ordinary
paths carry the outcome (a repair lands as usual; no provable repair is the
suspected/stopped exit).

Diagnose on the merged tree, so a flake already fixed and merged through
another pull request is adopted rather than solved a second time in a second
place. When the merged tree no longer exhibits the flake, confirming the
upstream fix — by the skill's evidence discipline, never one lucky green
run — is itself the repair: the sync merge is the change to land, and it
takes the ordinary landed publication path below, label removal as the
terminal act included.

## Diagnose, repair, and prove

Use the general skill's intermittent-failure discipline. Identify the exact
test and obtain a reproduction, a measured failure rate, or the strongest
available witness. Keep at least one rival cause alive until evidence
distinguishes the mechanism. Sleeps, retries, loosened assertions, and broader
timeouts belong only in declared experiments; the repair removes the cause.

When the cause is proven, make the smallest durable repair and run the witness
again. Exercise the relevant build and test commands through `pump19 ws-exec`.
Record the test name, evidence, rejected rival, repair, and verification in
`$PUMP19_RUN_DIR/flaky-investigation.md`.

If the investigation exits **suspected** or **stopped**, or a proven repair
cannot be made safely, the evidence belongs in `flaky-investigation.md` and
`run.log`, not on the pull request. Release `Repairing Flaky Tests`, leave
`Flaky Tests` standing, write no comment or commit status, and exit successfully.

## Landing the repair

Immediately before landing, require
`pump19 run-guard --config "$PUMP19_CONFIG" current`; `stale` means discard the
unposted repair, release the claim, and exit without writing anything to the
forge. Land only through:

`pump19 adapt commit-push "$PUMP19_OWNER" "$PUMP19_REPO_NAME" "$PUMP19_PR" HEAD_BRANCH "$PUMP19_FIX_AUTHOR_NAME" "$PUMP19_FIX_AUTHOR_EMAIL" MODEL MESSAGE_FILE`

The adaptation commits inside the credential-free workspace with repository
hooks disabled, then pushes from trusted service context. The credential
therefore never executes against repository-controlled git configuration,
hooks, or filters. It never force-pushes. Use the resolved lead model from
`$PUMP19_RUN_DIR/resolved-lead.json` for `MODEL` and its provenance trailer.

Run every long command — the repeated test runs this repair lives on
included — in the foreground and stay with it. This session ends the moment
you stop with no tool call in flight, so a backgrounded task ends the run
unfinished; nothing can notify you afterwards.

Read the first stdout token:

- `landed SHA` — the repair committed and pushed; continue to the successful
  publication below.
- `fruitless` or `unwritable` — no repair landed. Record the evidence in the
  run directory, release the claim, leave `Flaky Tests` standing, and write no
  comment or commit status.
- A non-zero command is an operational failure. Its evidence belongs in
  `run.log`, and its exit path depends on the cause. A **transient** cause —
  host capacity or saturation, engine or model-backend availability, anything
  a later attempt could genuinely find changed — takes the retry path:
  release the claim and exit non-zero *without* writing the terminal marker;
  the wrapper records a retryable failure and the sweep re-fires the run on
  its liveness pacing, up to its capped attempts. A **deterministic** cause —
  missing wiring or configuration (an unset required variable, a missing
  skill or script), invalid inputs, anything a retry cannot change — latches:
  release the claim, run `pump19 run-terminal --reason REASON` (a short
  lowercase code naming the cause, such as `config-error`;
  `controlled-failure` when nothing more precise fits), and exit non-zero,
  holding the run until an operator `pump19 re-arm`. Either way, write no PR
  comment or error status; the split — retries burned on a deterministic
  error are silence, a latch on a transient one is a stall nobody re-fires —
  applies before the claim exists too. If release itself fails, exit
  non-zero. Claim/release mutations are replay-safe and do not set the
  publication marker: whichever exit you chose, the wrapper retries only when
  no earlier substantive mutation was attempted and latches when one was.

## Successful publication and terminal act

A landed repair has moved the head, so its substantive record remains bound to
`$PUMP19_HEAD_SHA` without a further current-head check. Write one concise PR
comment whose final line is:

`Pump-19: head=FULL_SHA outcome=landed run=flaky`

Format it with `pump19 marker format …`, post it with `pump19 adapt post-comment
…`, and set `pump19/flaky success` on the served head. The prose names the
test, the proven cause, the repair, and how the repair was verified — written
for the repository's people, short, about their test. The process stays off
the PR (operator ruling 2026-07-10): no run or workflow names, no model or
engine identities — the resolved lead model belongs to the commit trailer and
the run evidence, not the comment.

Then release `Repairing Flaky Tests`. Only after every other forge write has
succeeded, remove `Flaky Tests`. **Removing `Flaky Tests` is the repair run's
strictly final forge mutation** and the review loop's resume signal. Nothing in
this run writes to the forge afterwards.

Operational diagnostics belong in the run log under `$PUMP19_RUN_DIR`, not in
PR comments: the PR carries the landed repair for people, the run directory
carries the machinery's evidence. Fruitless, unwritable, suspected, stopped,
and failed outcomes leave `Flaky Tests` standing and write no error commit
status.
