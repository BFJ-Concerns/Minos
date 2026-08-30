# Minos

Minos is a pull-request review service. An authenticated webhook or the
periodic sweep starts one trusted Claude Code lead on a disposable
single-tenant box. The lead may clone and read without an internal
containment layer; it reviews and publishes to the forge, and nothing else.

## Product boundary

- Minos reviews; it does not repair and it does not merge. A run never
  builds or tests the reviewed change, never syncs the branch with its
  target, never writes to the pull-request branch, and dispatches no agent
  to modify the reviewed code. Fixing and landing belong to the author, and
  the forge's own CI is the build evidence.
- Keep one accountable lead from claim through the review workflow, the
  `.review/` briefs stage, verdict classification and publication.
- Keep the review workflow as an Ensemble workflow the Opus lead invokes
  through the installed launcher and adjudication wrapper. Independent
  reviewers propose findings over the whole pull-request diff and the
  opposite model family verifies them; the workflow honours the reviewed
  repository's `.review/` briefs, reporting a skipped concern as skipped,
  never as passed.
- The workflow's explicit engine dispatch guarantees the opposite-family
  pairing. A leg that does not complete or a finding without a complete
  verifier verdict makes the run incomplete: no review is published and no
  clean status is set. This is a product correctness rule inside the
  ordinary flow, not a procedure check on Minos itself.
- The verdict is the lead's classification judgement, informed by the
  configured severity threshold and never mechanically bound to it
  (`workflows/verdict-classification.mjs` owns the digest and the
  fail-closed decision validation).
- The forge carries the durable result through the current head's review,
  reactions and `Minos` status. The status is the lead's own best-effort
  write, informational only — it never serves as a required check. Scratch
  data may disappear with the run.
- Webhooks and the sweep both reconcile current forge state. The completion
  marker is head-bound — a terminal Minos review of the current head, or a
  clean/attention status on it — and any head movement is the author's,
  spending the marker. The sweep never writes statuses itself. The systemd
  unit name prevents duplicate live runs of one pull request, and
  `runs.max-concurrent` caps how many run at once inside a fixed
  whole-box memory envelope.
- The disposable box is the security boundary. Do not add nested
  containment, credential scrubbing, policy gates, model admission,
  clearance, heartbeats, incident ledgers, backoff ladders or lifecycle
  bookkeeping.

Planning and task records live in `../Minos-Annexe`. Keep its contract
aligned with this boundary rather than treating older procedural text as
authority.

## The vendored Ensemble runtime

`runtime/ensemble.mjs` is a vendored copy of the `ensemble-workflow`
skill's bundle, pinned by `runtime/ensemble.mjs.sha256` and described by
`runtime/ensemble.source-version`. It tracks that bundle: refresh it when
it drifts rather than letting it age, because the review workflow runs on
whatever is vendored here, not on whatever the skill ships.

A refresh moves every pin with the bundle — the checksum file, the
source-version, and the installed-digest pin in
`internal/shell/run_body_test.go` — and preserves the file's executable
mode, which two earlier refreshes each had to repair afterwards. The
repository gate is the check that matters: the runtime load-checks each
workflow's free identifiers against Node globals plus the hook bindings,
and the divergence gate
(`workflows/runtime_bundle_compatibility_test.mjs`) drives the shipped
workflow shapes through the installed bundle's own validator surface, so a
bundle that renamed an agent option or changed the accepted schema shapes
turns it red.

## Memory accounting on the box

Judge memory by anonymous memory plus swap and by pressure, never by
`memory.current`, journal "memory peak" lines, or `free`'s used column:
those count page cache, which the kernel reclaims the instant anything
needs the space. Swap likewise fills with cold pages that are rarely read
back; a full swap is not itself harm. The honest signals are
`memory.stat`'s `anon` plus `memory.swap.current` for footprint,
`memory.pressure` (PSI stall time, which cache cannot inflate) for harm
being experienced now, and `workingset_refault_anon` for genuine thrash.
run-body's pressure watch judges on anon plus swap for this reason; keep
any new memory judgement on the same basis.

## Commands

- `just verify` runs formatting, shell syntax, vet, tests and build. It is
  the gate: CI runs this same recipe, so a green run here means CI's checks
  passed.
- `go test ./internal/shell -run '<TestName>'` runs a focused shell-package
  test.
- `node --test workflows/review_test.mjs` drives the review workflow's
  decision logic against synthetic run results and fixture briefs.
