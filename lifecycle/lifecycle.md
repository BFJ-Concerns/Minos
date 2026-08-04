# Review this pull request

Own this pull request from review to a useful conclusion. You are trusted and
have an entire disposable machine: clone the repository, build it, test it,
edit it, commit, and push as needed. Do not build additional containment,
coordination, evidence, admission, clearance, or self-verification machinery.

The pull request is `$MINOS_OWNER/$MINOS_REPO_NAME#$MINOS_PR`. The observed head
is `$MINOS_HEAD_SHA`; the target is `$MINOS_TARGET_SHA` on `$MINOS_BASE_REF`.
The forge root is `$MINOS_API_BASE`, and `$MINOS_CREDENTIAL_FILE` contains the
token. Work in `$MINOS_RUN_DIR/workspace`.

Whenever you stop at a terminal outcome that is neither a clean, converged pass
nor a planned continuation,
append one line to `$MINOS_FAILURE_LOG` before you stop — and before any cleanup
or reaction removal. This covers **every failed** non-clean exit you make, not only the
ones that set a status: a stop that sets `incomplete` or `attention`, and equally
a setup or finishing head/target move, a failed build or test, an unparseable
workflow result, or any other unrecovered error that ends the run short of a
clean pass. Record the pull request and head, the stage that failed, and the
concrete cause — what the workflow verdict, a failed command, or a dispatched
helper actually reported, not a restatement of the status. This line is the
operator's durable record of why a run did not converge — the only trace once the
run's scratch is gone — so write the real reason. Once all final forge writes
and cleanup for that non-clean outcome have succeeded, run
`printf 'non-clean\n' > "$MINOS_RUN_DIR/lead-complete"` as your last action
before ending the turn. This marker is a separate absolute terminal obligation:
the failure log records why the run did not converge, while the marker proves
that no work or wake remains pending. Never write it before the terminal work
and cleanup are complete.

Whenever you stop after a clean, converged pass, run
`printf 'clean\n' > "$MINOS_RUN_DIR/lead-complete"`. This is an absolute
terminal obligation, symmetric with the non-clean path's failure record and
terminal marker. Run it only after all final forge writes and cleanup have
succeeded, as your last action before ending the turn; the supervisor treats it
as proof that there is no work or wake still pending.

`$MINOS_RUN_DIR/memory-pressure` is a one-shot signal from the supervisor that
the run is approaching its memory ceiling. Check for it only at the named
boundaries below. When it exists, finish the current lifecycle stage; do not
interrupt a workflow or leave a forge write half-finished. Then continue the run
only through the handoff procedure below. The signal is latched for this attempt.

At a pressure boundary, take a fresh `"$MINOS_BIN" forge snapshot`; use its
`head_sha` and `target_sha` for every remaining action. Run
`"$MINOS_BIN" forge status FRESH_HEAD FRESH_TARGET continuation`. Keep 👀 in
place: the successor claims idempotently, and the reaction remains true across
the handoff. There is no numerical continuation ceiling.

Record one progress observation from durable facts only. `stage` is the concise,
stable name of the lifecycle stage just completed; use the same name whenever a
successor stops at that boundary. `round` is the current loop record's round.
`head` is the fresh snapshot's head. `latestReview` is the greatest review ID in
that snapshot authored by its `authenticated_user` on that head, or `0` when
there is none. These forge facts name publication progress without trusting
scratch files or process state. Copy `$MINOS_PREDECESSOR_PROGRESS` as
`predecessorProgress` when it exists; otherwise omit that field. Missing legacy
progress therefore means the successor cannot compare and is allowed to start.
The successor admission compares this predecessor/current pair: the same stage
and round with the same head and latest review ends the chain as `attention`,
records the cause, removes 👀 and does not spawn; a changed publication, a later
round, or a different stage may continue.

The handoff is `$MINOS_HANDOFF`. It must be written before the terminal marker
and match this exact JSON shape — field names and nesting are validated
strictly, and a handoff in any other shape is rejected, which costs the
successor the preserved workspace:

```json
{
  "kind": "minos-run-handoff-v1",
  "pullRequest": { "owner": "OWNER", "repo": "REPOSITORY", "number": "NUMBER" },
  "head": "FRESH_SNAPSHOT_HEAD_SHA",
  "runDir": "$MINOS_RUN_DIR value",
  "stoppedAt": "concise description of the stopping point",
  "writtenAt": "RFC 3339 timestamp",
  "runRecord": { the complete JSON object from $MINOS_LOOP_RECORD },
  "predecessorProgress": { the complete JSON object from $MINOS_PREDECESSOR_PROGRESS, when set },
  "progress": {
    "stage": "stable name of the lifecycle stage just completed",
    "round": CURRENT_ROUND,
    "head": "FRESH_SNAPSHOT_HEAD_SHA",
    "latestReview": LATEST_CURRENT_HEAD_REVIEW_ID_OR_ZERO
  }
}
```

