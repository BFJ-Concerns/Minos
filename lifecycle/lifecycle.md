# Review this pull request

Own this pull request from review to a useful conclusion. You are trusted and
have an entire disposable machine: clone the repository, build it, test it,
edit it, commit, and push as needed. Do not build additional containment,
coordination, evidence, admission, clearance, or self-verification machinery.

The pull request is `$MINOS_OWNER/$MINOS_REPO_NAME#$MINOS_PR`. The observed head
is `$MINOS_HEAD_SHA`; the target is `$MINOS_TARGET_SHA` on `$MINOS_BASE_REF`.
The forge root is `$MINOS_API_BASE`, and `$MINOS_CREDENTIAL_FILE` contains the
token. Work in `$MINOS_RUN_DIR/workspace`.

1. The setup script has cloned the repository at the observed pull-request head
   into `$MINOS_WORKSPACE`, refused setup if that head moved while cloning, and
   recorded its orientation in
   `$MINOS_ORIENTATION`. Read that record. When its `grounding` is `annexe`,
   read the commission in the recorded annexe README as the driving statement
   of the project; when it is `repository`, no sibling annexe exists, so ground
   the work in the repository's own checked-in guidance. Then run
   `"$MINOS_BIN" forge snapshot` and claim the pull request with `"$MINOS_BIN"
   forge claim` (it assigns the Minos account and adds the 👀 reaction; it is
   safe to repeat). If the snapshot now shows that the head or target has moved
   since setup, remove 👀 against that fresh head and target and stop without
   publishing.
2. Publish `working` with `"$MINOS_BIN" forge status HEAD TARGET working`.
3. Read the repository guidance and the complete target-to-head diff. The
   repository's configured build and test commands are already resolved for you
   in `$MINOS_BUILD_CMD` and `$MINOS_TEST_CMD`. When `$MINOS_BUILD_CMD` is
   non-empty, run that exact command string exactly as configured — never a
   repository-native guess or substitute of your own — to completion before any
   review starts; when it is empty, no build command is configured, so skip it
   rather than inventing one. When `$MINOS_TEST_CMD` is non-empty, run that exact
   command string exactly as configured — never a repository-native guess or
   substitute of your own — to completion before any review starts; when it is
   empty, no test command is configured, so skip it rather than inventing one.
4. Build the workflow input from disk with `node
   "${MINOS_REVIEW_WORKFLOW%/*}/review-inputs.mjs" "$MINOS_TARGET_SHA"
   "$MINOS_HEAD_SHA"`. Pass the emitted JSON object as `args` when running the
   Workflow at `$MINOS_REVIEW_WORKFLOW` from the workspace. This enumeration is
   the deterministic file-reading seam: it validates `$MINOS_ORIENTATION`,
   reads the selected annexe commission or repository-fallback guidance, and
   supplies that content together with the shipped role briefs. Do not ask an
   agent to reproduce it or hand-author its `guidance` or `instructionBriefs`
   entries. The workflow binds that project guidance into exploration, every
   specialist and every verifier; it supplies the review plan, Minos's bounded
   specialists and opposite-family verification. Repository `.review/`
   concerns do not run in this loop. Consume the workflow's verdict rather than
   proposing or verifying findings yourself.

   Actual served models exist in the Workflow tool's run record outside the
   script. After the first pass, add that result's `workflowProgress` array
   unchanged at `args.runRecord.workflowProgress`, then resume the same Workflow
   with its `scriptPath` and `resumeFromRunId`. Do not translate model names or
   judge their families yourself. The script reuses completed exact calls and
   checks the run-record models against its pins. Repeat this data hand-off if a
   resumed failed call produced newer progress. If an attempt stalls, cancel it
   before requesting its replacement; never start a parallel replacement.
   Preserve failed attempts in `args.runRecord.attempts`. When the Workflow was
   cancelled or exhausted memory, carry that terminal condition in
   `args.runRecord.terminal` so the result reports `infrastructure-failure`
   rather than an ordinary `incomplete` verdict.

   Only a final workflow result whose `status` is `complete` is publishable. A
   missing result or missing or wrong-family run-record evidence leaves it
   `incomplete`. In either `incomplete` or `infrastructure-failure`, publish no
   review, set `"$MINOS_BIN" forge status HEAD TARGET incomplete`, remove the
   👀 with `"$MINOS_BIN" forge reaction-remove HEAD TARGET eyes`, and stop.
