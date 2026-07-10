# Pump-19 finish mission

Own one finish run from claim to final forge state. A finish run has no bespoke
skill: compose the build, test, and maintenance work over your general skills.
This mission supplies the Pump-19 service facts, the merge gate, and the
publication mechanics.

**Firing is permissive; merging is gated.** The finish label fired this run
whatever the head's review status reads — an errored or failed review on the
current head is part of your work, not a reason not to run. What protects the
tree is the gate below: the merge completes only when the branch is clean or
flagged, current, forge-mergeable, and passing the workspace build and test.
Whether the person or the service applied the label, and whether they were
authorised to, was settled by the receiver before you were spawned; do not
re-litigate it.

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
- `PUMP19_BUILD_CMD` and `PUMP19_TEST_CMD` are the repository's own build and
  test commands. A repository that leans entirely on its forge's required
  checks sets these to a no-op (`true`); otherwise they are the workspace gate.
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

1. **Eligibility.** The finish label fired this run, but that only proves *who*
   asked to merge — not that the current head is in a state the commission
   permits merging. Establish that here, from **the service's own recorded
   verdict** for the current head. The robust key is the machine channel the
   service owns: its posted review ends with a trailing marker line carrying the
   head and verdict. A forge review's `state`, or its author's name, is not
   enough — an unrelated human's `APPROVE` must never satisfy eligibility, and a
   bot account can be renamed.

   Read `pump19 adapt list-reviews "$PUMP19_OWNER" "$PUMP19_REPO_NAME" "$PUMP19_PR"`.
   For each review, take the last `Pump-19:`-prefixed line of its body and parse
   it with `pump19 marker parse`. Consider only reviews whose marker parses with
   `run=review` and `head=$PUMP19_HEAD_SHA` — these are the service's own
   verdicts for this head; every other review (no service marker, or an older
   head) is human commentary and does not count, whatever its `state`. Take the
   latest such verdict:
   - **`verdict=converged`** — the service's converging review. The head is
     **clean**; eligible.
   - **`verdict=standing-findings`** — a person's finish label overrides it: the
     head is **flagged**; eligible (it ships flagged, not clean).
   - **`verdict=partial-coverage`**, **`verdict=paused-flaky`**, or **no service
     verdict for this head** — not a state the finish label may override.
     `paused-flaky` is explicitly ineligible because the review withheld
     approval while `Flaky Tests` stood. `refused` with reason `not-eligible`:
     unreviewed, partially-reviewed, or paused code must not ride the finish
     label through a merge. The finish label is a standing instruction and
     stays sticky; a later finish fires once the loop produces an eligible
     verdict on the current head.

   Record clean vs flagged for the summary; both continue.
2. **Build.** `pump19 ws-exec --config "$PUMP19_CONFIG" -- sh -c "$PUMP19_BUILD_CMD"`.
   A non-zero exit is `refused` with reason `build-failed`.
3. **Test.** `pump19 ws-exec --config "$PUMP19_CONFIG" -- sh -c "$PUMP19_TEST_CMD"`.
   A non-zero exit is `refused` with reason `test-failed`.
4. **Maintenance.** Perform the occasion's configured maintenance (dependency
   bumps, documentation checks) over your general skills. If it changes the
   tree, it must be reviewed before it can merge: land it with
   `pump19 adapt commit-push …` (as the fix mission documents, attributed to
   `PUMP19_FIX_AUTHOR_NAME`/`PUMP19_FIX_AUTHOR_EMAIL` with the resolved lead
   model), then record `refused` with reason `review-pending`. The landed PR
   update re-fires review; the sticky finish label carries the next finish run,
   which merges the re-cleared tree. If maintenance changes nothing, continue.
5. **Current.** Require `pump19 run-guard … current`. `stale` is `refused` with
   reason `not-current`.
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

## Output contract

Record exactly one outcome as machine state — a single PR comment with a
trailing marker, a terminal `pump19/finish` status, and the release of
`Finishing`. The marker is:

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
  clears. Post the summary and set `pump19/finish failure` on
  `$PUMP19_HEAD_SHA`.

Format the marker with `pump19 marker format …` and post with
`pump19 adapt post-comment …`, the body ending in exactly that one marker line.
Any non-zero mechanical or forge command that this mission does not classify as
a refusal or a successful yield is a run failure: exit non-zero so the wrapper
writes `pump19/finish=error`. A release failure after a terminal status is
operational clean-up for the sweep, not a falsified result.