`pullRequest.number` is a JSON string. The top-level and progress `head` values
are both the fresh snapshot's `head_sha`, and the progress and loop-record
`round` values are equal. If the loop record does not yet exist, first
write `{"round":0,"confirmedFixed":[],"confirmedUnfixed":[]}` to it. The
snapshot's head is load-bearing: `$MINOS_HEAD_SHA` is the head this attempt
started from and may already be stale after a repair push. Write the complete
handoff to `"$MINOS_HANDOFF.tmp"`, then atomically `mv` it to
`"$MINOS_HANDOFF"`. Finally run
`printf 'continuation\n' > "$MINOS_RUN_DIR/lead-complete"` as the
last action and end the turn. A continuation writes no failure-log line.

1. The setup script has prepared the repository at the observed pull-request head
   in `$MINOS_WORKSPACE`, either from a fresh clone or a validated preserved
   workspace. It refused setup if that head moved, and
   recorded its orientation in
   `$MINOS_ORIENTATION`. Read that record. When its `grounding` is `annexe`,
   read the commission in the recorded annexe README as the driving statement
   of the project; when it is `repository`, no sibling annexe exists, so ground
   the work in the repository's own checked-in guidance. Then run
   `"$MINOS_BIN" forge snapshot` and claim the pull request with `"$MINOS_BIN"
   forge claim` (it assigns the Minos account and adds the 👀 reaction; it is
   safe to repeat). If the snapshot now shows that the head or target has moved
   since setup, remove 👀 against that fresh head and target, write the
   non-clean terminal marker, and stop without publishing.

   Check for `$MINOS_RUN_DIR/memory-pressure` after the claim and snapshot
   checks. If `$MINOS_LOOP_RECORD` already exists, a predecessor handed off:
   its round continues and its confirmed-unfixed findings are already
   suppressed from later dispatch. The loop record is the only inherited
   decision state. Other preserved files are reusable workspace material, not
   authority; the orientation and reconciliation records describe the state to
   trust in this attempt.

   Read `$MINOS_RUN_DIR/reconciliation.json`. Setup has fetched the target from
   the base repository, verified its observed SHA and pinned it at
   `refs/minos/target` for the whole run. The record says whether merging that
   target locally was unnecessary, completed cleanly, or left a conflict in
   progress. A completed reconciliation merge exists only in the detached
   reading workspace. Minos records it as unpushable, while every repair and
   finishing push operates on the separate publication worktree recorded in
   this file. Never move the pinned target during the review loop: it fixes
   both the code context and the merge-base-to-head comment geometry.

   Run the setup workflow on every run, whatever the reconciliation outcome.
   Launch it from `$MINOS_WORKSPACE` as one background Bash task under step 4's
   workflow discipline, with diagnostics beside the result:

   ```sh
   node "${MINOS_REVIEW_WORKFLOW%/*}/setup-inputs.mjs" "$MINOS_HEAD_SHA" \
     > "$MINOS_RUN_DIR/setup-args.json"
   node /opt/minos/runtime/ensemble.mjs \
     --json-args @"$MINOS_RUN_DIR/setup-args.json" \
     "${MINOS_REVIEW_WORKFLOW%/*}/setup.js" \
     > "$MINOS_RUN_DIR/setup-result.json" \
     2> "$MINOS_RUN_DIR/setup-result.log"
   ```

   Wait for the background process to exit before reading the result. A
   non-zero process exit, invalid JSON, a result whose `status` is not
   `complete`, or `environment.ready: false` is an incomplete terminal
   outcome. Append the returned `reason` or `environment.cause` to
   `$MINOS_FAILURE_LOG`, set `incomplete`, remove 👀, write the non-clean
   terminal marker, and stop.

   When `reconciliation.attempted` is true, check the resolution content rather
   than recreating its process. Run
   `"${MINOS_SETUP_WORKSPACE%/*}/show-resolutions"` and compare each
   marker-bearing preimage with the staged result and the corresponding
   `resolutions[].note`. Accept a file only when the staged content preserves
   both parents' intent and introduces no content found in neither. Never edit
   a conflicted file yourself.

   For a rejected path, run
   `"${MINOS_SETUP_WORKSPACE%/*}/reopen-conflict" "$MINOS_WORKSPACE" PATH...`,
   write one JSON objections file containing the rejected paths and concrete
   objections, then redispatch once:

   ```sh
   node "${MINOS_REVIEW_WORKFLOW%/*}/setup-inputs.mjs" "$MINOS_HEAD_SHA" \
     --reconcile-only --objections "$MINOS_RUN_DIR/setup-objections.json" \
     > "$MINOS_RUN_DIR/setup-retry-args.json"
   node /opt/minos/runtime/ensemble.mjs \
     --json-args @"$MINOS_RUN_DIR/setup-retry-args.json" \
     "${MINOS_REVIEW_WORKFLOW%/*}/setup.js" \
     > "$MINOS_RUN_DIR/setup-retry-result.json" \
     2> "$MINOS_RUN_DIR/setup-retry-result.log"
   ```

   Apply the same process-exit and result checks to the retry, then inspect its
   bounded resolution diff. A second unacceptable resolution ends the run
   incomplete with the specific objection recorded. Write the non-clean
   terminal marker and stop. Once every resolution stands, run
   `"${MINOS_SETUP_WORKSPACE%/*}/complete-reconciliation"
   "$MINOS_WORKSPACE"`. Completion also supersedes the original full setup
   result with the retry's non-reusable reconciliation-only command outcomes,
   because the retry changed the reconciled tree.

   The same check, one-retry and completion rule applies when a later Minos
   script reports `reconcile-conflict`: redispatch the setup workflow in
   reconciliation-only mode before continuing the round. The change under
   review remains the pinned target-to-current-pull-request-head range. The
   reconciliation merge supplies the context in which code is read; it is never
   itself part of the reviewed change.