5. Save each complete review result and run
   `"${MINOS_REVIEW_WORKFLOW%/*}/fix-inputs.mjs" REVIEW_RESULT LOOP_RECORD` to
   build the action input. Run the Workflow at
   `"${MINOS_REVIEW_WORKFLOW%/*}/fix.js"`; its returned `runRecord` is the
   run-scoped loop record, including the round count and confirmed-unfixed
   findings. Start with an absent record and replace the scratch record after
   every classification. The configured threshold, cluster cap and optional
   maximum rounds arrive through `$MINOS_REVIEW_THRESHOLD`,
   `$MINOS_FIX_CLUSTER_CAP` and `$MINOS_MAX_ROUNDS`.

   A `working` classification means at least one newly actionable confirmed
   finding is at or above the threshold. Materialise `sweepReview.body` and
   `sweepReview.comments` exactly and call `"$MINOS_BIN" forge review HEAD
   TARGET comment BODY_FILE COMMENTS_FILE`. This posts all confirmed findings
   in the sweep, including those below threshold, as one review with inline
   path/line comments. It is safe to repeat after a crash: the guarded command
   reads the forge review and its comment collection first and does not post an
   exact review twice.

   The fix Workflow clusters the findings by overlapping files up to the
   configured cap and dispatches the clusters. Fix agents use the bootstrapped
   workspace, commit with the configured Minos identity and never push. A
   failed finding receives one retry; after two failures it remains in the
   loop record as confirmed-unfixed and later sweeps do not dispatch it again.
   When `integration.commits` is non-empty, write that array unchanged to a
   file and run `"${MINOS_REVIEW_WORKFLOW%/*}/integrate-wave"
   "$MINOS_WORKSPACE" COMMITS_FILE`. That script verifies every commit's Minos
   author, cherry-picks the wave and performs exactly one push to
   `$MINOS_HEAD_BRANCH`. No fix agent may push.

   After the push, read a fresh forge snapshot and use its current head. When
   `fixReview` is present, materialise its body and comments and post one
   `comment` review on that head; every write-up deliberately uses its original
   finding's path and line. Then run the exact configured build and test
   commands again and run the whole review Workflow on the new head. Do the
   same fresh build, test and review even when every attempted fix failed and
   no commit was integrated. Continue from classification; do not substitute
   your own judgement for the threshold.
6. A `terminal` classification means no finding at or above the threshold
   remains beyond entries already confirmed-unfixed, or the configured maximum
   rounds has been reached. Do not post the terminal sweep's sub-threshold
   findings and do not dispatch fix agents for them. Pass `overflow` to
   `publish-overflow.mjs` with `$MINOS_ORIENTATION`: with an annexe it appends
   each finding once to `ISSUES.md`, commits and pushes the annexe; without an
   annexe it returns the one review body/comments payload that must be posted
   to the pull request, still without fix agents.

   If `requestChangesReview` is present, materialise it exactly, post it with
   `"$MINOS_BIN" forge review HEAD TARGET request-changes BODY_FILE
   COMMENTS_FILE`, and set status `attention`. Otherwise set status `clean`.
   Review prose talks only about the code, never about Minos, its process or a
   round number. Operational conditions — head moved, forge unreadable, review
   not reached — are always carried by the `Minos` status, never by a review.
7. Once the main loop has reached its terminal classification, build the
   repository-brief input from disk with `node
   "${MINOS_REVIEW_WORKFLOW%/*}/review-brief-inputs.mjs"
   "$MINOS_TARGET_SHA" CURRENT_HEAD`. This deterministic
   enumeration supplies every `.review/` Markdown brief and its content, the
   changed paths, and the tracked-file inventory used for full-extent sharding
   and weighted whole-tree limits. Do not hand-author or agent-enumerate these
   values.

   When `hasReviewDirectory` is false, run no brief Workflow and add the clean
   signal immediately with `"$MINOS_BIN" forge reaction CURRENT_HEAD
   "$MINOS_TARGET_SHA" +1`. This guarded write reads the forge reactions before
   and after writing, so it is safe to repeat. It posts no comment.

   When `.review/` exists, run the Workflow at
   `"${MINOS_REVIEW_WORKFLOW%/*}/review-briefs.js"` with the enumerated object as
   `args`. Handle its `workflowProgress`, `scriptPath`, `resumeFromRunId`,
   attempts and terminal conditions exactly like the main review Workflow. Its
   scripted gates apply extent, sweep and occasion; its relevance judgement
   skips a concern only when the diff clearly gives it nothing to do. A skipped
   concern remains `skipped` in the returned run record and is never published
   on the pull request. A `not-run` concern, missing result, or incomplete
   actual-model evidence makes this stage incomplete: publish no brief review,
   add no 👍, set `"$MINOS_BIN" forge status CURRENT_HEAD
   "$MINOS_TARGET_SHA" incomplete`, remove the 👀 with `"$MINOS_BIN" forge
   reaction-remove CURRENT_HEAD "$MINOS_TARGET_SHA" eyes`, and stop.

   On a complete result, materialise `briefReview.body` and
   `briefReview.comments` when `briefReview` is present, then post exactly one
   distinct review group with `"$MINOS_BIN" forge review CURRENT_HEAD
   "$MINOS_TARGET_SHA" comment BODY_FILE COMMENTS_FILE`. The guarded review
   command makes that group idempotent. Do not publish a review when there are
   no confirmed brief findings, and never publish an all-clear comment.

   When `fixRequired` is false, the brief stage has passed: add the 👍 with
   `"$MINOS_BIN" forge reaction CURRENT_HEAD "$MINOS_TARGET_SHA" +1` and
   continue to finishing. When `fixRequired` is true, save the complete brief
   result and run `"${MINOS_REVIEW_WORKFLOW%/*}/fix-inputs.mjs"
   BRIEF_RESULT --single-wave`; run the existing `fix.js` Workflow with that
   input. This mode dispatches every confirmed brief finding exactly once and
   never requests a second brief review or fix wave. If its
   `integration.commits` is non-empty, integrate them through `integrate-wave`
   exactly as in the main loop, then take a fresh forge snapshot and use its
   current head. Run `$MINOS_BUILD_CMD` and `$MINOS_TEST_CMD` exactly as
   configured and to completion when each is non-empty; do not substitute or
   invent commands. If the single wave leaves `confirmedUnfixed` entries, or
   integration, build or tests fail, add no 👍, set attention or incomplete to
   reflect the actual result, remove the 👀, and stop. If all fixes were
   integrated and the exact configured build and tests pass, the brief stage
   has passed: add the 👍 on
   the fresh head. Do not run another review loop. Thus both a no-findings pass
   and a findings-fixed-and-verified pass end in the same idempotent 👍 signal.

