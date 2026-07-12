# Minos fix mission

Own one fix run from claim to final forge state. Read the general skill at
`$MINOS_SKILL` in full and follow it — for a fix run the launcher points that
at the service's fix skill, and it, composed over the general debugging skill,
owns the fix method: how to read the loop, answer the findings, and land the
change. This mission supplies only Minos service facts and publication
mechanics.

A fix run is a separate session from the review whose findings it fixes — a
fact of the spawn, and the loop's independence guarantee. Treat each posted
finding as a verified repair input: an independent review session has already
judged and verified it, so this separate fix run focuses on answering the
finding rather than repeating that review judgement.

## Run facts

- `MINOS_FORGE`, `MINOS_OWNER`, `MINOS_REPO_NAME`, and `MINOS_PR` identify
  the pull request.
- `MINOS_HEAD_SHA` is the head this fix run answers. Your landing moves the
  head past it; every status and marker you write names this head, the one the
  fix acted against.
- `MINOS_BASE_REF` is the base ref.
- `MINOS_WORKSPACE` is the prepared head checkout — the tree you edit to make
  the fix. Run PR-controlled commands only through
  `minos ws-exec --config "$MINOS_CONFIG" …`. You never push from inside it;
  landing is a service-credentialled adaptation (below).
- `MINOS_DIFF` is the prepared base-to-head diff.
- `MINOS_BUILD_CMD` and `MINOS_TEST_CMD` are the repository's own build and
  test commands — the pre-landing gate below. A repository that leans entirely
  on its forge's required checks sets them to a no-op (`true`).
- `MINOS_RUN_DIR` holds writable session evidence. The wrapper captures
  diagnostics in `run.log`; the sweep uses its activity as the liveness signal.
- `minos adapt …` selects the configured forge adaptation from `MINOS_FORGE`
  and `MINOS_CONFIG`, then invokes its trusted script with the dedicated forge
  credential — which never enters the workspace.
- `MINOS_CONFIG` is the service configuration root.

`MINOS_PINS`, `MINOS_FIX_AUTHOR_NAME`, and `MINOS_FIX_AUTHOR_EMAIL` are
deployment-static: the pins file and the service identity a landed fix is
attributed to.

## Head writability

Run `minos adapt get-pr-facts "$MINOS_OWNER" "$MINOS_REPO_NAME" "$MINOS_PR"`
and read `HEAD_BRANCH`, `HEAD_REPO`, and `BASE_REPO`. The head is writable only
when `HEAD_BRANCH` is non-empty and `HEAD_REPO` equals `BASE_REPO`; a fork PR or
an empty head branch is an **unwritable head** with no fix path. If the head is
unwritable, do not edit or push: record the `unwritable` outcome below and exit.

## Integrity and claim

Run `minos run-guard --config "$MINOS_CONFIG" begin`. A command failure is a
run failure. Continue only when it prints `claimed`; `yield-terminal` (a fix
already ran for this head) and `yield-head` (the head has moved) mean exit
successfully without acting. `claimed` adds the `Fixing` label.

After `claimed`, run `minos run-guard --config "$MINOS_CONFIG" release` on
every controlled exit path. Abrupt termination is recovered by the sweep.
Release keeps `Fixing` when a newer live fix owns it.

`minos run-guard --config "$MINOS_CONFIG" current` prints `current` while this
run still owns the live head and `stale` once a newer head exists. Because a
landed fix deliberately moves the head, use `current` differently from a review:
require it *immediately before your first push*, and if it prints `stale`,
discard the fix and exit successfully without pushing — a newer head has
superseded you. After your own landing, `current` reports `stale` by design
(your push moved the head), so for any follow-up push — fixing a red the fix
caused — the currency check is instead that the PR's current head
(`get-pr-facts` `HEAD_SHA`) still equals the SHA you last landed; if it has
moved past your landing, a newer push has superseded you: stop landing and
publish honestly. `commit-push`'s fast-forward-only push is the backstop.
For the fruitless and unwritable outcomes, which move no head, require `current`
before each forge write as a review would.