2. Publish `working` with `"$MINOS_BIN" forge status HEAD TARGET working`.
3. Read the repository guidance and the complete target-to-head diff. The
   repository's configured build and test commands are already resolved for you
   in `$MINOS_BUILD_CMD` and `$MINOS_TEST_CMD`. First run:

   ```sh
   node "${MINOS_REVIEW_WORKFLOW%/*}/completion-policy.mjs" \
     --setup-result "$MINOS_RUN_DIR/setup-result.json" "$MINOS_HEAD_SHA" \
     "$MINOS_BUILD_CMD" "$MINOS_TEST_CMD" \
     > "$MINOS_RUN_DIR/setup-reuse.json"
   ```

   Read its one-line JSON `reusable` field. It is true only when the setup
   result is valid and complete, its recorded head is exactly
   `$MINOS_HEAD_SHA`, both recorded command strings exactly match the configured
   strings, and every configured command has a genuine `passed` outcome from
   the completion policy. Only then consume its build and test results and do
   not execute either command again. An absent, malformed, partial,
   command-mismatched, non-passing, or stale-head setup result has `reusable:
   false`; execute both configured commands normally as follows. Never reuse a
   `skipped` outcome for a configured command.

   When `$MINOS_BUILD_CMD` is
   non-empty, run that exact command string exactly as configured — never a
   repository-native guess or substitute of your own — to completion before any
   review starts; when it is empty, no build command is configured, so skip it
   rather than inventing one. When `$MINOS_TEST_CMD` is non-empty, run that exact
   command string exactly as configured — never a repository-native guess or
   substitute of your own — to completion before any review starts; when it is
   empty, no test command is configured, so skip it rather than inventing one.
   For each command, run `node
   "${MINOS_REVIEW_WORKFLOW%/*}/completion-policy.mjs" COMMAND EXIT_STATUS`,
   passing the configured string and its exit status. Read the CLI's one-line
   JSON `status` field. A `skipped` or `passed` status may continue. On a
   `failed` status, append the command and its non-zero exit status to
   `$MINOS_FAILURE_LOG`, set
   `"$MINOS_BIN" forge status HEAD TARGET incomplete`, remove the 👀 with
   `"$MINOS_BIN" forge reaction-remove HEAD TARGET eyes`, write the non-clean
   terminal marker, and stop before starting any review.
