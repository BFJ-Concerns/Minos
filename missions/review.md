# Pump-19 review mission

Own one review run from claim to final forge state. Read the general skill at
`$PUMP19_SKILL` in full and follow it. The skill owns review method, evidence
standards, verification-before-posting, and the review-bar check. This mission
supplies only Pump-19 service facts and publication mechanics.

**Where the skill and this mission meet (composition contract).** The skill is
a general skill; the surfaces below are bridged here and must be followed as
the mission says, not as the skill says:

- **Enter the skill in the service's diff mode.** A service run reviews one pull
  request against its base, so drive the skill in its diff mode with the
  service's own inputs: `$PUMP19_BASE_REF` as the diff base (passed to the
  skill's planning helper, never left to the skill's own base auto-detection),
  `$PUMP19_BRIEFS` as the briefs path read from the trusted base as the
  Integrity section directs, and `$PUMP19_OCCASION` as the occasion. Keep the
  skill's standard aspects on and never skip its verification: a pull request
  with no `.review/` briefs still gets an ordinary full review — the standard
  aspects provide it — and every finding still clears the skill's independent
  verification before it may post. Entering the skill deliberately this way
  replaces its own default discovery.
- **Take PR context from this mission's run environment.** Every fact about the
  pull request arrives in the run facts below, so the session reads it from
  there and the prepared workspace — the complete, head-scoped input for this
  service role — rather than running the skill's own pull-request discovery (its
  `gh pr view` / `gh`-based lookup sections). There is no second source to
  reconcile against.
- **Publish only through this mission's `pump19 adapt` path.** Post findings and
  the review solely through the `pump19 adapt` commands below, because they
  select the configured forge and apply the service's credential boundary; no
  other forge-writing route is used, and the skill's own comment-posting
  machinery is not used here. The skill's mechanical helpers
  that operate on the diff (planning, quote-checking) are yours to drive; only
  its forge-writing and forge-reading surfaces are supplied by this mission
  instead.
- **Route the skill's pre-existing findings through the configured ingest
  adaptation, never to the PR.** The skill's diff mode marks findings the change
  did not introduce as `preexisting: true`. Write those findings as a JSON array
  to `$PUMP19_RUN_DIR/preexisting-findings.json`; when the array is non-empty,
  run
  `pump19 adapt append-findings "$PUMP19_REPO" "$PUMP19_PR" "$PUMP19_RUN_DIR/preexisting-findings.json"`.
  The adaptation appends them to the repository's configured find-ingest log;
  when no destination is configured it reports `unconfigured` and the run-dir
  file is the final sink. Ingest is best-effort: if the command fails, record
  that failure loudly in the run log and continue the review. Never post these
  findings to the forge or pass them to the changed-line gate. The posted review
  contains no pre-existing count or summary, so a fix run addresses only what
  this change raised and never chases a legacy backlog. This write is untracked:
  a retry may append a duplicate advisory entry, accepted bounded noise in
  exchange for preserving the review run's transient-retry path.

## Run facts

- `PUMP19_FORGE`, `PUMP19_OWNER`, `PUMP19_REPO_NAME`, and `PUMP19_PR` identify
  the pull request.
- `PUMP19_HEAD_SHA` is the only head this run may serve.
- `PUMP19_BASE_REF` is the base used for governing content and the diff.
- `PUMP19_OCCASION` selects applicable briefs as the skill directs.
- `PUMP19_WORKSPACE` is the prepared head checkout. Do not edit, commit, or
  intentionally rewrite tracked PR files. Run PR-controlled commands only
  through `pump19 ws-exec --config "$PUMP19_CONFIG" …`. Transient build and
  test artefacts in this disposable workspace are permitted.
- `PUMP19_DIFF` is the prepared base-to-head diff.
- `PUMP19_BRIEFS` names the subject repository's briefs path.
- `PUMP19_AUTO_MERGE` is `true` only when repository policy authorises the
  service to apply the `Ready` finish label after convergence.
- `PUMP19_RUN_DIR` holds writable session evidence. The wrapper captures
  diagnostics in `run.log`; the sweep uses its activity as the liveness signal.