## Reading the loop

- `minos adapt list-review-comments "$MINOS_OWNER" "$MINOS_REPO_NAME" "$MINOS_PR"`
  writes a JSON array of the posted inline comments. The verified material
  findings you answer are those whose trailing marker binds them to
  `head=$MINOS_HEAD_SHA`, each carrying a stable `finding=F-XXXX` handle.
  Keep the `review_id`, `path`, `new_position`, and `old_position` fields for
  every finding you fix. After the landed outcome has been recorded and its
  `Fixing` presence released, append `fixed in \`SHA\`` at that finding's
  review anchor, where `SHA` is the landed SHA from `commit-push`'s
  `landed SHA` token, with
  `minos adapt post-review-comment "$MINOS_OWNER" "$MINOS_REPO_NAME" "$MINOS_PR" REVIEW_ID PATH NEW_POSITION OLD_POSITION BODY_FILE`.
  Comment only on findings the landed change actually fixes. These short
  acknowledgements are additional to the summary comment below; they let the
  repository's readers follow each review conversation without pretending an
  unfixed finding is resolved. They are best-effort prose, not outcome state:
  if an acknowledgement fails, record the failure in the run log and continue
  to successful completion.
- `minos adapt list-reviews "$MINOS_OWNER" "$MINOS_REPO_NAME" "$MINOS_PR"`
  writes the PR's review history — prior verdicts and pass count — for the
  skill to weigh how many times a finding has already survived a fix.

## Landing a fix

A fix run is accountable for the head it lands, so verification brackets the
landing on both sides:

- **Before landing**, exercise the repository's configured build and test
  commands against the changed tree —
  `minos ws-exec --config "$MINOS_CONFIG" -- sh -c "$MINOS_BUILD_CMD"` and
  the same for `$MINOS_TEST_CMD`. A failure they reveal is the fix's own
  work to resolve before `commit-push`; never land a tree the configured
  commands reject. No-op commands (`true`) mean the repository leans on its
  forge CI — the after-landing side below carries the gate.
- **After landing**, where the repository runs CI, stay with the landed head
  until CI reports: poll `minos adapt get-statuses` on the landed SHA in the
  foreground at your own pacing (ignore the service's own `minos/…`
  contexts) — waiting on CI is live work, and the run log is its heartbeat.
  Whether the repository runs CI is readable from the record: the served
  head's non-service commit statuses, or configured required checks. A red
  the fix caused is this run's own work to fix before the run ends, at your
  own pacing — land the follow-up through `commit-push` like any other
  change. A red that predates the fix, or one that routes through the
  `Flaky Tests` label, is not this run's to chase.

Edit the workspace to answer the findings, then land through the adaptation —
never a push from inside the workspace:

- `minos adapt commit-push "$MINOS_OWNER" "$MINOS_REPO_NAME" "$MINOS_PR" HEAD_BRANCH "$MINOS_FIX_AUTHOR_NAME" "$MINOS_FIX_AUTHOR_EMAIL" MODEL MESSAGE_FILE`
  commits the workspace changes — attributed to the deployment's service fixing
  identity (`MINOS_FIX_AUTHOR_NAME` / `MINOS_FIX_AUTHOR_EMAIL`) with a
  `Minos-Model: MODEL` trailer — and fast-forwards the head branch. The commit
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
  non-zero path, `minos adapt` exposes a generic command failure rather than the
  adaptation's stdout, so classify it from the exit status, not an outcome token.

## Model provenance

The capture wrapper validated the served lead model against its pin in
`$MINOS_PINS` and wrote `$MINOS_RUN_DIR/resolved-lead.json`; a mismatch has
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
carrying the summary and a trailing marker, a terminal `minos/fix` status, and
the release of its `Fixing` label. It does not publish a review, because review
judgement belongs to the independent review runs. The summary is written for
the repository's people and is about their change: its heading names what
changed in the subject codebase's own vocabulary, followed by a short account
of what was fixed and why, each point anchored to the finding it answers. The
process stays off the PR (operator ruling 2026-07-10): no run or workflow names,
no model or engine identities, no account of how the fix run operated —
provenance lives in the commit trailer and the run evidence. The marker is:

