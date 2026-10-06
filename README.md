# Minos

Minos reviews pull requests on Forgejo and GitHub. A webhook or periodic sweep
starts one lead session on a disposable box — Claude Code on `claude-opus-5-5` by
default, or Codex on `gpt-6-sol`. The lead claims the pull request — requesting a
review from the bot account (Forgejo) or adding the configured in-flight marker
(both forges) — clones the repository and invokes the shipped review workflows
through the installed Ensemble CLI and adjudication wrapper. Independent reviewers cover the
whole diff, with breadth scaled to the diff and the repository's `.review/`
briefs honoured (see [brief authoring rules](docs/deployment.md#repository-review-briefs));
by default, the opposite model family verifies every proposed
finding — proposing and planning roles run on Codex (`gpt-6-sol`) while the
verifier runs on Claude (`claude-opus-5-5`), so a finding is checked by the
family that did not propose it. `[routing]` in `service.toml` overrides any
role's engine, model or effort; a single-engine deployment routes every role
to the one provisioned engine. Review workers are dispatched with skills and
agent capabilities stripped, so they cannot spawn sub-agents or reach installed
skills; each worker carries an identity label, and a failed leg names its label
and failure cause in the incomplete result. The adapter checks that every leg
completed and every finding has a complete verifier verdict before returning a
result to the lead. An incomplete run publishes no review and sets no clean
status. The lead classifies the verdict, publishes the result to the forge,
and stops. Minos never builds, tests, fixes, or merges the reviewed change.

The implementation deliberately has no lifecycle database, stage machine,
incident system, preflight framework, model-admission gate, coverage
ledger, review bar or review-of-review loop. The forge is the durable record and
the systemd unit for a pull request is the only live-run coordination. How many
leads may run at once is `runs.max-concurrent` in `service.toml`; run units share a
whole-box memory envelope live through a systemd slice, so a lone run may use
all of it and concurrent runs can never together exceed the envelope. A run that
exits cleanly triggers one sweep pass, so the freed slot is filled without
waiting for the next periodic cycle.

## Commands

```sh
just verify
minos receive --config /etc/minos
minos sweep --config /etc/minos
```

The lead uses `minos forge claim`, `snapshot`, `status`, `review`,
`marker` and `file-issue`.
Forge adaptations live in `scripts/adaptations/`, one directory per forge. The review entry point
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
round. A clean result posts no request-changes review: Minos adds the configured
clean marker and a clean status, and delivers the confirmed advisory findings
to the repository's
configured filing destination — a file in a repository's default branch, an
issue filed on a named repository, a comment on the pull request, or nowhere.
The default is `pull-request-comment`: one comment-review carrying the advisory
entries, posted only when new entries remain after deduplication — a clean run
with nothing new to file posts no comment.
Unverified observations stay in the internal run record. If delivery is
unavailable, the run report records the failure without posting a fallback
comment. Configuration diagnostics — brief misconfigurations and unreadable
guidance sources — take the same destination. A review is grounded on the
per-repository guidance sources configured in `service.toml`; when none are
configured, the first non-empty `AGENTS.md`, `CLAUDE.md` or `README.md` in
the reviewed repository is used (see
[guidance sources](docs/deployment.md#guidance-sources)).

Published review comments carry model provenance: each finding is tagged with
`Proposed by:` and `Verified by:` lines naming the models that proposed and
verified it, and the forge review command appends a `Reviewed by:` line naming
the lead model (`MINOS_LEAD_MODEL`) to the review body. Finding prose carries
only the finding's own anchor location; other model-written file and line
references are stripped before publication.

At the end of each completed pass the sweep writes `.sweep-deferrals.json`
atomically into the runs directory, recording every pull request the pass
deliberately deferred and why — work-in-progress branch prefix, completed
review marker, or unresolved dependency — alongside pull requests that were
suppressed (blocked by the concurrency cap or their own active unit), those
that reached a terminal outcome (clean or attention), and those readied for a
run. When a repository could not be listed, the pass is partial and the
document names the skipped repositories and their reasons. Publication is
best-effort: a failed write logs the error and leaves the previous whole
document in place, so the served record can lag the latest pass. The status
endpoint serves this file as the `sweep` field in its projection.

The lead is guided by [`lifecycle/lifecycle.md`](lifecycle/lifecycle.md),
editable markdown fed to its session as standing launch instructions via
`MINOS_LIFECYCLE_INSTRUCTION`, not a literal installed skill.
