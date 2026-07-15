# Minos

Minos is a pull-request review-and-fix service. An authenticated webhook or the
periodic sweep starts one trusted Codex lead on a disposable single-tenant box.
The lead may clone, build, test, edit, commit and push without an internal
containment layer.

## Product boundary

- Keep one accountable lead from orientation through review, repair, fresh
  review and optional merge.
- Keep the Ensemble workflow. Independent reviewers propose findings and a
  different model family verifies them.
- Verify the pull request and proposed findings. Do not add machinery that
  verifies whether Minos followed its own procedure.
- The forge carries the durable result through the current head, review and
  `Minos` status. Scratch data may disappear with the run.
- Webhooks and the sweep both reconcile current forge state. The systemd unit
  name prevents duplicate live runs; a later event starts a fresh attempt.
- The disposable box is the security boundary. Do not add nested containment,
  credential scrubbing, policy gates, model admission, clearance, heartbeats,
  incident ledgers, backoff ladders or lifecycle bookkeeping.

Planning and task records live in `../Minos-Annexe`. Keep its contract aligned
with this boundary rather than treating older procedural text as authority.

## Commands

- `make check` runs formatting, shell syntax, vet, tests and build.
- `go test ./internal/shell -run '<TestName>'` runs a focused shell-package test.
- Do not change the deployed Minos box unless the active task explicitly
  includes deployment.