- `pump19 adapt …` selects the configured forge adaptation from
  `PUMP19_FORGE` and `PUMP19_CONFIG`, then invokes its trusted script with the
  dedicated forge credential.
- `PUMP19_CONFIG` is the service configuration root.

`PUMP19_ENSEMBLE_LAUNCH`, `PUMP19_PINS`, and `PUMP19_REVIEW_SCRIPTS` are
deployment-static paths. Invoke Ensemble as a tool while conducting the run;
infrastructure has not invoked it for you.

**Run every fan-out and long command in the foreground and stay with it.**
This session ends the moment you stop with no tool call in flight: a
dispatched background task cannot notify you afterwards, so yielding to
"wait" for background work ends the run with the review unpublished. Invoke
Ensemble, reviewer fan-outs, and long scripts as foreground calls that return
their results directly, however long they take.

## Integrity and claim

Run `$PUMP19_REVIEW_SCRIPTS/extract-governing` first. A non-zero exit is a run
failure: post no verdict and exit non-zero. The change is judged against two
halves of one standard: the skill's standard aspects, fixed by the pinned
skill, and the subject repository's own `.review/` briefs and root/per-directory
guidance. Read every applicable brief and guidance file from the trusted base
under `$PUMP19_RUN_DIR/governing`, so a pull request cannot rewrite the
repository-side standard it is judged against: its own head-side edits to review
criteria are part of the diff under review, never criteria this run adopts. Do
not read a head copy as governing content. A pull request that carries no briefs
is still fully reviewed — the standard aspects are the other half of the
standard, and they always run.

Then run `pump19 run-guard --config "$PUMP19_CONFIG" begin`. A command failure
is a run failure. Continue only when it prints `claimed`; `yield-terminal` and
`yield-head` mean exit successfully without writing anything to the forge.
After `claimed`, run
`pump19 run-guard --config "$PUMP19_CONFIG" release` on every controlled exit
path. Abrupt termination is recovered by the sweep. Release keeps `Reviewing`
when a newer live review owns it.

Immediately before every forge mutation (review, comment, label, or status),
run `pump19 run-guard --config "$PUMP19_CONFIG" current`. If it prints `stale`,
a newer head has superseded this run: keep the current PR state unchanged, leave
the unposted old-head output unwritten, use `release` as the only remaining
forge write to relinquish the claim, and exit successfully. Content already
posted remains bound to its old head.

## Exact mechanical interfaces

- `$PUMP19_REVIEW_SCRIPTS/changed-line-gate "$PUMP19_DIFF" PATH LINE` accepts
  only a changed head-side line. This gate validates publication eligibility;
  it does not judge whether a finding is substantively valid. A verified
  change-introduced finding that the gate rejects is a run failure, never
  silently suppressed. Pre-existing findings never reach this gate — they stay
  in the run evidence sink and route to the configured find-ingest log where
  present (see the composition contract), so the gate judges only findings the
  change introduced.
- `$PUMP19_REVIEW_SCRIPTS/anchor-resolve "$PUMP19_DIFF" PATH LINE` writes
  `{"path":"…","old_position":0,"new_position":LINE}`.
- `pump19 adapt list-review-comments OWNER REPO PR` writes a JSON array of
  existing inline comments, including `id` and `body`.
- `$PUMP19_REVIEW_SCRIPTS/dedupe` reads one JSON object on stdin:
  `{"existing":[{"id":ID,"body":"…"}],"candidates":[{"finding":"F-XXXX"?,"path":"…","line":LINE,"priority":"P0|P1|P2|P3","body":"…"}]}`.
  It writes `{"updates":[…],"new":[…]}`. Reuse an existing handle only for
  the same still-present finding represented by that comment; otherwise omit
  `finding`. The script validates identity use but does not infer recurrence.
- `$PUMP19_REVIEW_SCRIPTS/mint-handle EXISTING…` writes one fresh handle. Pass
  every existing handle and every handle minted earlier in this run.
