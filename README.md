# Minos

Minos reviews and repairs pull requests. A webhook or periodic sweep starts one
Claude Code lead on `claude-opus-4-8` on a disposable box. The lead claims the
pull request, clones the repository, runs its build and tests as configured, and
invokes the shipped review workflows through the installed Ensemble CLI and
adjudication wrapper. Independent reviewers cover the whole diff, with breadth
scaled to the diff and the repository's `.review/` briefs honoured; the opposite
model family verifies every proposed finding. The adapter checks each leg's
actual served-model evidence from the Ensemble run record before returning a
verdict to the lead. A verification leg that cannot be confirmed on its pinned
family yields no verdict and makes the run incomplete: an incomplete run
publishes no review and sets no clean status. The lead repairs confirmed
problems, reviews the new head again, publishes the result to the forge, and may
merge when repository policy permits it.

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
Forge adaptations live in `scripts/adaptations/forgejo`. The review entry point
is `workflows/adjudicated-review`; it runs `workflows/review.js` through the
vendored Ensemble launcher and passes the result to
`workflows/run-record-adjudicator.mjs`. The workflow decision logic and
adjudication are exercised by `node --test workflows/*_test.mjs`.

The lead is guided by [`lifecycle/lifecycle.md`](lifecycle/lifecycle.md),
editable markdown fed to its session as standing launch instructions via
`MINOS_LIFECYCLE_INSTRUCTION`, not a literal installed skill.
