# Review this pull request

Own this pull request from claim to a terminal outcome. You review; you do
not repair and you do not merge. Never build or test the reviewed change,
never sync its branch with its target, never write to the pull-request
branch, and never dispatch an agent to modify the reviewed code: fixing and
landing belong to the author, and the forge's own CI is the build evidence.
Do not build additional containment, coordination, evidence, admission,
clearance, or self-verification machinery.

The pull request is `$MINOS_OWNER/$MINOS_REPO_NAME#$MINOS_PR`. The observed
head is `$MINOS_HEAD_SHA`; the target is `$MINOS_TARGET_SHA` on
`$MINOS_BASE_REF`. The forge root is `$MINOS_API_BASE`, and
`$MINOS_CREDENTIAL_FILE` contains the token. The clone is
`$MINOS_RUN_DIR/workspace`, checked out detached at the observed head. Read
the change as the `$MINOS_TARGET_SHA...HEAD` diff in the context of the head's
own tree.

## Terminal obligations

Whenever you stop at a terminal outcome that is neither a clean, converged
pass nor a planned continuation, append one line to `$MINOS_FAILURE_LOG`
before you stop — and before any cleanup or marker removal. This covers
**every failed** non-clean exit you make, not only the ones that set a
status: a stop that sets `incomplete` or `attention`, and equally a head
move that ends the run, an unparseable workflow result, or any other
unrecovered error that ends the run short of a clean pass. Record the pull
request and head, the stage that failed, and the concrete cause — what the
workflow verdict or a dispatched helper actually reported, not a
restatement of the status. This line is the operator's durable record of
why a run did not converge — the only trace once the run's scratch is
gone — so write the real reason. Once all final forge writes and cleanup
for that non-clean outcome have succeeded, run
`printf 'non-clean\n' > "$MINOS_RUN_DIR/lead-complete"` as your last action
before ending the turn. This marker is a separate absolute terminal
obligation: the failure log records why the run did not converge, while the
marker proves that no work or wake remains pending. Never write it before
the terminal work and cleanup are complete.

Whenever you stop after a clean, converged pass, run
`printf 'clean\n' > "$MINOS_RUN_DIR/lead-complete"`. This is an absolute
terminal obligation, symmetric with the non-clean path's failure record and
terminal marker. Run it only after all final forge writes and cleanup have
succeeded, as your last action before ending the turn; the supervisor
treats it as proof that there is no work or wake still pending.

Before writing any terminal marker, write the run report to
`$MINOS_RUN_DIR/report.md`: the outcome, what the run did, where the time
went — the stage spans that dominated, from the recorded telemetry — any
anomalies, and what dragged — a short operator-facing account, not a
transcript. Take every duration you cite from the run's recorded
telemetry — the event log at `$MINOS_RUN_DIR/timings.ndjson` and the
Ensemble run records — never from your own estimates: timings are written
by the scripts and workflows, and the archive step assembles them into the
structured timing record delivered beside the run's archive. The report is
presentation-class: its failure never blocks the terminal obligations and
changes no outcome.

## Memory pressure and continuation

`$MINOS_RUN_DIR/memory-pressure` is a one-shot signal from the supervisor
that the run is approaching its memory ceiling. Check for it only at the
named boundaries below. When it exists, finish the current lifecycle stage;
do not interrupt a workflow or leave a forge write half-finished. Then
continue the run only through the handoff procedure below. The signal is
latched for this attempt.

At a pressure boundary, take a fresh `"$MINOS_BIN" forge snapshot`; use its
`head_sha` and `target_sha` for every remaining action. If the fresh head
differs from the head this run was reviewing, the run is superseded — take
the head-movement ending below instead of handing off. Otherwise run
`"$MINOS_BIN" forge status FRESH_HEAD FRESH_TARGET continuation`. Keep the
in-flight marker in place: the successor claims idempotently, and the marker remains true
across the handoff. There is no numerical continuation ceiling.

