# Minos

Minos reviews and repairs pull requests. A webhook or periodic sweep starts one
Claude Code lead on a disposable box. The lead claims the pull request, clones
the repository, runs its build and tests as configured, runs the shipped review
workflow with its own Workflow tool — independent reviewers over the whole
diff, breadth scaled to the diff, the repository's `.review/` briefs honoured,
and every proposed finding verified by the other model family — repairs
confirmed problems, reviews the new head again, and publishes the result to the
forge. A verify leg that cannot be confirmed on its pinned family yields no
verdict and makes the run incomplete: an incomplete run publishes no review and
sets no clean status. The lead may merge when repository policy permits it.

The implementation deliberately has no lifecycle database, stage machine,
heartbeat, incident system, preflight framework, model-admission gate, coverage
ledger, review bar or review-of-review loop. The forge is the durable record and
the systemd unit for a pull request is the only live-run coordination.

## Commands

```sh
make check
minos receive --config /etc/minos
minos sweep --config /etc/minos
```

The lead uses `minos forge claim`, `snapshot`, `status`, `review` and `merge`.
Forge adaptations live in `scripts/adaptations/forgejo`; the lead instruction
is `lifecycle/lifecycle.md`; the review workflow is `workflows/review.js`, with
its decision logic driven by `node --test workflows/review_test.mjs`.
