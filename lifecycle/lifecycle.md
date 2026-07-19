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
   into `$MINOS_WORKSPACE` and recorded its orientation in
   `$MINOS_ORIENTATION`. Read that record. When its `grounding` is `annexe`,
   read the commission in the recorded annexe README as the driving statement
   of the project; when it is `repository`, no sibling annexe exists, so ground
   the work in the repository's own checked-in guidance. Then run
   `"$MINOS_BIN" forge snapshot` and claim the pull request with `"$MINOS_BIN"
   forge claim` (it assigns the Minos account and adds the 👀 reaction; it is
   safe to repeat). Stop without publishing if the head or target has moved.
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
4. Run the review workflow at `$MINOS_REVIEW_WORKFLOW` with your Workflow tool,
   passing `args` of `{"target": "$MINOS_TARGET_SHA", "head": "$MINOS_HEAD_SHA"}`,
   from the workspace. It supplies independent reviewers, the repository's
   `.review/` briefs, and cross-family verification; use its findings rather
   than recreating its review by hand. The result's `verdictsComplete` reflects
   only what the script could see from inside the run — each leg's own
   `selfReportedModel`, which is corroboration, not proof. Cross-family
   independence binds **both** legs behind a finding: the reviewer that proposed
   it and the verifier that checked it must have actually run on different
   families. The Workflow tool silently substitutes the GPT session model when a
   pinned id cannot be served, so before you trust any verdict, confirm in the
   Workflow run/progress record the actual model of both legs — the review leg
   (`review.{label,configuredFamily,pinnedModel}`) and its verify leg
   (`verify.{label,expectedFamily,pinnedModel}`). Treat a finding's verdict as
   complete only when both legs' actual families are confirmed and differ: a
   review leg that fell back to the GPT session, a verify leg on the wrong
   family, or either with no result, is **no verdict**. A `.review/` brief the
   result reports as `not-run` is a concern left unevaluated. The run is
   complete only when `verdictsComplete` is true **and** that actual-model check
   passes for every review and verify leg — until both hold, treat the run as
   incomplete whatever the result says.
5. Fix confirmed material problems you can fix. Commit and push the repair to
   the pull-request branch, then repeat the build, tests, and the whole review
   workflow on the new head. Loop until a fresh review confirms nothing further
   you can fix.
6. Publish at most one review per run, on the final reviewed head, and only
   from a complete run: request-changes listing the confirmed problems that
   remain (`"$MINOS_BIN" forge review HEAD TARGET request-changes REVIEW_FILE`,
   then status `attention`), approve noting what was found and fixed, or a
   short plain approval when nothing was found (`approve`, then status
   `clean`). The review talks about the code, never about Minos or its
   process. If the run is incomplete, publish no review and set no clean
   status; report the condition through the status description instead.
   Operational conditions — head moved, forge unreadable, review not reached —
   are always carried by the `Minos` status, never by a review.
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