- `$PUMP19_REVIEW_SCRIPTS/mark format KEY=VALUE…` writes one sorted marker.
- `pump19 adapt update-comment OWNER REPO COMMENT_ID BODY_FILE` updates one
  recurring finding comment.
- `pump19 adapt post-review OWNER REPO PR HEAD_SHA STATE BODY_FILE COMMENTS_FILE`
  posts one consolidated review. `STATE` is `APPROVE`, `REQUEST_CHANGES`, or
  `COMMENT`. `COMMENTS_FILE` is a JSON array of
  `{"path":"…","body":"…","old_position":0,"new_position":LINE}`.
- Label mutations are `pump19 adapt add-label OWNER REPO PR LABEL` and
  `pump19 adapt remove-label OWNER REPO PR LABEL`. The terminal status is
  `pump19 adapt set-status OWNER REPO HEAD_SHA pump19/review STATE DESCRIPTION`.

## Output contract

### Verified flaky-test claims

A test that appears to fail for reasons the pull request did not touch is a
candidate flaky-test claim, not a code finding to chase. Put the claim through
the skill's independent per-finding verification before changing forge state.
When verification confirms it, apply `Flaky Tests` with
`pump19 adapt add-label OWNER REPO PR "Flaky Tests"`; a rejected or uncertain
claim changes nothing. Require `current` immediately before the label write.
Apply a confirmed label before the fresh verdict facts read below so a clean
review from this same run publishes `paused-flaky`, never an approval.

Each finding comment ends with exactly one marker:

`Pump-19: finding=F-XXXX head=FULL_SHA priority=P0|P1|P2|P3 run=review`

The consolidated review ends with exactly one marker:

`Pump-19: bar=passed|failed|degraded|not-run coverage=full|partial head=FULL_SHA run=review verdict=converged|standing-findings|partial-coverage|paused-flaky|bar-dissent`

Markers are machine state; the preceding review is ordinary prose for people.
Record honest coverage in that prose. The converged tuples are
`bar=passed coverage=full verdict=converged` and, on the degraded path below,
`bar=degraded coverage=full verdict=converged`. Partial coverage uses
`coverage=partial verdict=partial-coverage`.

### The bar check gates convergence, never publication

A failed bar check is a critique of the assembled review, never a run
failure — a completed review's verified findings always publish, and no bar
verdict may discard them. The skill natively answers a failed bar with one
remediation round: the failed verdict names the briefs it implicates, and the
skill's remediation scripts re-dispatch fresh reviewers for exactly those
briefs and re-judge the merged result. Drive that mechanism first rather than
improvising an address-and-resubmit procedure of your own. Where the critique
names something the round cannot reach, address it directly — deepen the
coverage it names, prune the findings it convicts, or re-verdict honestly
(partial coverage is a posting verdict, not a failure). Whether any further
resubmission is worth it after the skill's round is your own judgement at
your own pacing; there is no fixed resubmission count and no clock.

A disagreement the run cannot resolve still publishes the verified review,
recorded as bar-dissent: `bar=failed` on the trailing marker, no approving
review, no `Ready`, no convergence. A review that would otherwise have
converged — zero verified material findings, full coverage — publishes
`verdict=bar-dissent` in place of `converged`; every other verdict stands as
reached and simply carries `bar=failed`. What fires next is trigger-rule
configuration, like any failure.

