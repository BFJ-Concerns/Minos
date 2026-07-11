# Pump-19 finish mission

Own one finish run from claim to final forge state. A finish run has no bespoke
skill: compose the build, test, and maintenance work over your general skills.
This mission supplies the Pump-19 service facts, the merge gate, and the
publication mechanics.

**Triggering is permissive; merging is gated.** The finish label triggered this
run whatever the head's review status reads — an errored or failed review on the
current head is part of your work, not a reason not to run. What protects the
tree is the gate below: the merge completes only when the branch is clean or
flagged, current, forge-mergeable, and passing the workspace build and test.
The receiver has already validated who applied the finish label and whether
repository policy authorises that actor. Treat that validated result as this
run's authority input, then apply the independent merge gate below rather than
repeating the receiver's actor-authorisation check.

## Run facts

- `PUMP19_FORGE`, `PUMP19_OWNER`, `PUMP19_REPO_NAME`, and `PUMP19_PR` identify
  the pull request.
- `PUMP19_HEAD_SHA` is the head this finish run considers. Every status and
  marker you write names it.
- `PUMP19_BASE_REF` is the base ref.
- `PUMP19_WORKSPACE` is the prepared head checkout. Run the repository's build,
  test, and maintenance commands only through
  `pump19 ws-exec --config "$PUMP19_CONFIG" …`, which scrubs the service
  credential from the environment PR-controlled code sees.
- `PUMP19_BUILD_CMD` and `PUMP19_TEST_CMD` are the repository's own optional
  workspace build and test commands. A non-empty command is part of this run's
  gate. An empty command means the repository's non-empty
  `[ci].required-checks` declaration names the forge-required checks that run
  that gate; skip the corresponding workspace step.
- `PUMP19_AUTO_MERGE` records whether the service's own convergence applies the
  finish label (auto-merge). It does not change your gate: you merge a validly
  labelled, gate-passing PR either way.
- `PUMP19_RUN_DIR` holds writable session evidence; `run.log` is the sweep's
  liveness signal.
- `pump19 adapt …` runs the configured forge adaptation with the dedicated
  credential, which never enters the workspace.
- `PUMP19_CONFIG` is the service configuration root; `PUMP19_PINS` is the
  deployment-static pins file.

## Integrity and claim

Run `pump19 adapt get-pr-facts "$PUMP19_OWNER" "$PUMP19_REPO_NAME" "$PUMP19_PR"`
and read `HEAD_BRANCH` and `MERGEABLE`. Eligibility to merge is read from the
**current head's review verdict**, not from a label (a label is PR-level and can
be stale from an older head); see the eligibility gate below.

Run `pump19 run-guard --config "$PUMP19_CONFIG" begin`. Continue only on
`claimed`, which adds `Finishing`; `yield-terminal` (a finish already ran for
this head) and `yield-head` (the head has moved) mean exit successfully without
acting. Run `pump19 run-guard --config "$PUMP19_CONFIG" release` on every
controlled exit path; it keeps `Finishing` when a newer live finish owns it.
`pump19 run-guard --config "$PUMP19_CONFIG" current` prints `stale` once a newer
head exists — a `stale` result is the `not-current` refusal below.

## The gate, in order

Stop at the first failure and record the matching refusal; do not merge.

1. **Current and base sync.** Require `pump19 run-guard … current`. `stale`
   means a newer PR head exists that this run is not serving; record `refused`
   with reason `not-current`.

   A `current` result establishes that this run still serves the PR head. If
   `origin/$PUMP19_BASE_REF` is not already an ancestor of that head, merge the
   base into the head in `$PUMP19_WORKSPACE` with an ordinary merge commit:
   never rebase or force-push. Merge conflicts are this run's work — resolve
   them, then exercise the applicable build and test gates against the merged
   tree. Land the sync through `pump19 adapt commit-push …`, attributed
   exactly like a maintenance change (step 5), and record `refused` with
   reason `review-pending` without attempting the forge merge. The PR update
   re-fires review; the sticky finish label carries the next finish run, which
   may merge only after the combined tree is re-cleared.

   The sync runs first, before eligibility, deliberately (operator ruling
   2026-07-11): the finish label is a standing instruction to get this PR
   merged, so a labelled head must never rot behind its base while it waits —
   an ineligible head still gets its sync, giving the loop a fresh combined
   tree to review and leaving nothing staleness-blocked for a human who
   decides to merge by hand.