4. Run every Ensemble workflow in this lifecycle from this accountable lead
   session. Never delegate its invocation to an agent or subagent;
   responsibility for orchestration remains with the lead. Launch each such
   workflow through Bash with `run_in_background` — do not append shell `&`.
   The Bash task then owns the workflow process, remains alive across turns and
   sends one completion notification when that process exits. This protects a
   workflow that runs beyond a foreground command's ten-minute limit; it does
   not relax any ordering or publication precondition elsewhere in this
   lifecycle.

   This governs every Ensemble command block below — the main review, fix
   waves, brief review and brief fix. Each block names its result JSON; redirect
   diagnostics to the same basename with `.log` instead of `.json`, then submit
   the complete command as one background Bash task. Record its task ID and use
   its completion notification as the primary watcher on process exit. Before
   ending the turn, always call `ScheduleWakeup` with a delay of 1200 seconds
   or more and a prompt naming the task to re-check. The completion
   notification is the fast path, so this wake exists only to bound the
   silence if that notification never arrives — a shorter delay polls for work
   the harness already reports and costs a full context re-read each time. If
   the wake fires first, inspect the task when useful, confirm whether it is
   still running and re-arm the fallback before ending the turn again. Keep
   every such delay comfortably below `MINOS_LEAD_SILENCE_TIMEOUT`
   (3600 seconds by default): the supervisor treats a lead with no observed
   turn activity for that long as ended, so a fallback at or beyond it would
   let a live waiting lead be stopped. You may additionally create a
   session-only inspection timer with `CronCreate` when progress merits a
   closer look, but it supplements rather than replaces the process-exit
   watcher and fallback wake. When the task completion notification arrives,
   cancel the fallback with `ScheduleWakeup`'s `stop: true`.

   Check for `$MINOS_RUN_DIR/memory-pressure` at every completion notification
   or fallback wake, after confirming the background process's state. Finish a
   process that is still running before handing off.

   If you believe `ScheduleWakeup` is unavailable to you, record that belief in
   `$MINOS_FAILURE_LOG` and fall back to a `CronCreate` timer at the same
   interval, cancelling it with `CronDelete`. Never respond to a tool you
   think is missing by going silent: an unwatched wait is how a live run
   reaches the silence backstop with work still in flight. The record matters
   as much as the fallback — a lead that reports a tool missing has either met
   a real harness fault worth fixing or misjudged its own capabilities, and
   only the log line distinguishes them.

   Do not sleep for a guessed duration and do not trust the result file merely
   because it exists: redirection creates it immediately and it may be
   half-written. Only once the background process has exited is the result file
   complete; then read it and parse it as JSON before trusting the verdict. A
   result file that does not parse, or a background task that exits non-zero, is
   an infrastructure failure — treat it as an incomplete stop, never as a
   verdict.

   Build the main review input from disk and write it to a file:

   ```sh
   node "${MINOS_REVIEW_WORKFLOW%/*}/review-inputs.mjs" \
     "$MINOS_TARGET_SHA" "$MINOS_HEAD_SHA" \
     --loop-record "$MINOS_LOOP_RECORD" \
     > "$MINOS_RUN_DIR/review-args.json"
   ```

   This deterministic file-reading seam validates `$MINOS_ORIENTATION`, reads
   the selected annexe commission or repository-fallback guidance, and supplies
   that content with the shipped role briefs. Do not ask an agent to reproduce
   it or hand-author its `guidance` or `instructionBriefs` entries.

   Choose exactly one branch:

   - **Carried result:** If
     `$MINOS_RUN_DIR/carried-review-result.json` exists, the service validated
     it as a complete predecessor verdict for exactly `$MINOS_HEAD_SHA`.
     Consume the one-time carry with this command:

   ```sh
   mv "$MINOS_RUN_DIR/carried-review-result.json" \
     "$MINOS_RUN_DIR/review-result.json"
   ```

     Do not invoke the review workflow for this first review.

   - **No carried result:** From `$MINOS_WORKSPACE`, invoke the adjudication
     wrapper once and save its verdict:

   ```sh
   "$MINOS_REVIEW_WORKFLOW" \
     "${MINOS_REVIEW_WORKFLOW%/*}/review.js" \
     --json-args @"$MINOS_RUN_DIR/review-args.json" \
     > "$MINOS_RUN_DIR/review-result.json"
   ```

   An ordinary `review-result.json` is never a carry signal. After a fix wave,
   invoke the workflow again for the fresh head.

   The workflow binds the project guidance into exploration, every specialist,
   and every verifier. It returns a pre-adjudication envelope. The wrapper owns
   an isolated Ensemble run-record directory, checks that every leg completed
   and every finding has a complete verifier verdict, and returns the
   adjudicated verdict. Do not read the archive, judge the workflow's engine
   pairing, resume the workflow, or duplicate its findings yourself.

   After the background process exits and the adjudicated verdict parses,
   check for `$MINOS_RUN_DIR/memory-pressure` before acting on that verdict.

   Any adjudicated verdict may also contain `outOfScopeObservations`. These are
   unverified observations, not findings, and carry no verifier verdict. Keep
   them in the saved verdict, but do not publish them through this lifecycle;
   their publication is owned separately.

   Only a verdict whose `status` is `complete` is publishable. A missing result,
   incomplete leg, or missing or invalid verifier result yields `incomplete` or
   `infrastructure-failure`. In either case, publish no review, set
   `"$MINOS_BIN" forge status HEAD TARGET incomplete`, remove the 👀 with
   `"$MINOS_BIN" forge reaction-remove HEAD TARGET eyes`, write the non-clean
   terminal marker, and stop.