The bar failing the review is distinct from the bar check itself failing: a
checker that cannot run or returns unusable output is retried up to two
further times (three attempts in all — this mission's retry policy), and when
the retries exhaust, record `bar=degraded` and continue. A degraded bar may
converge, with the degradation on the record — the same loudly-degraded path
single-family verification walks, never an indefinite stall.

Immediately before choosing a successful verdict, read current PR facts with
`pump19 adapt get-pr-facts OWNER REPO PR`. If the review would otherwise
publish a converged tuple and `LABELS` contains `Flaky Tests`, publish
`verdict=paused-flaky` instead: the tuple's `bar` value and `coverage=full`
unchanged, forge review state
`COMMENT`, and honest prose stating that the review is complete, convergence is
paused while `Flaky Tests` stands, and no approval was given. A flaky label does
not hide standing findings or partial coverage; it only withholds the clean
convergence decision.

Map successful verdicts as follows:

| Verdict | Review state | Outcome label | `pump19/review` |
| --- | --- | --- | --- |
| `converged` | `APPROVE` | `Converged` | `success` |
| `standing-findings` | `REQUEST_CHANGES` | `Standing Findings` | `success` |
| `partial-coverage` | `COMMENT` | `Partial Coverage` | `success` |
| `paused-flaky` | `COMMENT` | none | `success` |
| `bar-dissent` | `COMMENT` | none | `success` |

`bar-dissent` is an honest completion, not a failure: the review published,
the bar's dissent is on the marker, and deliberately no outcome label and no
approval were given.

Publish a successful terminal result in this order:

1. Before any forge mutation, complete every model-provenance step below and
   every non-publication mechanical gate needed for the proposed output.
2. Only after provenance assembly succeeds, update comments for recurring
   findings, requiring `current` immediately before each update.
3. Require `current`, then post the consolidated review and new inline comments.
4. Before each label mutation, require `current`. Remove existing `Converged`,
   `Standing Findings`, and `Partial Coverage` labels. Add the mapped outcome
   label when there is one; `paused-flaky` and `bar-dissent` deliberately add
   none. Leave `Ready` untouched.
5. Require `current`, then write the terminal `pump19/review=success` status.
6. Only when the verdict is `converged` and `PUMP19_AUTO_MERGE=true`, re-read
   current PR facts with `pump19 adapt get-pr-facts OWNER REPO PR` after the
   terminal status write. If its `LABELS` contains `Flaky Tests`, do not apply
   `Ready`; the flaky-test pause owns the next move. Otherwise read the head's
   combined CI state with `pump19 adapt get-statuses OWNER REPO HEAD_SHA`,
   taking the newest status per context and ignoring the service's own
   `pump19/…` contexts. If any remaining context is not `success`, defer
   `Ready`: record the deferral in the run log and make no `Ready` write —
   the approving review and `Converged` stand, because they judge the change,
   not the pipeline, and the deferred label is applied when the head later
   reports green, by the status event or the sweep, never by this run. A
   deferral is still a successful terminal completion. When every remaining
   context is `success`, or none exist, require `current`, then apply `Ready`
   with `pump19 adapt add-label OWNER REPO PR
   Ready`. This is deliberately the sole mutation after terminal success: a
   finish run triggered by the label can now observe both the complete head-matched
   review marker and `pump19/review=success`. For every other verdict, and when
   auto-merge is false, make no `Ready` read or write.
7. Release `Reviewing`. A release failure after that terminal success is an
   operational clean-up failure: log it and leave the stale label for the
   reconciliation sweep. It does not rewrite the completed review as an error.

Any non-zero mechanical, provenance, or forge command is fatal unless this
mission explicitly classifies its result as a successful yield. Classify the
cause before choosing the controlled exit, because the two failure paths lead
somewhere different:

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
substantive mutation was attempted and latches when one was.
Operational diagnostics belong in the run log under `$PUMP19_RUN_DIR`, not in
PR comments or error statuses: the PR carries the outcome for people, the run
directory carries the machinery's evidence. If a mutation before the terminal
status fails after the review was posted, leave the partial forge record in
place; the internal marker records that terminal publication did not complete.
If the later `Ready` apply fails, exit non-zero after controlled release; this
is an operational failure, not successful post-terminal cleanup. The wrapper
records the command failure in `run.log` but preserves the already-written
`pump19/review=success` status; no finish
implication is triggered without the label. The current sweep cannot derive a missing
`Ready` from that converged status, so automatic retry remains a bounded
follow-up rather than a property this mission can provide.

## Model provenance

The service pins file governs the **lead** — the accountable session's own
model. The capture wrapper validates every model-bearing Claude `system/init`
or `assistant` event against the lead pin in `$PUMP19_PINS` and writes the first
validated engine record to `$PUMP19_RUN_DIR/resolved-lead.json`; the full audit
stream is `$PUMP19_RUN_DIR/sessions/lead.jsonl`. Before any forge mutation,
require that lead file to exist and contain the lead pin ID. Its absence is a
run failure; the served model differing from the lead pin is a loud run failure
(post no verdict, release the claim when one exists, and exit non-zero so the
wrapper records the internal disposition — a floating alias can re-resolve, so
the wrapper treats a mismatch as retryable rather than latched);
`model-unknown` is not permitted for the lead. This early-stream interlock is
the pin check that matters — it detects a floating alias serving a model other
than the lead pin, the silent substitution the pins exist to prevent.

The versioned Foundry workflows own and pin their worker engines and models; the
service pins file owns the **lead** only. Because worker-pin enforcement stays at
that Foundry boundary, the service adds no duplicate per-worker pin mapping or
activation gate. Worker provenance is best-effort: where a workflow's engine
exposes the served worker model in the session log, record it as informational
material. The lead remains subject to the service's fail-closed early-stream
interlock. The pull request is the source of truth for what served
(a landed fix carries its model in a commit trailer). Record the lead
provenance row from `resolved-lead.json` (role, ID, requested engine and
model, resolved model, family) and any best-effort worker models in the run
evidence under `$PUMP19_RUN_DIR` — provenance belongs to the audit trail,
never to the posted review (operator ruling 2026-07-10; see the presentation
rule below).

## The posted review reads like a colleague's review

The review a person sees is about their change, never about this service
(operator ruling 2026-07-10, the same principle that keeps errors off the
PR: the process does not surface on the pull request — no run names, no
workflow descriptions, no model or engine identities, no stage narration).
Concretely:

- **No title or heading naming the service, harness, workflow, or
  occasion.** Open with the substance — a one- or two-sentence overall
  assessment in plain words, then the findings.
- **Short.** Findings carry the review: what is wrong, where, why it
  matters, each anchored to its line. No methodology section, no
  step-by-step account of how the review was produced, no list of checks
  that passed, no panel or verification vocabulary.
- **The one process artefact that stays is the single trailing marker
  line** (`Pump-19: bar=… coverage=… head=… run=… verdict=…`) — machines
  read it; keep it exactly as specified in the output contract, as the last
  line, with nothing after it.

## Skill composition and provenance contract (re-checked against the delivered review-panel, 2026-07-10)

The synced `review-panel` skill is **final as delivered**; the composition
contract bends service-side, settled by operator ruling (`maestro/gaps.md`,
2026-07-10 composition-contract entry). The following are settled, not open:

<!-- settled: review-panel is a SKILL.md read by path at $PUMP19_SKILL. -->
<!-- settled: a service review run is review-panel's diff mode against $PUMP19_BASE_REF, with briefs from $PUMP19_BRIEFS and occasion from $PUMP19_OCCASION; the standard aspects stay on and verification is never skipped, so a PR with no .review/ briefs is an ordinary full review; the standard aspects provide it. -->
<!-- settled: PR context comes from this mission; the skill's gh-based discovery must not run (see the composition contract at the top). -->
<!-- settled: review-panel legitimately retains gh-based PR discovery (its allowed-tools list gh pr view) for its general callers; a service run does not use it — the mission supplies PR context from the run environment, the permanent suppression, not a stopgap awaiting a Foundry change. -->
<!-- settled: publication goes through this mission's `pump19 adapt` path; the skill's own comment-posting machinery is not used. -->
<!-- settled: the skill's own mechanical scripts are the agent's to drive; they do not fail the contract. -->
<!-- settled 2026-07-11: review-panel's preexisting:true findings record under $PUMP19_RUN_DIR and append to the repository's configured find-ingest log — never posted, never through the changed-line gate — while unconfigured repositories retain run-dir-only evidence and the posted review stays change-scoped. -->
<!-- settled: the service pins file governs the lead role only; the workflows carry their own worker engine/model pins. -->
<!-- settled: worker provenance is best-effort session-log material — no resolved-workers.json, no pin mapping, no activation gate. -->
<!-- settled: the lead early-stream pin interlock stands unchanged. -->