2. **Eligibility.** The finish label triggered this run, but that only proves
   *who* asked to merge — not that the current head is in a state the commission
   permits merging. Eligibility rests only on **the service's own recorded
   verdict** for the current head: the trailing marker line the service wrote on
   its own review, keyed to this head. That machine channel is the authority
   precisely because only the service can produce it; a forge review's `state`
   or its author's name is deliberately not enough, since a display name is not
   proof of authorship and an approving review may come from someone outside the
   loop.

   Read `pump19 adapt list-reviews "$PUMP19_OWNER" "$PUMP19_REPO_NAME" "$PUMP19_PR"`.
   For each review, take the last `Pump-19:`-prefixed line of its body and parse
   it with `pump19 marker parse`. Consider only reviews whose marker parses with
   `run=review` and `head=$PUMP19_HEAD_SHA` — these are the service's own
   verdicts for this head; every other review (no service marker, or an older
   head) is human commentary and does not count, whatever its `state`. Take the
   latest such verdict:
   - **`verdict=converged`** — the service's converging review. The head is
     **clean**; eligible.
   - **`verdict=standing-findings`** — an authorised person's finish label is the
     explicit decision to proceed despite the verified standing findings. Record
     the head as **flagged** and eligible; if it merges, the summary must state
     that it merged with standing findings rather than describe it as clean.
   - **`verdict=partial-coverage`**, **`verdict=paused-flaky`**,
     **`verdict=bar-dissent`**, or **no service verdict for this head** — a
     finish label cannot substitute for a complete service review of the
     current head. `paused-flaky` is explicitly ineligible because the review
     withheld approval while `Flaky Tests` stood; `bar-dissent` because the
     review-bar check dissented from the published review, so convergence was
     withheld. Record
     `refused` with reason `not-eligible`: unreviewed, partially-reviewed,
     paused, or bar-disputed code cannot be merged on the strength of the
     finish label alone. The
     finish label is a standing instruction and stays sticky; a later finish
     runs once the loop produces an eligible verdict on the current head.

   Record clean vs flagged for the summary; both continue.
3. **Build.** When `PUMP19_BUILD_CMD` is non-empty, run
   `pump19 ws-exec --config "$PUMP19_CONFIG" -- sh -c "$PUMP19_BUILD_CMD"`.
   A non-zero exit is `refused` with reason `build-failed`. When it is empty,
   the configured required CI checks carry this gate; skip the workspace step.
4. **Test.** When `PUMP19_TEST_CMD` is non-empty, run
   `pump19 ws-exec --config "$PUMP19_CONFIG" -- sh -c "$PUMP19_TEST_CMD"`.
   A non-zero exit is `refused` with reason `test-failed`. When it is empty,
   the configured required CI checks carry this gate; skip the workspace step.
5. **Maintenance.** Perform the occasion's configured maintenance (dependency
   bumps, documentation checks) over your general skills. If it changes the
   tree, it must be reviewed before it can merge: land it with
   `pump19 adapt commit-push …` (as the fix mission documents, attributed to
   `PUMP19_FIX_AUTHOR_NAME`/`PUMP19_FIX_AUTHOR_EMAIL` with the resolved lead
   model), then record `refused` with reason `review-pending`. The landed PR
   update re-triggers review; the sticky finish label carries the next finish run,
   which merges the re-cleared tree. If maintenance changes nothing, continue.
6. **Forge-mergeable.** If `MERGEABLE` is not `true`, that is `refused` with
   reason `not-mergeable`.
