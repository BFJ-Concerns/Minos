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
  name prevents duplicate live runs of one pull request, and
  `runs.max-concurrent` caps how many run at once; a later event starts a fresh
  attempt. The run unit's memory ceiling is that cap's share of a fixed
  whole-box envelope.
- The disposable box is the security boundary. Do not add nested containment,
  credential scrubbing, policy gates, model admission, clearance, heartbeats,
  incident ledgers, backoff ladders or lifecycle bookkeeping.

Planning and task records live in `../Minos-Annexe`. Keep its contract aligned
with this boundary rather than treating older procedural text as authority.

## The vendored Ensemble runtime

`runtime/ensemble.mjs` is a vendored copy of the `ensemble-workflow` skill's
bundle, pinned by `runtime/ensemble.mjs.sha256` and described by
`runtime/ensemble.source-version`. It tracks that bundle: refresh it when it
drifts rather than letting it age, because the review workflow runs on
whatever is vendored here, not on whatever the skill ships.

A refresh moves every pin with the bundle — the checksum file, the
source-version, and the installed-digest pin in
`internal/shell/run_body_test.go` — and preserves the file's executable
mode, which two earlier refreshes each had to repair afterwards. The
repository gate is the check that matters: the vm-context gate scrapes the
vendored launcher's `createSandboxContext` for its binding set and the
divergence gate drives the shipped workflow shapes through the runtime's
own validator, so a bundle that renamed an agent option or changed the
sandbox bindings turns them red.

## Memory accounting on the box

Judge memory by anonymous memory plus swap and by pressure, never by
`memory.current`, journal "memory peak" lines, or `free`'s used column: those
count page cache, which a busy build inflates to whatever ceiling exists and
the kernel reclaims the instant anything needs the space. A runs slice
reading 17G can be 2G of genuine demand. Swap likewise fills with cold pages
that are rarely read back; a full swap is not itself harm. The honest
signals are `memory.stat`'s `anon` plus `memory.swap.current` for footprint,
`memory.pressure` (PSI stall time, which cache cannot inflate) for harm being
experienced now, and `workingset_refault_anon` for genuine thrash. run-body's
wind-down sampler already judges on anon plus swap for this reason; keep any
new memory judgement on the same basis.

## Commands

- `just verify` runs formatting, shell syntax, vet, tests and build. It is the
  gate: CI runs this same recipe, so a green run here means CI's checks passed.
- `go test ./internal/shell -run '<TestName>'` runs a focused shell-package test.
- `node --test workflows/review_test.mjs` drives the review workflow's decision
  logic against synthetic run results and fixture briefs.
