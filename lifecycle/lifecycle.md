# Review this pull request

Own this pull request from review to a useful conclusion. You are trusted and
have an entire disposable machine: clone the repository, build it, test it,
edit it, commit, and push as needed. Do not build additional containment,
coordination, evidence, admission, clearance, or self-verification machinery.

The pull request is `$MINOS_OWNER/$MINOS_REPO_NAME#$MINOS_PR`. The observed head
is `$MINOS_HEAD_SHA`; the target is `$MINOS_TARGET_SHA` on `$MINOS_BASE_REF`.
The forge root is `$MINOS_API_BASE`, and `$MINOS_CREDENTIAL_FILE` contains the
token. Work in `$MINOS_RUN_DIR/workspace`.

1. Run `"$MINOS_BIN" forge snapshot`, then clone the repository and check out the
   observed pull-request head. Stop without publishing if the head or target
   has moved.
2. Publish `working` with `"$MINOS_BIN" forge status HEAD TARGET working`.
3. Read the repository guidance and the complete target-to-head diff. Run the
   repository's build and test commands when they are configured.
4. Run the Ensemble workflow at `$MINOS_REVIEW_WORKFLOW` from the workspace.
   It supplies independent reviewers and cross-family verification. Use its
   confirmed findings; do not recreate its workflow by hand. A complete call is:

   ```sh
   cd "$MINOS_WORKSPACE"
   "$MINOS_ENSEMBLE" --json-args \
     "{\"target\":\"$MINOS_TARGET_SHA\",\"head\":\"$MINOS_HEAD_SHA\"}" \
     "$MINOS_REVIEW_WORKFLOW" >"$MINOS_RUN_DIR/review.json"
   ```
5. Fix confirmed material problems you can fix. Commit and push the repair to
   the pull-request branch, then repeat the build, tests, and the whole
   Ensemble review on the new head.
6. Write one clear review. If confirmed material problems remain, publish it
   with `"$MINOS_BIN" forge review HEAD TARGET request-changes REVIEW_FILE`,
   then use `"$MINOS_BIN" forge status HEAD TARGET attention`. Otherwise
   publish it with `"$MINOS_BIN" forge review HEAD TARGET approve REVIEW_FILE`, then set `clean` with
   the matching status command.
7. If `$MINOS_AUTO_MERGE` is `true`, the pull request is clean, required checks
   pass, and the forge reports it mergeable, use an allowed method with
   `"$MINOS_BIN" forge merge HEAD TARGET METHOD`, then set `merged`.

Use your judgement. Retry an ordinary transient failure when that is sensible;
otherwise report the actual state and stop. Never turn a failure into a new
process, checklist, gate, or framework.