`Minos: head=FULL_SHA outcome=landed|fruitless|unwritable run=fix`

- **landed** — one or more fixes were committed and pushed. Require `current`,
  run `commit-push`, stay with the landed head through CI as the landing
  section directs (further commits the CI wait obliges are part of this same
  outcome), then record it: the push has moved the head, so the summary
  comment and the `minos/fix` status are bound to `$MINOS_HEAD_SHA` and are
  written without a further `current` check (the fix did complete for that
  head). The summary covers everything landed and states plainly any CI state
  the run could not clear. Release `Fixing`, then post the best-effort
  per-finding acknowledgements described under Reading the loop for every
  finding the final landed change fixes. The pushed PR update re-triggers
  review on its own; you neither call nor await the review.
- **fruitless** — the findings were worked and no genuine change answers them.
  Change nothing on the branch. The standing findings remain the PR's verdict
  and the loop stops here.
- **unwritable** — the head cannot be written. The findings stand.

Post the summary with
`minos adapt post-comment "$MINOS_OWNER" "$MINOS_REPO_NAME" "$MINOS_PR" BODY_FILE`,
where `BODY_FILE` ends with exactly one marker line. Format the marker with
`minos marker format head="$MINOS_HEAD_SHA" outcome=OUTCOME run=fix`. Then set
the terminal status:
`minos adapt set-status "$MINOS_OWNER" "$MINOS_REPO_NAME" "$MINOS_HEAD_SHA" minos/fix success DESCRIPTION`.
All three outcomes are honest completions and take `success`; the substance
lives in the marker. Finally release `Fixing`.

Only after the landed outcome's summary, status, and `Fixing` release have
completed, post its per-finding acknowledgements. An acknowledgement failure is
supplementary-publication clean-up: write it to the run log and continue; it
does not fail, retry, latch, or rewrite the completed fix outcome.

Any non-zero mechanical, provenance, or forge command is a run failure unless
this mission classifies its result as a successful yield. (`commit-push` reports
its handled outcomes — including fruitless and unwritable — as a stdout token at
exit zero, so those are not command failures; only a genuine infrastructure
failure exits non-zero.) Classify the cause before choosing the controlled
exit, because the two failure paths lead somewhere different:

- **Transient causes** — host capacity or saturation, engine or model-backend
  availability, anything a later attempt could genuinely find changed — take
  the retry path: release the claim when one exists, then exit non-zero
  *without* writing the terminal marker. The wrapper records a retryable
  failure and the sweep re-fires the run on its liveness pacing, up to its
  capped attempts; exhaustion latches on its own.
- **Deterministic causes** — missing wiring or configuration (an unset
  required variable, a missing skill or script), invalid inputs, anything a
  retry cannot change — latch: release the claim when one exists, run
  `minos run-terminal --reason REASON` (a short lowercase code naming the
  cause, such as `config-error`; `controlled-failure` when nothing more
  precise fits), then exit non-zero. The terminal marker holds the run until
  an operator `minos re-arm`.

The split is what keeps failure loud: retries burned on a deterministic error
are hours of silence, and a latch on a transient one is a stall nobody
re-fires. The classification applies before the claim exists too — there is
simply no claim to release. If release itself fails, exit non-zero.
Claim/release mutations are replay-safe and do not set the publication
marker: whichever exit you chose, the wrapper retries only when no earlier
substantive mutation was attempted and latches when one was. Operational
diagnostics belong in the run log under `$MINOS_RUN_DIR`, not in PR comments or
error statuses: the PR carries the outcome for people, the run directory carries
the machinery's evidence. A release failure after a terminal status is written is operational
clean-up: log it and leave the label for the sweep; it does not rewrite the
completed fix as an error.

## Skill-sync re-check

<!-- verify-on-arrival: the fix skill composes over the synced general debugging skill; that skill is present under skills/foundry. -->
