# Minos

Minos reviews and repairs pull requests. A webhook or periodic sweep starts one
Codex lead on a disposable box. The lead clones the repository, runs its build
and tests, uses Ensemble for independent review and cross-family verification,
repairs confirmed problems, reviews the new head again, and publishes the
result to the forge. It may merge when repository policy permits it.

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

The lead uses `minos forge snapshot`, `status`, `review` and `merge`. Forge
adaptations live in `scripts/adaptations/forgejo`; the lead instruction is
`lifecycle/lifecycle.md`; the retained multi-agent review is
`workflows/review.js`.