7. **Merge.** `pump19 adapt merge "$PUMP19_OWNER" "$PUMP19_REPO_NAME" "$PUMP19_PR" merge`.
   The forge applies its own final gate (required checks, branch protection,
   up-to-date); a non-zero exit means the forge refused — record `refused` with
   reason `not-mergeable`, never a silent stop.

## Model provenance

The capture wrapper validated the served lead model against its pin and wrote
`$PUMP19_RUN_DIR/resolved-lead.json`; a mismatch has already failed the run
loudly. A finish run is a single accountable session with no Ensemble workers —
the lead record is the whole provenance. Use the resolved lead model for any
maintenance commit's `Pump-19-Model:` trailer.

Run every long command — the merge gate's builds and test suites included —
in the foreground and stay with it. This session ends the moment you stop
with no tool call in flight, so a backgrounded task ends the run unfinished;
nothing can notify you afterwards.

## Output contract

Record exactly one outcome as machine state — a terminal `pump19/finish`
status and the release of `Finishing`, plus a single PR comment with a
trailing marker **when the merge completed**. A refusal posts no PR comment
(operator ruling 2026-07-11): a refusal is service process, and process stays
off the PR — the status, whose description names the refusal reason plainly,
carries the machine outcome, and the run directory carries the account. The
merged comment's prose is one or two plain sentences about the merge outcome
itself; the process stays off the PR (operator ruling 2026-07-10): no run or
workflow names, no model or engine identities. The marker is:

`Pump-19: head=FULL_SHA outcome=merged|refused[ reason=REASON] run=finish`

with `reason` one of `not-eligible`, `build-failed`, `test-failed`,
`review-pending`, `not-current`, `not-mergeable`, present only on `refused`.

- **merged** — the merge completed. Remove the `Ready` label (the finish label
  is now consumed), post the summary, and set
  `pump19/finish success` on `$PUMP19_HEAD_SHA`. Because the merge moves the
  base branch, write the status and comment without a further `current` check —
  the finish did complete. If the repository configures post-merge release acts
  (tag, forge release), perform them here; they are commit-free.
- **refused** — the gate stopped. Leave `Ready` in place: it is sticky until a
  merge consumes it, so a later finish retries when the blocking condition
  clears. Write the summary (prose plus marker) to
  `$PUMP19_RUN_DIR/finish-summary.md` only, and set `pump19/finish failure` on
  `$PUMP19_HEAD_SHA` with a description naming the reason. Post nothing to
  the PR.

Format the marker with `pump19 marker format …`; on `merged`, post the summary
with `pump19 adapt post-comment …`, the body ending in exactly that one marker
line.
Any non-zero mechanical or forge command that this mission does not classify as
a refusal or a successful yield is a run failure. Classify the cause before
choosing the controlled exit, because the two failure paths lead somewhere
different:

- **Transient causes** — host capacity or saturation, engine or model-backend
  availability, anything a later attempt could genuinely find changed — take
  the retry path: release the claim when one exists, then exit non-zero
  *without* writing the terminal marker. The wrapper records a retryable
  failure and the sweep re-fires the run on its liveness pacing, up to its
  capped attempts; exhaustion latches on its own.
- **Deterministic causes** — missing wiring or configuration (an unset
  required variable, a missing skill or script), invalid inputs, anything a
  retry cannot change — latch: release the claim when one exists, run
  `pump19 run-terminal --reason REASON` (a short lowercase code naming the
  cause, such as `config-error`; `controlled-failure` when nothing more
  precise fits), then exit non-zero. The terminal marker holds the run until
  an operator `pump19 re-arm`.

The split is what keeps failure loud: retries burned on a deterministic error
are hours of silence, and a latch on a transient one is a stall nobody
re-fires. The classification applies before the claim exists too — there is
simply no claim to release. If release itself fails, exit non-zero.
Claim/release mutations are replay-safe and do not set the publication
marker: whichever exit you chose, the wrapper retries only when no earlier
substantive mutation was attempted and latches when one was. Write no error
comment or commit status. A release failure after a terminal status is
operational clean-up for the sweep, not a falsified result.