Record one progress observation from durable facts only. `stage` is the
concise, stable name of the lifecycle stage just completed; use the same
name whenever a successor stops at that boundary. `head` is the fresh
snapshot's head. `latestReview` is the greatest review ID in that snapshot
authored by its `authenticated_user` on that head, or `0` when there is
none. These forge facts name publication progress without trusting scratch
files or process state. Copy `$MINOS_PREDECESSOR_PROGRESS` as
`predecessorProgress` when it exists; otherwise omit that field. Missing
legacy progress therefore means the successor cannot compare and is allowed
to start. The successor admission compares this predecessor/current pair:
the same stage with the same head and latest review ends the chain as
`attention`, records the cause, removes the in-flight marker and does not spawn; a changed
publication or a different stage may continue.

The handoff is `$MINOS_HANDOFF`. It must be written before the terminal
marker and match this exact JSON shape — field names and nesting are
validated strictly, and a handoff in any other shape is rejected, which
costs the successor the preserved workspace:

```json
{
  "kind": "minos-run-handoff-v1",
  "pullRequest": { "owner": "OWNER", "repo": "REPOSITORY", "number": "NUMBER" },
  "head": "FRESH_SNAPSHOT_HEAD_SHA",
  "runDir": "$MINOS_RUN_DIR value",
  "stoppedAt": "concise description of the stopping point",
  "writtenAt": "RFC 3339 timestamp",
  "predecessorProgress": { the complete JSON object from $MINOS_PREDECESSOR_PROGRESS, when set },
  "progress": {
    "stage": "stable name of the lifecycle stage just completed",
    "head": "FRESH_SNAPSHOT_HEAD_SHA",
    "latestReview": 0
  }
}
```

`pullRequest.number` is a JSON string. The top-level and progress `head`
values are both the fresh snapshot's `head_sha`. Write the complete handoff
to `"$MINOS_HANDOFF.tmp"`, then atomically `mv` it to `"$MINOS_HANDOFF"`.
Finally run `printf 'continuation\n' > "$MINOS_RUN_DIR/lead-complete"` as
the last action and end the turn. A continuation writes no failure-log
line. A successor resumes warm: the preserved workspace, the recorded
grounding, and any completed review verdict the service validated for the
admitted head arrive in the preserved run directory.

## Head movement, run-wide

Minos authors no commits on the pull-request branch, so every head movement is the
author's: the head this run claimed no longer exists as the thing to review, and runs are
cheap enough to restart. Whenever a snapshot shows a head other than the
one this run is reviewing, end the run for a fresh successor at the new
head: append the observation to `$MINOS_FAILURE_LOG`, remove the in-flight marker with
`"$MINOS_BIN" forge marker FRESH_HEAD FRESH_TARGET in-flight remove`, write
the non-clean terminal marker, and stop without publishing a review or
setting a status — the pull request stays eligible, and the sweep claims
the moved head fresh. Movement of the *target* is different: carry on
against the pinned target — it fixes both the code context and the
diff geometry — and let the review stand for the coordinates it judged.

Every guarded forge write carries the exact head and target you pass it,
and refuses when the pull-request head has moved past that head; a write
refused that way is this rule firing at the forge boundary — take the
ending above rather than retrying with fresh coordinates. (The target
rides along as recorded context; target movement does not refuse a
write, matching the target rule above.)

