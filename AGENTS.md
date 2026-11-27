# Minos

Minos is a pull-request review service. An authenticated webhook or the
periodic sweep starts one trusted lead, on Claude Code by default or on
Codex (`MINOS_LEAD_ENGINE`). The lead may clone and read without an
internal containment layer; it reviews and publishes to the forge, and
nothing else.

## Layout

- `cmd/minos` — the service binary.
- `internal/shell/` — the Go service: webhook receiver, sweep, run
  lifecycle, configuration and the `minos forge` commands the lead runs.
- `internal/forge/` — the forge protocol: the adapter that runs a forge's
  adaptation scripts and the write guards and markers built on it.
- `internal/product/` — the status vocabulary Minos writes on a pull
  request and the head-and-target record a review body carries.
- `workflows/` — the Ensemble review workflows, the Node modules the lead
  runs around them, and their Node tests.
- `workflows/review-briefs/` — the Markdown prompts the review legs
  receive; its `README.md` maps each file to its stage and slots.
- `workflows/run-driver.mjs` and `workflows/run-steps/` — the scripted run
  driver and its steps, one module each. It judges nothing: a step that
  needs a judgement asks a judgement stage. `run-body` starts the lead and
  nothing starts the driver, whose steps are unbuilt but for push
  inspection.
- `workflows/judgements/` — the one-shot judgement stages, each a prompt
  and a definition of one name; its `README.md` says what a stage is, how
  one runs and how one is replayed on a recorded input.
- `runtime/` — the vendored Ensemble runtime (below).
- `scripts/adaptations/` — one directory of forge scripts per forge
  (`forgejo`, `github`).
- `scripts/` — provisioning, the runtime installer and the on-box per-run
  driver (`run-body/`).
- `lifecycle/lifecycle.md` — the lead's own run prompt.
- `deploy/` — the shipped systemd units and configuration.

## Product boundary

- Minos reviews; it does not repair and it does not merge. A run never
  builds or tests the reviewed change, never syncs the branch with its
  target, never writes to the pull-request branch, and dispatches no agent
  to modify the reviewed code. Fixing and landing belong to the author,
  and the forge's own CI is the build evidence.
- Keep one accountable lead from claim through the review workflow, the
  `.review/` briefs stage, verdict classification and publication.
- Keep the review as Ensemble workflows the lead invokes through the
  installed launcher and adjudication wrapper (`workflows/review.js`, then
  `workflows/review-briefs.js` for the reviewed repository's `.review/`
  briefs). In the main review an exploration leg plans the review and
  concern specialists propose findings over the whole range the lead
  chose; in the brief stage a planner selects the briefs the change gives
  work to and divides them into units, each proposed on by its own leg.
  Both then merge, where one leg groups the findings one fix would
  resolve, and verify, where verifier legs give each merged finding one
  verdict — `confirmed`, `refuted` or `unsettled`. Only a confirmed
  finding is a defect; a refuted one reaches no surface; an unsettled one
  is never a defect and is filed as an open question to a `file` or
  `issue` destination only, the pull-request-comment and `none` kinds
  dropping it (`workflows/finding-disposition.mjs`,
  `workflows/filing-destination.mjs`). The brief stage reports a skipped
  concern as skipped, never as passed.
- The workflow dispatches every leg on the engine its role's routing
  names, and a specialist on its concern's where `[routing.concerns]` sets
  one (`workflows/concern-routing.mjs`). With both engines provisioned, the
  default routing (`workflows/role-routing.mjs`) proposes on Codex and
  verifies on Claude, so a finding is verified by the family that did not
  propose it, a verifier's own new finding and a finding from a concern
  routed to the verifier's engine included. `[routing]` can put the
  proposer and verifier on one engine, and then that pairing no longer
  holds. A repository's own `[routing]` replaces each role or concern it
  sets. A leg
  that does not complete or a finding without a complete verifier verdict
  makes the run incomplete: no review is published and no clean status is
  set.
- The lead sizes each review by reading the change: nothing, the increment
  since the head Minos last finished on, or the whole pull request
  (`lifecycle/lifecycle.md`, step 2). That earlier head is known by the
  content of the pull request's files, not by its commits (`minos forge
  earlier`), from a record kept under the runs directory. The record is a
  cache: without it, `minos forge earlier` looks among the newest 20 of
  the pull request's commits for one Minos marked clean by status or
  requested changes on by review, and a full review follows when none
  qualifies. The lifecycle dispatches no scope workflow;
  `workflows/review-scope.js` ships unused.
- The pull request carries only what blocks it. A confirmed finding that
  neither blocks nor takes a class is advisory and goes to the configured
  filing destination, as do unverified observations from outside the
  change where that destination is a `file` or an `issue`. A finding the
  lead puts in one of the three never-gating classes is published and
  filed nowhere, and stays in the run record: a gap the pull-request
  description declares as known or out of scope (`declared-out-of-scope`),
  a request for a safeguard against something the project is not shown to
  face (`speculative-hardening`), and a restatement of a finding the
  author already has (`already-raised`) (`lifecycle/lifecycle.md`, step 5;
  `workflows/compose-review-publication.mjs`).
- The verdict is the lead's classification judgement, informed by the
  configured severity threshold and never mechanically bound to it
  (`workflows/verdict-classification.mjs` owns the digest and the
  fail-closed decision validation).
- The forge carries the durable result through the current head's review,
  reactions and `Minos` status. The status is a best-effort write,
  informational only — it never serves as a required check. Scratch data
  may disappear with the run.
- Webhooks and the sweep both reconcile current forge state. The
  completion marker is head-bound — a terminal Minos review of the current
  head, or a clean/attention status on it — and any head movement is the
  author's, spending the marker. Every status is the lead's but one:
  admission, from a webhook or the sweep, writes `attention` when a
  continuation made no progress beyond its predecessor, and stops it
  (`internal/shell/spawn.go`, `stopStalledContinuation`).
  `runs.max-concurrent` caps how many runs are live at once.
- Minos carries no sandbox of its own: whatever isolates the deployment is
  the security boundary, and the docs recommend a dedicated container or
  VM without requiring one. Run-scoped containment layers, policy gates
  and health bookkeeping stay out by design. The continuation handoff,
  with the progress record that lets admission stop a stalled
  continuation, is the run-lifecycle state the service keeps.

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