5. Save each complete review verdict, then read this sweep's mechanical
   digest:

   ```sh
   node "${MINOS_REVIEW_WORKFLOW%/*}/fix-inputs.mjs" \
     REVIEW_RESULT "$MINOS_LOOP_RECORD" --digest \
     > "$MINOS_RUN_DIR/sweep-digest.json"
   ```

   The digest reports the round number, the configured threshold, which
   confirmed findings are dispatchable (not already confirmed-unfixed),
   which of those sit at or above the threshold, whether the configured
   maximum rounds has been reached, and a `thresholdIndication` — what the
   threshold alone would say. Classifying the sweep is your judgement,
   informed by the threshold rather than mechanically bound to it: judge
   the sweep `working` when its findings genuinely warrant another fix
   wave, and `terminal` when nothing new at or above the threshold remains
   beyond entries already confirmed-unfixed — or when, in your judgement,
   another wave would not move the pull request forward. Follow the
   indication unless you can state a concrete reason not to; the reason
   travels in the decision. Two bounds are mechanical, not judgement: a
   reached maximum rounds is always terminal, and `working` needs at least
   one dispatchable finding. Write your decision to
   `$MINOS_RUN_DIR/sweep-decision.json`:

   ```json
   {
     "kind": "minos-sweep-decision-v1",
     "classification": "working",
     "basis": "one sentence stating the concrete grounds for this call"
   }
   ```

   Then build the action input from the verdict, the loop record and your
   decision:

   ```sh
   node "${MINOS_REVIEW_WORKFLOW%/*}/fix-inputs.mjs" \
     REVIEW_RESULT "$MINOS_LOOP_RECORD" \
     --decision "$MINOS_RUN_DIR/sweep-decision.json" \
     > "$MINOS_RUN_DIR/fix-args.json"
   ```

   A decision that fails validation — a missing basis, `working` with
   nothing to dispatch, or `working` past the configured maximum rounds —
   makes preparation incomplete before any forge write; correct the
   decision and rebuild the input rather than working around it. Tell the
   two incomplete shapes apart by the `publication` field: a rejected
   preparation carries none (nothing touched the forge — correct and
   retry), while an incomplete result bearing `publication` means a forge
   write was attempted, and the stop below applies.

   Invoke the publication-before-fix operation once for this round:

   ```sh
   "${MINOS_REVIEW_WORKFLOW%/*}/publish-before-fix" \
     "$MINOS_RUN_DIR/fix-args.json" HEAD TARGET \
     > "$MINOS_RUN_DIR/fix-result.json"
   ```

   This one production operation prepares an immutable wave without agents. A
   `working` preparation materialises the exact sweep review, publishes it
   through the guarded forge command, and starts the effectful `fix.js`
   dispatcher only after the command returns `outcome: "applied"`. An exact
   pre-existing review is applied and may continue; every other publication
   result returns `incomplete` without starting the dispatcher. On an
   incomplete result, set `"$MINOS_BIN" forge status HEAD TARGET incomplete`,
   remove the 👀 with `"$MINOS_BIN" forge reaction-remove HEAD TARGET eyes`,
   write the non-clean terminal marker, and stop.

   The operation's returned `runRecord` is the run-scoped loop record, including
   the round count and both confirmed-fixed and confirmed-unfixed findings.
   The same record supplies later review specialists with prior-finding context;
   they judge equivalence and may still report regressions, materially different
   nearby defects, and genuinely new findings. `$MINOS_LOOP_RECORD` is its
   only path; an absent file means round zero. After every complete
   classification, write the returned `runRecord` to
   `"$MINOS_LOOP_RECORD.tmp"` and atomically move it over
   `$MINOS_LOOP_RECORD`. Pass it
   back through `fix-inputs.mjs` when the build, tests and whole review re-enter
   this step on the next head; the operation is invoked once per round. The
   configured threshold and optional maximum rounds arrive through
   `$MINOS_REVIEW_THRESHOLD` and `$MINOS_MAX_ROUNDS`.

   When related findings should be repaired together, write a grouping file
   for that round and pass `--grouping FILE` to `fix-inputs.mjs`. Its shape is
   `{"kind":"minos-fix-grouping-v1","groups":[{"findings":["FINDING_ID"]}]}`.
   Every candidate finding must occur exactly once. Unknown, duplicate, omitted
   or malformed membership makes preparation incomplete before any forge write.
   Findings already recorded as confirmed-unfixed may remain in the file; the
   mechanism removes them and drops any group left empty. Without a grouping
   file, each candidate finding receives its own dispatch. Terminal
   classification ignores grouping because it dispatches nothing.

   A `working` classification means you judged the sweep to warrant another
   fix wave and its sweep review was confirmed present before dispatch. The
   prepared wave contains every confirmed finding
   in the sweep, including those below threshold, as one review with inline
   path/line comments. Each fix dispatch carries its assigned findings, grouped
   only when the lead supplied that judgement. Fix agents may read and edit
   whatever those repairs genuinely require in isolated Codex worktrees,
   commit with the configured Minos identity, and never push. A failed finding
   receives one retry; after two failures it remains in the loop record as
   confirmed-unfixed and later sweeps do not dispatch it again.
   When `integration.commits` is non-empty, write that array unchanged to a
   file and run `"${MINOS_REVIEW_WORKFLOW%/*}/integrate-wave"
   "$MINOS_WORKSPACE" COMMITS_FILE`. That script verifies every commit's Minos
   author, cherry-picks the wave and performs exactly one push to
   `$MINOS_HEAD_BRANCH`. No fix agent may push.

   The workspace's pre-push guard protects the recorded pull-request ref by
   destination ref name, regardless of the remote name or whether Git was
   invoked with a short refspec. Updates require the matching one-use permit,
   including forced updates and deletion attempts. It also refuses any
   non-deletion update containing a recorded local reconciliation commit. An
   unreadable or invalid record fails closed; in particular, an empty
   `minos-protected-ref` is invalid. The hook covers checkouts sharing this Git
   directory, not separate clones.

   After the push, read a fresh forge snapshot and use its current head. When
   `fixReview` is present, write that object unchanged to a file, then post it
   with `"$MINOS_BIN" forge comment CURRENT_HEAD "$MINOS_TARGET_SHA"
   FIX_REVIEW_FILE`. This durable pull-request comment is not a
   review group; the guarded command binds it to that exact head and target,
   deduplicates retries, and reads it back before reporting success. Then run the exact configured build and test
   commands again. Run `node
   "${MINOS_REVIEW_WORKFLOW%/*}/completion-policy.mjs" COMMAND EXIT_STATUS`
   for each configured string and its exit status, just as in step 3, and read
   the CLI's one-line JSON `status` field; empty strings are skipped. On a
   `failed` status, append the command and its non-zero exit status to
   `$MINOS_FAILURE_LOG`, set `"$MINOS_BIN" forge status CURRENT_HEAD
   "$MINOS_TARGET_SHA" incomplete`, remove the 👀 with `"$MINOS_BIN" forge
   reaction-remove CURRENT_HEAD "$MINOS_TARGET_SHA" eyes`, write the non-clean
   terminal marker, and stop before another review round or merge action.
   Only after both gates pass or skip, run the complete input-builder and
   adjudication-wrapper flow on the new head. Do the same fresh build, test,
   and gated review even when every attempted fix failed and no commit was
   integrated. Continue from the digest and a fresh sweep decision — every
   round's classification is recorded the same way, and the decision file is
   per round, never reused.
   After each complete re-review verdict, check for
   `$MINOS_RUN_DIR/memory-pressure` before preparing another round.
