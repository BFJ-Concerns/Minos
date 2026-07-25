# Minos

Minos is a pull-request review-and-fix service. An authenticated webhook or the
periodic sweep starts one trusted Claude Code lead on a disposable
single-tenant box. The lead may clone, build, test, edit, commit and push
without an internal containment layer.

## Product boundary

- Keep one accountable lead from claim through workflow orchestration, repair,
  fresh review and optional merge.
- Keep the review workflow as an Ensemble workflow that the Opus lead invokes
  through the installed launcher and adjudication wrapper. Independent
  reviewers propose findings over the whole pull-request diff and a different
  model family verifies them; the workflow scales its breadth to the diff and
  honours the reviewed repository's `.review/` briefs, reporting a skipped
  concern as skipped, never as passed.
- The workflow's explicit engine dispatch guarantees the opposite-family
  pairing. A leg that does not complete or a finding without a complete
  verifier verdict makes the run incomplete: no review is published and no
  clean status is set. This is a product correctness rule inside the ordinary
  flow, not a procedure check on Minos itself.
- The workflow's reviewers propose findings and its opposite-family verifiers
  judge them. The lead consumes the workflow verdict; it does not duplicate
  either judgement. Do not add machinery that verifies whether Minos followed
  its own procedure.
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
- `node --test workflows/review_test.mjs` drives the review workflow's decision
  logic against synthetic run results and fixture briefs.
- Do not change the deployed Minos box unless the active task explicitly
  includes deployment.
