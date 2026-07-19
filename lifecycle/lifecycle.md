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
   safe to repeat). Stop without publishing if the snapshot now shows that the
   head or target has moved since setup.
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
   specialist and every verifier; it supplies the review plan, bounded
   specialists, repository `.review/` concerns and opposite-family
   verification. Consume its verdict rather than proposing or verifying
   findings yourself.

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
   `.review/` concern reported as `not-run`, a missing result, or missing or
   wrong-family run-record evidence leaves it `incomplete`. In either
   `incomplete` or `infrastructure-failure`, publish no review and set
   `"$MINOS_BIN" forge status HEAD TARGET incomplete`.
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
7. If required checks are still red as you finish, or the pull request carries
   the `Flaky Tests` label, dispatch a root-cause agent: a Claude Code session
   with the `root-cause` skill on `gpt-5.6-sol` at high effort, to diagnose,
   fix what it proves, and push. Its push moves the head, so this run does not
   merge; the new head gets its own fresh attempt.
8. If `$MINOS_AUTO_MERGE` is `true`, the run is complete, the pull request is
   clean, required checks pass, and the forge reports it mergeable, use an
   allowed method with `"$MINOS_BIN" forge merge HEAD TARGET METHOD`, then set
   `merged`. Never merge a head that moved after your review.

Use your judgement. Retry an ordinary transient failure when that is sensible;
otherwise report the actual state and stop. Never turn a failure into a new
process, checklist, gate, or framework.