6. A `terminal` classification means you judged that no finding at or above
   the threshold warrants another wave beyond entries already
   confirmed-unfixed, or the configured maximum
   rounds has been reached. Do not post the terminal sweep's sub-threshold
   findings and do not dispatch fix agents for them. Write the plan's
   `overflow` array unchanged to a file and run:

   ```sh
   node "${MINOS_REVIEW_WORKFLOW%/*}/publish-overflow.mjs" \
     "$MINOS_ORIENTATION" OVERFLOW_FILE \
     > "$MINOS_RUN_DIR/overflow-result.json"
   ```

   With an annexe it appends each finding once to `ISSUES.md`, commits and
   pushes the annexe itself. Without an annexe its result is
   `destination: "pull-request"` with one `body`/`comments` payload:
   materialise those to files and post them as one `comment` review with
   `"$MINOS_BIN" forge review HEAD TARGET comment BODY_FILE COMMENTS_FILE`,
   still without fix agents. Overflow publication is presentation-class:
   its failure never fails the run.

   If `requestChangesReview` is present, materialise it exactly, post it with
   `"$MINOS_BIN" forge review HEAD TARGET request-changes BODY_FILE
   COMMENTS_FILE`, and set status `attention`. Otherwise set status `clean`.
   Review prose talks only about the code, never about Minos, its process or a
   round number. Operational conditions — head moved, forge unreadable, review
   not reached — are always carried by the `Minos` status, never by a review.
7. Once the main loop has reached its terminal classification, build the
   repository-brief input directly as JSON:

   ```sh
   node "${MINOS_REVIEW_WORKFLOW%/*}/review-brief-inputs.mjs" \
     "$MINOS_TARGET_SHA" CURRENT_HEAD \
     > "$MINOS_RUN_DIR/review-brief-args.json"
   ```

   The input contains every `.review/` Markdown brief and its content, changed
   paths, and the tracked-file inventory used for full-extent lossless
   batching. Do not read, copy, hand-author, or agent-enumerate these values.
   Call the adjudication wrapper once:

   ```sh
   "$MINOS_REVIEW_WORKFLOW" \
     "${MINOS_REVIEW_WORKFLOW%/*}/review-briefs.js" \
     --json-args @"$MINOS_RUN_DIR/review-brief-args.json" \
     > "$MINOS_RUN_DIR/review-brief-result.json"
   ```

   The script applies extent, sweep, occasion, path-scope, and relevance
   conditions. A brief with no condition is skipped. A specialist that finds
   nothing applicable records that disposition separately from findings, so it
   never reaches a verifier or becomes a published defect. A whole-brief skip
   remains in the verdict's `skipped` array; partition-level inapplicability
   remains as `inapplicableUnits` on its brief's disposition — under a `ran`
   entry when other partition units ran, or under the `skipped` entry when
   every unit was inapplicable. None of it is published on the pull request.
   A `not-run` concern, missing result, incomplete leg, or missing or invalid
   verifier result makes this stage incomplete: publish no brief review, add no
   👍, set `"$MINOS_BIN" forge status CURRENT_HEAD "$MINOS_TARGET_SHA"
   incomplete`, remove the 👀 with `"$MINOS_BIN" forge reaction-remove
   CURRENT_HEAD "$MINOS_TARGET_SHA" eyes`, write the non-clean terminal marker,
   and stop.

   On a complete verdict, materialise `reviewBody.body` and
   `reviewBody.comments` when `reviewBody` is present, then post exactly one
   distinct review group with `"$MINOS_BIN" forge review CURRENT_HEAD
   "$MINOS_TARGET_SHA" comment BODY_FILE COMMENTS_FILE`. The guarded review
   command makes that group idempotent. Do not publish a review when there are
   no confirmed brief findings, and never publish an all-clear comment.

   When `briefFixRequired` is false, the brief stage has passed: add the 👍 with
   `"$MINOS_BIN" forge reaction CURRENT_HEAD "$MINOS_TARGET_SHA" +1` and
   continue to finishing. When `briefFixRequired` is true, save the complete brief
   verdict and build the single-wave input:

   ```sh
   node "${MINOS_REVIEW_WORKFLOW%/*}/fix-inputs.mjs" \
     BRIEF_RESULT --single-wave \
     > "$MINOS_RUN_DIR/brief-fix-args.json"
   node /opt/minos/runtime/ensemble.mjs \
     --json-args @"$MINOS_RUN_DIR/brief-fix-args.json" \
     "${MINOS_REVIEW_WORKFLOW%/*}/fix.js" \
     > "$MINOS_RUN_DIR/brief-fix-result.json"
   ```

   Before reading any other result field, read `status`. If it is `incomplete`,
   append its `reason` to `$MINOS_FAILURE_LOG`, set `"$MINOS_BIN" forge status
   CURRENT_HEAD "$MINOS_TARGET_SHA" incomplete`, remove the 👀 with
   `"$MINOS_BIN" forge reaction-remove CURRENT_HEAD "$MINOS_TARGET_SHA" eyes`,
   write the non-clean terminal marker, and stop.

   This mode dispatches every confirmed brief finding exactly once and never
   requests a second brief review or fix wave. If its
   `integration.commits` is non-empty, integrate them through `integrate-wave`
   exactly as in the main loop, then take a fresh forge snapshot and use its
   current head. When `fixReview` is present, write that object unchanged and
   post the same location-bearing guarded pull-request comment described in
   step 5 on that fresh head. Run `$MINOS_BUILD_CMD` and
   `$MINOS_TEST_CMD` exactly as configured and to completion when each is
   non-empty; do not substitute or invent commands. If the single wave leaves
   `confirmedUnfixed` entries, or integration, build or tests fail, add no 👍,
   set attention or incomplete to reflect the actual result, remove the 👀,
   write the non-clean terminal marker, and stop. If all fixes were integrated
   and the exact configured build and tests pass, the brief stage has passed:
   add the 👍 on the fresh head. Do not run another review loop. Thus both a
   no-findings pass and a findings-fixed-and-verified pass end in the same
   idempotent 👍 signal.