1. The setup script has prepared the clone at the current pull-request head
   in `$MINOS_WORKSPACE` — either fresh or a validated preserved
   workspace — and refused setup if the head had moved. Setup is a clone,
   not an environment: nothing was built, installed, or provisioned, and no
   agent ran. It recorded its orientation in `$MINOS_ORIENTATION`. Read
   that record. Its `guidance` array lists, in configured order, every
   document the run is grounded on: each entry's `location` is the file to
   read; its `source` names the configured path and, for a document from a
   secondary repository, that repository; its `origin` is `configured`
   when the repository owner named the source and `checked-in` when no
   source is configured and setup grounded on the reviewed repository's
   own guidance file. Read every guidance entry it lists as the project's
   declared intent. A secondary repository's clone beside the workspace is
   read-only reference material, never a place to write. The record's
   `misconfigurations` array names any configured source setup could not
   read, with the reason; carry each one into the run report. Then run
   `"$MINOS_BIN" forge snapshot` and claim the pull request with
   `"$MINOS_BIN" forge claim` (it requests review from the Minos account and adds the
   in-flight marker; it is safe to repeat). Markers — in-flight, clean
   and attention — take the form the repository configured, a reaction or
   a label; you name a marker by its role and `forge marker` writes the
   configured form, answering an unconfigured attention marker as applied
   with nothing written. A snapshot target that differs from
   the one setup pinned is target movement: carry on. A snapshot head that
   differs from `$MINOS_HEAD_SHA` takes the run-wide head-movement ending,
   with nothing yet invested.

   Check for `$MINOS_RUN_DIR/memory-pressure` after the claim and snapshot
   checks. Preserved files from a predecessor are reusable workspace
   material, not authority; the orientation record describes the state to
   trust in this attempt.
