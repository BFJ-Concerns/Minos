# Minos

Minos is a pull-request review service. An authenticated webhook or the
periodic sweep starts one trusted Claude Code lead on a disposable
single-tenant box. The lead may clone and read without an internal
containment layer; it reviews and publishes to the forge, and nothing else.

## Layout

- `cmd/minos` — the service binary.
- `internal/shell/` — the Go service: webhook receiver, sweep, and run
  lifecycle.
- `workflows/` — the Ensemble review workflow and its Node tests.
- `scripts/` — provisioning and the on-box per-run driver (`run-body/`).
- `lifecycle/lifecycle.md` — the lead's own run prompt.
- `deploy/` — the shipped systemd units and configuration.

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
  clean status is set.
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
  spending the marker. The sweep never writes statuses itself, and
  `runs.max-concurrent` caps how many runs are live at once.
- The disposable box is the security boundary; run-scoped machinery inside
  it — containment layers, policy gates, health and lifecycle bookkeeping —
  was deliberately removed and stays out.

## The vendored Ensemble runtime

`runtime/ensemble.mjs` is a vendored copy of the `ensemble-workflow`
skill's bundle, pinned by `runtime/ensemble.mjs.sha256` and described by
`runtime/ensemble.source-version`. It tracks that bundle: refresh it when
it drifts rather than letting it age, because the review workflow runs on
whatever is vendored here, not on whatever the skill ships.

A refresh moves every pin with the bundle — the checksum file, the
source-version, and the installed-digest pin in
`internal/shell/run_body_test.go` — and preserves the file's executable
mode, which refreshes have lost before. The repository gate is the check
that matters: the runtime load-checks each
workflow's free identifiers against Node globals plus the hook bindings,
and the divergence gate
(`workflows/runtime_bundle_compatibility_test.mjs`) drives the shipped
workflow shapes through the installed bundle's own validator surface, so a
bundle that renamed an agent option or changed the accepted schema shapes
turns it red.

## Commands

- `just verify` runs formatting, shell syntax, vet, tests and build. It is
  the gate: CI runs this same recipe, so a green run here means CI's checks
  passed.
- `go test ./internal/shell -run '<TestName>'` runs a focused shell-package
  test.
- `node --test workflows/review_test.mjs` drives the review workflow's
  decision logic against synthetic run results and fixture briefs.