8. Enter finishing only after both review stages are done and the current result
   is clean. If the result is attention, remove the 👀 with `"$MINOS_BIN" forge
   reaction-remove CURRENT_HEAD "$MINOS_TARGET_SHA" eyes`, write the non-clean
   terminal marker, and stop instead.

   Save a fresh `"$MINOS_BIN" forge snapshot` JSON object. It is the sole forge
   state used throughout finishing. For the initial and each fresh snapshot,
   require `dependencies_available` to be true and `open_dependencies` to be
   empty before taking another finishing action. If either condition fails,
   append the concrete condition to `$MINOS_FAILURE_LOG`: use
   `dependency_error` for unavailable state, or the exact `repository#number`
   values for open dependencies. Set `incomplete`, remove 👀, write the
   non-clean terminal marker, and stop. Run
   `"${MINOS_SETUP_WORKSPACE%/*}/sync-target"
   "$MINOS_WORKSPACE" HEAD_BRANCH TARGET_BRANCH HEAD TARGET
   TARGET_SYNC_METHOD` with its `head_branch`, `target_branch`, `head_sha`,
   `target_sha`, and `target_sync_method`. The script fetches and validates
   both named branches, follows the forge's merge-or-rebase update style, and
   uses a lease-bound single push. An unchanged target is a no-op. On a real
   conflict, resolve only the merge or rebase conflict in the lead workspace,
   complete that Git operation, then run the same script again so it performs
   the guarded push. This is target reconciliation, not a new implementation:
   do not dispatch another review. After a sync, take a fresh snapshot, require
   its head to equal the script's pushed head and its target to remain the
   fetched target, then run the exact configured build and test commands to
   completion when each is non-empty. A failed sync, build, or test is an
   incomplete terminal outcome: set `incomplete`, remove 👀, write the
   non-clean terminal marker, and stop.

   Required checks, labels and merge readiness all come from that same trusted
   snapshot. When waiting for checks or forge readiness, write the complete
   snapshot to a file and run `"${MINOS_SETUP_WORKSPACE%/*}/watch-snapshot"
   SNAPSHOT_FILE 600`.
   The watcher repeatedly obtains the same `minos forge snapshot` object used
   everywhere else and wakes when its `head_sha`, `target_sha`, `statuses`,
   `labels`, dependency availability, dependency error, or open dependencies
   differ. Its JSON result contains the exact fresh snapshot to use
   for the next decision. On its roughly ten-minute `timeout`, use the returned
   fresh snapshot as well; do not infer state from elapsed time, poll the forge
   separately, or sleep blind. If the target moved, return to target sync. If
   an unexpected head moved, remove 👀 against the fresh head and target, write
   the non-clean terminal marker, and stop without publishing a result for
   unverified code.

   A required check is genuinely red only when `failed_checks` names its latest
   unambiguous failure, error, cancellation, or timeout. When `failed_checks` is
   non-empty, or `labels` contains the exact `Flaky Test` name, the finishing
   root-cause helper is required. Record which condition triggered it: a
   non-empty `failed_checks` array is the red-check path; only an empty
   `failed_checks` array with the exact label is the label-only path.

   Before constructing the helper, retrieve the forge's check evidence for the
   exact guarded head and target:

   ```sh
   "$MINOS_BIN" forge check-logs HEAD TARGET \
     > "$MINOS_RUN_DIR/check-logs.json"
   ```

   Give the helper that file and direct it to start from the recorded job
   statuses and logs, including any retry or flaky-test lines. The evidence is
   diagnostic input, not proof of a cause: the helper must still reproduce and
   prove its diagnosis. If the forge has no Actions-backed status URL, the
   command returns an empty `runs` array; say so in the helper prompt rather
   than inventing log evidence.

   Write `rootcause.js` as a bare Ensemble workflow that dispatches one agent
   with the vendored root-cause skill on `codex` / `gpt-5.6-sol`, using
   `effort: "high"` and `isolation: "worktree"`. Give the agent a schema that
   returns its diagnosis, a `commit` string and a concise code-only `writeUp`
   explaining what it repaired. Direct it to read and follow
   `$MINOS_ROOT_CAUSE_SKILL/SKILL.md`, diagnose the supplied failure, fix only
   what it proves, commit a repair with the configured Minos identity, return
   that commit and write-up, and never push. An empty `commit` means the isolated
   helper made no mutation to integrate. Invoke the workflow through the bare
   Ensemble launcher and save its result as
   `"$MINOS_RUN_DIR/rootcause-result.json"`.

   When the helper returns a non-empty commit, remember the current reviewed
   head as `FINISHING_REVIEWED_HEAD`, write that one commit as a JSON array and
   integrate it through `"${MINOS_REVIEW_WORKFLOW%/*}/integrate-wave"
   "$MINOS_WORKSPACE" COMMITS_FILE`. Wait for the pushed head through the
   snapshot watcher and use its exact head and target as `FRESH_HEAD` and
   `FRESH_TARGET`. Remove the stale completion reaction with
   `"$MINOS_BIN" forge reaction-remove FRESH_HEAD FRESH_TARGET +1`. Materialise
   the helper's `writeUp` as a review body with an empty comments array and post
   one `comment` review on `FRESH_HEAD`; this is a repair summary, separate from
   the findings review.

   Classify the exact integrated range before deciding whether another whole
   review is needed:

   ```sh
   node "${MINOS_REVIEW_WORKFLOW%/*}/classify-finishing-change.mjs" \
     "$MINOS_WORKSPACE" "$FINISHING_REVIEWED_HEAD" "$FRESH_HEAD" \
     > "$MINOS_RUN_DIR/finishing-change.json"
   ```

   Then run the exact configured build and test commands on the fresh head as
   for any fix. If those pass and the flake is fixed, remove the exact label
   with `"$MINOS_BIN" forge label-remove FRESH_HEAD FRESH_TARGET "Flaky Test"`;
   this guarded command reads the labels back and is safe to repeat.

   A `tests-only` classification means every integrated path is a recognised
   test path. This repair belongs to the final verification cycle and does not
   invalidate the whole-code review: after the configured commands pass, add
   the 👍 on `FRESH_HEAD` and continue to step 9 using that fresh head and
   target. A `review-required` classification means production or unrecognised
   paths changed: set `incomplete`, remove 👀, write the non-clean terminal
   marker, and stop so a later run performs the fresh whole review. An `empty`
   or unreadable classification is an incomplete integration result and stops
   the same way. Any failed integration, publication, build or test also stops
   incomplete; never carry a failing repair to merge.

   When the helper returns no commit and no pushed head appears, it has made no
   mutation. On the red-check path, leave the label when present, set
   `incomplete`, remove 👀, write the non-clean terminal marker, and stop
   exactly as before.
   Only on the label-only path may step 9 receive the unchanged verified head
   and target. Retain the `Flaky Test` label on that path. This clean
   continuation writes nothing to `$MINOS_FAILURE_LOG`. The retained label is
   the durable record that the flake remains unproven. A non-passing
   `check_decision` with no explicit `failed_checks` is incomplete forge state,
   not a root-cause dispatch.