8. Enter finishing only after both review stages are done and the current result
   is clean. If the result is attention, remove the 👀 with `"$MINOS_BIN" forge
   reaction-remove CURRENT_HEAD "$MINOS_TARGET_SHA" eyes` and stop instead.

   Save a fresh `"$MINOS_BIN" forge snapshot` JSON object. It is the sole forge
   state used throughout finishing. Run
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
   incomplete terminal outcome: set `incomplete`, remove 👀, and stop.

   Required checks, labels and merge readiness all come from that same trusted
   snapshot. When waiting for checks or forge readiness, write the complete
   snapshot to a file and run `"${MINOS_SETUP_WORKSPACE%/*}/watch-snapshot"
   SNAPSHOT_FILE 600`.
   The watcher repeatedly obtains the same `minos forge snapshot` object used
   everywhere else and wakes only when its `head_sha`, `target_sha`, `statuses`,
   or `labels` differ. Its JSON result contains the exact fresh snapshot to use
   for the next decision. On its roughly ten-minute `timeout`, use the returned
   fresh snapshot as well; do not infer state from elapsed time, poll the forge
   separately, or sleep blind. If the target moved, return to target sync. If
   an unexpected head moved, remove 👀 against the fresh head and target and
   stop without publishing a result for unverified code.

   A required check is genuinely red only when `failed_checks` names its latest
   unambiguous failure, error, cancellation, or timeout. When `failed_checks` is
   non-empty, or `labels` contains the exact `Flaky Test` name, dispatch one
   recorded Claude Code fix session named for root cause, explicitly using the
   installed `root-cause` skill on `anthropic-gpt-5.6-sol` at high effort. Give
   it the failing check details and workspace; it diagnoses, fixes only what it
   proves, commits, and pushes. Wait for its head change through the snapshot
   watcher. On the returned fresh head, run the exact configured build and test
   commands as for any fix. If those pass and the flake is fixed, remove the
   exact label with `"$MINOS_BIN" forge label-remove FRESH_HEAD FRESH_TARGET
   "Flaky Test"`; this guarded command reads the labels back and is safe to
   repeat. The pushed head has not had a fresh whole review, so set `incomplete`,
   remove 👀, and stop; reconciliation starts its fresh attempt. If no pushed
   head appears or verification fails, leave the label, set `incomplete`, remove
   👀, and stop. A non-passing `check_decision` with no explicit
   `failed_checks` is incomplete forge state, not a root-cause dispatch.
9. If `$MINOS_AUTO_MERGE` is not `true`, the clean run is complete without a
   merge: remove 👀 and stop. If it is `true`, keep using the watcher until the
   trusted snapshot has `check_decision` `pass`, reports both `mergeable` and
   `can_merge`, and still names the verified finishing head and target. Select
   one of its `allowed_merge_methods` and call `"$MINOS_BIN" forge merge HEAD
   TARGET METHOD`. The guarded merge binds the exact head and is idempotent.
   Then set `merged`. For a non-empty, unprotected source branch whose
   `head_repository` equals `target_repository`, call `"$MINOS_BIN" forge
   delete-source-branch HEAD TARGET HEAD_BRANCH`; its merged-pull guard and
   read-back make repeat deletion safe. Never try to delete a fork branch, a
   protected branch, or a virtual pull ref. Finally remove 👀 with
   `"$MINOS_BIN" forge reaction-remove HEAD TARGET eyes`. Merged,
   request-changes, clean-without-auto-merge, and every incomplete outcome the
   run reaches end 👀-absent; a crash alone leaves it for the next idempotent
   claim.

Use your judgement. Retry an ordinary transient failure when that is sensible;
otherwise report the actual state and stop. Never turn a failure into a new
process, checklist, gate, or framework.
