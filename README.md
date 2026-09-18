# Minos

Minos reviews pull requests. A webhook or periodic sweep starts one Claude Code
lead on `claude-opus-5` on a disposable box. The lead claims the pull request,
clones the repository, and invokes the shipped review workflows through the
installed Ensemble CLI and adjudication wrapper. Independent reviewers cover the
whole diff, with breadth scaled to the diff and the repository's `.review/`
briefs honoured; the opposite model family verifies every proposed finding.
Explicit Ensemble engine dispatch guarantees that pairing. The adapter checks
that every leg completed and every finding has a complete verifier verdict before
returning a result to the lead. An incomplete run publishes no review and sets no
clean status. The lead classifies the verdict, publishes the result to the forge,
and stops. Minos never builds, tests, fixes, or merges the reviewed change.

The implementation deliberately has no lifecycle database, stage machine,
heartbeat, incident system, preflight framework, model-admission gate, coverage
ledger, review bar or review-of-review loop. The forge is the durable record and
the systemd unit for a pull request is the only live-run coordination. How many
leads may run at once is `runs.max-concurrent` in `service.toml`; the run unit's
memory ceiling is one share of a fixed whole-box envelope, so concurrent runs
can never together promise more memory than the box has.

## Commands

```sh
just verify
minos receive --config /etc/minos
minos sweep --config /etc/minos
```

The lead uses `minos forge claim`, `snapshot`, `status` and `review`.
Forge adaptations live in `scripts/adaptations/forgejo`. The review entry point
is `workflows/adjudicated-review`; it runs `workflows/review.js` through the
vendored Ensemble launcher and passes the result to
`workflows/run-record-adjudicator.mjs`. Ahead of the first review,
`workflows/review-scope.js` runs one lightweight engagement gate; its
confident "nothing engages" verdict is itself a complete clean review, so a
pull request that gives no specialist or `.review/` brief any work skips the
full review launch. The workflow decision logic and
adjudication are exercised by `node --test workflows/*_test.mjs`.

A review that requests changes includes all confirmed findings, with Medium
and Low findings labelled advisory so the author can address them in the same
round. A clean result posts no review or comments: Minos adds 👍 and a clean
status, and files the confirmed advisory findings in the project's annexe
`ISSUES.md`. Unverified observations stay in the internal run record. If annexe
filing is unavailable, the run report records the failure without posting a
fallback comment. Brief configuration diagnostics also go to the annexe.

Published review comments carry model provenance: each finding is tagged with
`Proposed by:` and `Verified by:` lines naming the models that proposed and
verified it, and the forge review command appends a `Reviewed by:` line naming
the lead model (`MINOS_LEAD_MODEL`) to every review body and inline comment.

At the end of each completed pass the sweep attempts to write
`.sweep-deferrals.json` atomically into the runs directory, recording the pull
requests the pass deliberately deferred and why — work-in-progress branch
prefix, completed review marker, or unresolved dependency — as a timestamped
document. Publication is best-effort: a failed write logs the error and leaves
the previous whole document (or none, if no earlier pass succeeded) in place,
so the served record can lag the latest pass. The status endpoint serves this
file as the `sweep` field in its projection.

The lead is guided by [`lifecycle/lifecycle.md`](lifecycle/lifecycle.md),
editable markdown fed to its session as standing launch instructions via
`MINOS_LIFECYCLE_INSTRUCTION`, not a literal installed skill.