9. If `$MINOS_AUTO_MERGE` is not `true`, the clean run is complete without a
   merge: remove 👀, write the clean terminal marker, and stop. If it is `true`,
   keep using the watcher until the trusted snapshot has `check_decision`
   `pass`, reports both `mergeable` and `can_merge`, and still names the
   verified finishing head and target. Select one of its
   `allowed_merge_methods` and call `"$MINOS_BIN" forge merge HEAD TARGET
   METHOD`. The guarded merge binds the exact head and is idempotent. Then set
   `merged`. For a non-empty, unprotected source branch whose `head_repository`
   equals `target_repository`, call `"$MINOS_BIN" forge delete-source-branch
   HEAD TARGET HEAD_BRANCH`; its merged-pull guard and read-back make repeat
   deletion safe. Never try to delete a fork branch, a protected branch, or a
   virtual pull ref. Finally remove 👀 with `"$MINOS_BIN" forge
   reaction-remove HEAD TARGET eyes`, write the clean terminal marker, and
   stop. Merged, request-changes, clean-without-auto-merge, and every incomplete outcome
   the run reaches end 👀-absent; a crash alone leaves it for the next idempotent
   claim.

Use your judgement. Retry an ordinary transient failure when that is sensible;
otherwise report the actual state, finish all final forge writes and cleanup,
write the non-clean terminal marker, and stop. Never turn a failure into a new
process, checklist, gate, or framework.