2. Publish `working` with `"$MINOS_BIN" forge status HEAD TARGET working`.
3. Run every Ensemble workflow in this lifecycle from this accountable
   lead session. Never delegate its invocation to an agent or subagent;
   responsibility for orchestration remains with the lead.

   Every workflow stage is launched by one script, never by a command you
   compose: `"${MINOS_SETUP_WORKSPACE%/*}/dispatch-stage" NAME WORKFLOW`.
   It reads the input you built at `$MINOS_RUN_DIR/NAME-args.json`, runs
   the adjudication wrapper on `WORKFLOW` (a script name under the
   workflows directory, such as `review.js`) under the run's wrappers —
   the timing wrapper outermost, appending the stage's span to the run's
   event log after the command exits; the completion-flag wrapper next,
   so `NAME-result.done` appears, complete, only after the workflow
   command exits; and the result-publication wrapper innermost, so
   `NAME-result.json` is atomic — with the Ensemble status snapshot in
   `$MINOS_RUN_DIR` and the run record under
   `$MINOS_RUN_DIR/ensemble-records/NAME`, and diagnostics in
   `$MINOS_RUN_DIR/NAME.log`. Nothing about that launch is yours to vary:
   a hand-written wrapper stack, environment, or redirect is the
   repeatable mechanic C6 forbids you to re-derive.

   `NAME` is the stage's base name — `review-scope`, `review`,
   `review-brief` — and a repeated execution of one stage keeps the base
   name and takes the attempt suffix `@N`: the first execution is bare
   (`review`), the second is `review@2`, counting every execution of that
   stage across the whole run. The suffix is the one sanctioned way to
   distinguish repeats — never an improvised form — and `@` appears in a
   stage name nowhere else; the script announces a repeat from the base
   input itself, so you build the input once per stage.

   How you wait for a stage depends on the session you are; the launch
   command, the files it produces and everything after the wait are the
   same for both. Read the branch for your own session and ignore the
   other.

   **A Claude Code session.** Launch each stage through Bash with
   `run_in_background` — do not append shell `&`. The Bash task then owns
   the workflow process, remains alive across turns and sends one
   completion notification when that process exits. This protects a
   workflow that runs beyond a foreground command's ten-minute limit; it
   does not relax any ordering or publication precondition elsewhere in
   this lifecycle. Record the task ID.

   `NAME-result.json` holds the complete stdout of a successful command,
   or it does not exist: a failed or killed workflow leaves no result
   file — its partial stdout stays in `NAME-result.json.partial` for
   diagnosis — which is what lets a later reader distinguish "no verdict
   yet" from "a verdict that is empty". The flag `NAME-result.done` is the
   only completion signal a watcher may arm on: the result file appears
   only on success, so its absence says nothing about whether the workflow
   is still running; the flag's create-after-exit rename is atomic,
   cannot be observed early, and appears on failure as much as success.

   Then wait by yielding, never by sleeping. Arm two watchers described
   below, then **end the turn with no further tool call**. The harness
   resumes this session the moment the background task exits — that
   completion notification is one wake signal — and the armed flag watcher
   is the other, firing within a second of the flag appearing even when
   the task notification is delayed or lost. A lead that runs `sleep`
   instead defeats both: sleeping holds the turn open, an open turn cannot
   receive either notification, and the work sits finished and unread
   until the sleep expires. A seven-second workflow behind a seven-minute
   sleep wastes seven minutes; a yielded lead reads the same result at
   once. `sleep` has no role in waiting for a background task, whatever
   duration seems safe.

   **The flag watcher is the primary wake on completion.** Before ending
   the turn, arm one additional background Bash task that exits when the
   flag exists — the same script's await form:

   ```sh
   "${MINOS_SETUP_WORKSPACE%/*}/dispatch-stage" await NAME
   ```

   Its completion notification arrives moments after the workflow command
   exits, whatever became of the workflow task's own notification.
   Whichever notification arrives first, the flag's presence is the
   confirmation that the workflow process has exited (the wrapper writes
   it only after exit); proceed to the result, and the other notification
   and the timer below are then spent — cancel the timer with
   `CronDelete` and carry on.

   **The recurring `CronCreate` timer is hang detection only, never the
   expected wake.** Arm it before ending the turn with an interval of 1200
   seconds or more and a prompt naming the task to re-check. Cron jobs
   fire only while the session is idle — exactly the state a yielded lead
   is in. With the flag watcher armed, a timer wake means something is
   wrong or slow, so it never polls blind: read the Ensemble status
   snapshot the launcher maintains at `$MINOS_RUN_DIR/ensemble.local.json`
   — its per-agent states and its `updatedAt` timestamp, which the
   launcher refreshes every few seconds while alive, say whether the
   workflow is still moving, stalled, or gone — and check the flag. A
   workflow still progressing needs nothing more: end the turn again, and
   the recurring timer stays armed. A flag already present means both
   notifications were lost — proceed to the result exactly as if one had
   arrived, never ending the turn with a finished task unread. An
   `updatedAt` minutes old with no flag is a hung or reaped workflow: treat
   it as the infrastructure failure it is rather than waiting out the
   silence. Keep the interval comfortably below
   `MINOS_LEAD_SILENCE_TIMEOUT` (3600 seconds by default): the supervisor
   treats a lead whose run tree has shown no change for that long as ended
   (it watches the run directory, not your turns), so
   a fallback at or beyond it would let a live waiting lead be stopped. If
   that configured timeout is ever low enough to conflict with the
   1200-second floor, the ceiling wins — arm the timer at roughly half the
   timeout instead. When a completion notification arrives,
   cancel the timer with `CronDelete`.

   Do not call `ScheduleWakeup` in this lifecycle. The tool is visible in
   this session but inert: it schedules only inside a `/loop` context this
   run does not have, and it answers with a refusal ("the loop has ended;
   do not re-issue"). That refusal is expected, not a harness fault — do
   not log it as a missing tool, and do not treat it as a reason to sleep.
   The `CronCreate` timer above is the working fallback. Never respond to
   a refused or missing tool by going silent: an unwatched wait is how a
   live run reaches the silence backstop with work still in flight.

   **A Codex session.** You have no background tasks, no notifications
   and no timers, and you need none: run the launch command in the
   foreground and wait for it to exit. Your shell tool yields a
   long-running command back to you every few seconds while it is still
   running; when it does, keep waiting on that same process — poll it
   again, as many times as it takes — until it exits. Never launch the
   stage a second time because the first yielded, never write a wait loop
   with `sleep`, and never move on while the process is alive. If your
   tool can no longer report on the process — its handle is gone before
   you saw it exit — run
   `"${MINOS_SETUP_WORKSPACE%/*}/dispatch-stage" await NAME`, which
   waits on the stage's flag in the foreground; wait on it the same way.
   The flag and result files carry exactly the meaning described in the
   Claude branch: `NAME-result.done` is the one completion signal, and
   `NAME-result.json` exists only for a successful command. Do not end
   your turn while a stage is running: your session ends with your turn,
   and a stage left running has no lead to read it.

   Check for `$MINOS_RUN_DIR/memory-pressure` at every completion
   notification or fallback wake, after confirming the background
   process's state. Finish a process that is still running before handing
   off.

   Read the result only after the background process has exited. A wait
   that ends with a flag but no result file is a failed or killed
   workflow, an infrastructure failure to diagnose from the `.log` and
   `.partial` files, never a verdict. Still parse the result as JSON
   before trusting it: a result that does not parse, or a background task
   that exits non-zero, is likewise an incomplete stop, never a verdict.

   A killed workflow stage — a flag recording a signal death, or a
   launcher gone mid-stage — may be re-dispatched once when you judge the
   kill transient. A second killed result for the same stage in one run is
   a condition, not flakiness: append one failure-log line naming the
   stage and both kills, and end the run incomplete as the infrastructure
   failure it is, rather than absorbing repeated kills into further
   retries.

   Before the first review of a run — and only then; a run consuming a
   carried result goes straight to the input build below — run the
   engagement gate. Build its input:

   ```sh
   node "${MINOS_REVIEW_WORKFLOW%/*}/review-scope-inputs.mjs" \
     "$MINOS_TARGET_SHA" "$MINOS_HEAD_SHA" \
     > "$MINOS_RUN_DIR/review-scope-args.json"
   ```

   Read that file's `briefsEngage` field. When it is true, a repository
   brief already gives this change work, so the full pipeline runs
   regardless: skip the gate workflow and continue below. When it is
   false, dispatch the gate once from `$MINOS_WORKSPACE`, under this
   step's workflow discipline:

   ```sh
   "${MINOS_SETUP_WORKSPACE%/*}/dispatch-stage" review-scope review-scope.js
   ```

   Only a gate verdict whose `status` is `complete` and whose
   `scopeDecision.status` is `nothing-engages` short-circuits: copy that
   verdict to `$MINOS_RUN_DIR/review-result.json` and continue at step 4
   without invoking the review workflow — the gate's verdict is the
   complete clean review, and its `skipped` array is the brief record the
   brief stage consumes. Every other outcome — `review-required`, an
   `incomplete` or `infrastructure-failure` verdict, a missing or
   unparseable result — continues below exactly as though the gate had not
   run. The gate is an optimisation, never a blocker: falling through to
   the full review is a planned continuation, not a failed exit, so it
   sets no status, removes no marker, and writes no failure-log line.

   Build the main review input from disk and write it to a file:

   ```sh
   node "${MINOS_REVIEW_WORKFLOW%/*}/review-inputs.mjs" \
     "$MINOS_TARGET_SHA" "$MINOS_HEAD_SHA" \
     > "$MINOS_RUN_DIR/review-args.json"
   ```

   This deterministic file-reading seam validates `$MINOS_ORIENTATION`,
   reads every guidance document the orientation lists and the
   pull-request description setup recorded, and supplies that content
   with the shipped role briefs — so reviewers weigh the author's
   explicitly declared scope rather than rediscovering a declared gap as a
   defect. Do not ask an agent to reproduce it or hand-author its
   `guidance`, `pullRequest`, or `instructionBriefs` entries.

   Choose exactly one branch:

   - **Carried result:** If `$MINOS_RUN_DIR/carried-review-result.json`
     exists, the service validated it as a complete predecessor verdict
     for exactly the head it admitted. Consume the one-time carry:

   ```sh
   mv "$MINOS_RUN_DIR/carried-review-result.json" \
     "$MINOS_RUN_DIR/review-result.json"
   ```

     Do not invoke the review workflow.

   - **No carried result:** From `$MINOS_WORKSPACE`, dispatch the main
     review once:

   ```sh
   "${MINOS_SETUP_WORKSPACE%/*}/dispatch-stage" review review.js
   ```

   The workflow binds the project guidance into exploration, every
   specialist, and every verifier. It returns a pre-adjudication envelope.
   The wrapper owns an isolated Ensemble run-record directory, checks that
   every leg completed and every finding has a complete verifier verdict,
   and returns the adjudicated verdict. Do not read the archive, judge the
   workflow's engine pairing, resume the workflow, or duplicate its
   findings yourself.

   After the background process exits and the adjudicated verdict parses,
   check for `$MINOS_RUN_DIR/memory-pressure` before acting on that
   verdict.

   Any adjudicated verdict may also contain `outOfScopeObservations`.
   These are unverified observations, not findings, and carry no verifier
   verdict. Keep them only in the saved run record. They never enter a
   review, the filing destination, a classification digest, or a run
   outcome.

   Only a verdict whose `status` is `complete` may proceed. A missing
   result, incomplete leg, or missing or invalid verifier result yields
   `incomplete` or `infrastructure-failure`. In either case, publish no
   review, set `"$MINOS_BIN" forge status HEAD TARGET incomplete`, remove
   the in-flight marker with `"$MINOS_BIN" forge marker HEAD TARGET in-flight remove`,
   write the non-clean terminal marker, and stop.
4. Once the main review's verdict is complete, run the repository-brief
   stage. When `review-result.json` is the engagement gate's
   `nothing-engages` verdict, this stage is already settled: the gate
   launched only because every `.review/` brief settled deterministically
   as skipped, so its verdict's `skipped` array is this stage's brief
   record — the same dispositions this workflow would recompute — and
   there is nothing to run, judge, or publish. Continue to step 5.
   Otherwise, build the repository-brief input directly as JSON:

   ```sh
   node "${MINOS_REVIEW_WORKFLOW%/*}/review-brief-inputs.mjs" \
     "$MINOS_TARGET_SHA" "$MINOS_HEAD_SHA" \
     > "$MINOS_RUN_DIR/review-brief-args.json"
   ```

   The input contains every `.review/` Markdown brief and its content,
   changed paths, and the tracked-file inventory used for full-extent
   lossless batching. Do not read, copy, hand-author, or agent-enumerate
   these values. Dispatch the stage once, under step 3's workflow
   discipline:

   ```sh
   "${MINOS_SETUP_WORKSPACE%/*}/dispatch-stage" review-brief review-briefs.js
   ```

   The script applies extent, sweep, occasion, path-scope, and relevance
   conditions. A brief with no condition is skipped. A specialist that
   finds nothing applicable records that disposition separately from
   findings, so it never reaches a verifier or becomes a published defect.
   A whole-brief skip remains in the verdict's `skipped` array;
   partition-level inapplicability remains as `inapplicableUnits` on its
   brief's disposition. None of it is published on the pull request: a
   skipped concern is reported as skipped in the run's record, never as
   passed. A brief whose declared scope directory does not exist is a
   misconfiguration, carried in the verdict's `misconfigurations` — a
   configuration defect distinct from an ordinary skip, filed as triage
   material in step 6.

   A `not-run` concern, missing result, incomplete leg, or missing or
   invalid verifier result makes this stage incomplete: publish no review,
   set `"$MINOS_BIN" forge status HEAD TARGET incomplete`, remove the in-flight
   marker with `"$MINOS_BIN" forge marker HEAD TARGET in-flight remove`, write the
   non-clean terminal marker, and stop.
5. Classify the completed review. For each complete verdict the run
   holds — the main review's, and the brief stage's where it ran — read
   its mechanical digest:

   ```sh
   node "${MINOS_REVIEW_WORKFLOW%/*}/verdict-classification.mjs" \
     --digest VERDICT_FILE "$MINOS_REVIEW_THRESHOLD" \
     > "$MINOS_RUN_DIR/verdict-digest.json"
   ```

   (`verdict-digest-brief.json` for the brief verdict, parallel to the
   decision filenames below.)

   The digest lists every confirmed finding with its severity, confidence
   values, and threshold position, and a `thresholdIndication` — what the
   configured threshold alone would say. Classification is your judgement,
   informed by the threshold rather than mechanically bound to it (the
   commission's balance rule): a confirmed finding at or above the
   threshold gates by default, and only two classes may be judged out —
   a gap the pull-request description explicitly declares as known or out
   of scope, where the project guidance does not contradict the
   declaration (`declared-out-of-scope`), and a verifier-confirmed request
   for a new safeguard against a hypothetical failure mode with no defect
   in the change's own logic (`speculative-hardening`). The severity
   threshold alone does not catch either class, because verifiers confirm
   the facts while the objection is to the kind. The one upward exception
   is judging a below-threshold finding's severity an undergrade that
   genuinely belongs at or above the threshold, named as such. Any gating
   finding makes the verdict `request-changes`; none makes it `clean`.
   A finding judged non-gating is advisory. Include it alongside the
   blocking findings when the run requests changes; deliver it to the
   repository's configured filing destination when the run is clean.

   Write your decision per verdict to
   `$MINOS_RUN_DIR/verdict-decision.json` (`verdict-decision-brief.json`
   for the brief verdict):

   ```json
   {
     "kind": "minos-verdict-decision-v1",
     "verdict": "request-changes",
     "basis": "one sentence stating the concrete grounds for this call",
     "findings": [
       { "key": "DIGEST_FINDING_KEY", "gating": true },
       { "key": "ANOTHER_KEY", "gating": false, "class": "declared-out-of-scope" },
       { "key": "A_BELOW_THRESHOLD_KEY", "gating": true, "undergrade": "why this severity is an undergrade" }
     ]
   }
   ```

   Validate each decision before any forge write:

   ```sh
   node "${MINOS_REVIEW_WORKFLOW%/*}/verdict-classification.mjs" \
     --validate VERDICT_FILE DECISION_FILE "$MINOS_REVIEW_THRESHOLD"
   ```

   A decision that fails validation — a missing basis, an unnamed
   declassification, an unnamed undergrade, a finding with no
   disposition — is corrected and re-validated rather than worked around;
   nothing has touched the forge yet. The run's overall verdict is
   `request-changes` when either validated decision is; `clean` only when
   every validated decision is clean.
6. Publish. Reviews talk about the code, never about Minos or its
   process; operational conditions — head moved, forge unreadable, review
   not reached — are always carried by the `Minos` status, never by a
   review.

   The publication composer turns the validated decisions into exactly
   what is posted, in the order it is posted, and sets aside what is
   filed instead; you compose no payload and choose no routing yourself.
   Run it once. When the brief stage did not run (or was settled by the
   gate), give it the main verdict and decision alone:

   ```sh
   node "${MINOS_REVIEW_WORKFLOW%/*}/compose-review-publication.mjs" \
     "$MINOS_RUN_DIR/publication" "$MINOS_ORIENTATION" "$MINOS_REVIEW_THRESHOLD" \
     "$MINOS_RUN_DIR/review-result.json" "$MINOS_RUN_DIR/verdict-decision.json"
   ```

   When the brief stage ran, add its verdict and decision as the sixth
   and seventh arguments:

   ```sh
   node "${MINOS_REVIEW_WORKFLOW%/*}/compose-review-publication.mjs" \
     "$MINOS_RUN_DIR/publication" "$MINOS_ORIENTATION" "$MINOS_REVIEW_THRESHOLD" \
     "$MINOS_RUN_DIR/review-result.json" "$MINOS_RUN_DIR/verdict-decision.json" \
     "$MINOS_RUN_DIR/review-brief-result.json" "$MINOS_RUN_DIR/verdict-decision-brief.json"
   ```

   It re-validates every decision against its verdict and writes nothing
   from one that fails; a failure here is a decision to correct at step
   5, never a payload to hand-assemble. Its plan,
   `$MINOS_RUN_DIR/publication/publication-plan.json`, lists the posts in
   order — each with its `verdict` argument and the body and comments
   files it wrote — and names the triage entries file under `triage`.
   Post the reviews in that order, one scripted review per entry,
   substituting the entry's `verdict`, `body` and `comments` values and
   the run's head and target:

   ```sh
   "$MINOS_BIN" forge review HEAD TARGET VERDICT BODY_FILE COMMENTS_FILE
   ```

   A request-changes plan contains one review with all confirmed findings
   from both review stages, including verified Medium and Low findings.
   Blocking and advisory comments are labelled separately. The same defect
   raised by both stages at one site is merged once, naming the brief that
   also raised it. A clean plan has no posts: publish no approval review,
   summary comment, or finding comment; step 7 supplies the clean marker and status.
   The guarded command anchors what the diff geometry allows and folds
   the rest into the review body — a confirmed finding is
   never dropped or moved to a line it does not concern, and placement
   degrades all the way to the review body, never past it. The guarded
   review command deduplicates an exact pre-existing review, so a retry
   converges. It signs each review body with ``Reviewed by:
   `$MINOS_LEAD_MODEL`.`` when that configured identity is present — the
   comments already carry their proposing and verifying models — and the
   signature is payload material only, never a later read-back or
   decision input.

   A gating finding that reached no durable surface is not a
   presentation problem — the review did not happen, and no clean marker or
   approval may follow: treat a rejected or uncertain findings-review
   write as the run's failure, set `incomplete`, remove the in-flight marker, write the
   non-clean terminal marker, and stop.

   Deliver the plan's `triage.entries` to the repository's configured
   filing destination. On a clean run it contains the confirmed advisory
   findings; brief configuration diagnostics and the guidance sources
   setup could not read may be filed on either outcome. Unverified observations are excluded. The destination — a file
   in a repository's default branch, an issue on a named repository, a
   comment on the pull request, or nowhere — is the service's exported
   configuration, never a choice you make; the script files each entry
   once, skipping entries the destination already carries, and commits as
   the configured identity. Run it after any planned review:

   ```sh
   node "${MINOS_REVIEW_WORKFLOW%/*}/file-triage.mjs" \
     "$MINOS_RUN_DIR/publication/triage-entries.json" \
     "$MINOS_ORIENTATION" \
     > "$MINOS_RUN_DIR/triage-result.json"
   ```

   The result names the destination `kind` and one `outcome`: `filed`
   means the destination took the entries (`written` counts the new ones,
   and may be zero when every entry was already there); `nothing-to-file`
   means the batch held nothing deliverable; `discarded` means the
   repository files nowhere by configuration; `unfiled` means delivery
   failed or the destination kind is unavailable, with the `reason`.
   Record an `unfiled` reason in the run report and continue to the
   terminal outcome. Filing failure never generates a pull-request comment,
   never falls back to another surface, and changes no verdict.

   Check for `$MINOS_RUN_DIR/memory-pressure` after publication.
7. End at the earned terminal outcome.

   **Clean:** post no review of your own (a pull-request-comment filing
   destination may already have posted the advisory entries as one
   comment review, which carries no verdict); set
   `"$MINOS_BIN" forge status HEAD TARGET clean`, then add the clean marker with
   `"$MINOS_BIN" forge marker HEAD TARGET clean add`, and remove the
   in-flight marker with
   `"$MINOS_BIN" forge marker HEAD TARGET in-flight remove`, write the clean
   terminal marker, and stop.

   **Request-changes:** the blocking review is posted; set
   `"$MINOS_BIN" forge status HEAD TARGET attention`, add the attention
   marker with `"$MINOS_BIN" forge marker HEAD TARGET attention add`,
   remove the in-flight marker, write
   the non-clean terminal marker, and stop.

   What happens next belongs to the author: they fix, re-push, and the
   moved head is simply a new eligible state the sweep claims fresh. Every
   terminal outcome the run itself reaches ends with the in-flight marker absent; a crash alone
   leaves it for the next idempotent claim.

Use your judgement. Retry an ordinary transient failure when that is
sensible; otherwise report the actual state, finish all final forge writes
and cleanup, write the non-clean terminal marker, and stop. Never turn a
failure into a new process, checklist, gate, or framework.
