# Minos

Minos is a pull-request review service. An authenticated webhook or the
periodic sweep starts a run. The shipped `run-body.env` sets
`MINOS_RUN_DRIVER`, so `run-body` starts the scripted driver, and stops
with a recorded failure when one of the driver's judgement modules is
missing. When the setting is absent, `run-body` starts a resident lead on
Claude Code by default or on Codex (`MINOS_LEAD_ENGINE`).

## Layout

- `cmd/minos` — the service binary.
- `internal/shell/` — the Go service: webhook receiver, sweep, run
  lifecycle, configuration and the `minos forge` commands a run uses.
- `internal/webpage/` — the web page the service serves its operator: the
  one sign-in, the layout and stylesheet, and one Go file and one template
  for each page. A page reads through `shell.StateReader`
  (`internal/shell/state_reader.go`) and changes a setting through the
  configuration writer (`internal/shell/settings_write.go`), and the
  service never imports it.
- `internal/forge/` — the forge protocol: the adapter that runs a forge's
  adaptation scripts and the write guards and markers built on it.
- `internal/product/` — the status vocabulary Minos writes on a pull
  request, the head-and-target record a review body carries and the
  summary comment's record, which binds a state to a head.
- `workflows/` — the Ensemble review workflows, their Node modules and tests.
- `workflows/review-briefs/` — the Markdown prompts the review legs
  receive; its `README.md` maps each file to its stage and slots.
- `workflows/run-driver.mjs` and `workflows/run-steps/` — the scripted run
  driver and its steps, one module each. The driver carries the run from
  claim to close, asking one-shot stages for judgements.
- `workflows/judgements/` — the one-shot judgement stages, each a prompt
  and a definition of one name; its `README.md` says what a stage is, how
  one runs and how one is replayed on a recorded input.
- `workflows/summary-comment.mjs` and `workflows/publication-templates/` —
  the summary comment's text: the named parts it is made of and the
  Markdown template that places them; the directory's `README.md` maps
  each template to its composer and slots.
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
- Both run modes invoke the Ensemble review workflows through the
  installed launcher and adjudication wrapper (`workflows/review.js`, then
  `workflows/review-briefs.js` for the reviewed repository's `.review/`
  briefs). In the main review an exploration leg plans the review and
  concern specialists propose findings over the selected range; a
  repository whose `review.mode` is `quick` gets `workflows/quick-review.js`
  in its place, where general reviewers with no concern propose, divided
  into shares only when the reviewer given the whole change judges it too
  large for one (`workflows/review-mode.mjs`). In the
  brief stage a planner selects the briefs the change gives work to and
  divides them into units, each proposed on by its own leg.
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
- Push inspection reads the change to choose whether to skip review,
  review the increment since the head Minos last finished on, or review
  the whole pull request. The scripted driver calls the push-inspection
  judgement stage and the resident lead follows step 2 of
  `lifecycle/lifecycle.md`. Both choose among the outcomes
  the repository's `push-inspection.outcomes` setting allows
  (`MINOS_PUSH_INSPECTION_OUTCOMES`; all three by default, `full` always
  among them). That earlier head is known by the
  content of the pull request's files, not by its commits (`minos forge
  earlier`), from a record kept under the runs directory when a finished
  summary comment or a clean or attention status is written. The record
  is a cache: without it, `minos forge earlier` takes the last finished
  head the summary comment's record names, and on a pull request with no
  summary comment looks among the newest 20 of the pull request's commits
  for one Minos marked clean by status or requested changes on by review;
  a full review follows when none qualifies. The resident lead and the scripted driver use the same
  brief engagement rules to constrain push inspection.
- The pull request carries only what blocks it. A confirmed finding that
  neither blocks nor takes a class is advisory and goes to the configured
  filing destination, as do unverified observations from outside the
  change where that destination is a `file` or an `issue`. A finding in
  one of the three never-gating classes stays in the run record alone:
  a gap the pull-request description declares as known or out of scope
  (`declared-out-of-scope`),
  a request for a safeguard against something the project is not shown to
  face (`speculative-hardening`), and a restatement of a finding the
  author already has (`already-raised`) (`lifecycle/lifecycle.md`, step 5;
  `workflows/compose-review-publication.mjs`). The one exception is a
  repository that sets `review.file-safeguard-requests`, off by default:
  its `speculative-hardening` findings go to the filing destination as
  safeguard requests, each marked as no defect.
- Classification determines the verdict. The scripted driver calls the
  verdict-classification judgement stage and the resident lead follows
  step 5 of `lifecycle/lifecycle.md`. The configured severity threshold
  informs the judgement. Both modes use the digest and fail-closed
  decision validation in `workflows/verdict-classification.mjs`.
- The forge carries the durable result through the current head's review,
  reactions and `Minos` status. The status is a best-effort write,
  informational only — it never serves as a required check. Scratch data
  may disappear with the run.
- A repository with `description.enabled` on has a description kept of
  it, written by the `repository-description` stage from its default
  branch. The sweep asks of current forge state whether one is due
  (`internal/shell/describe.go`) and starts a `minos-describe-` unit,
  which is no run and takes no run place; a run is handed a copy of the
  kept record, which its legs are shown beside the guidance and never as
  the project's intent.
- A repository with `walkthrough.enabled` on gets a walkthrough of the
  pull request in the summary comment of each head the scripted driver
  reviewed and finished (`workflows/walkthrough.mjs`, the
  `write-walkthrough` closing duty), and sequence diagrams inside it with
  `walkthrough.diagrams` on too. The duty runs after `withdraw`; a fault
  in either stage is an anomaly in the report and never changes how the
  run ended. The service keeps the last one per pull request beside its
  other records and names it in `MINOS_WALKTHROUGH_RECORD`; the resident
  lead writes none.
- Each pull request has one summary comment: a conversation comment the
  service creates at the first claim and edits in place ever after
  (`minos forge summary`, each adaptation's `guarded-write-summary`). Its
  first line is the summary record (`internal/product/summary.go`), which
  names a head and its state — inspecting, reviewing, clean, changes
  requested or incomplete — and carries the last head a run finished on
  while a later one is in hand. The command writes the record; a run names
  a state and composes the text below it.
- Webhooks and the sweep both reconcile current forge state. The
  completion marker is head-bound — the summary record saying clean or
  changes requested of the current head, or, on a head finished by a run
  that writes no summary comment, a terminal Minos review of the head or a
  clean/attention status on it (`headCompletion` in
  `internal/shell/service.go`) — and any head movement is the
  author's, spending the marker. The run writes its own statuses.
  Admission also writes `attention` and stops a continuation that made no
  progress beyond its predecessor (`stopStalledContinuation` in
  `internal/shell/spawn.go`).
  `runs.max-concurrent` caps how many runs are live at once.
- A conversation comment from a user with write access can ask for a
  review, a full review or a skip (`internal/shell/comment_commands.go`).
  Admission reads the commands from the forge on every pass, after the
  rules: a waiting review command spends the head's completion marker and
  narrows the run's `MINOS_PUSH_INSPECTION_OUTCOMES` so it never chooses
  `no-review`, and a newest skip defers the pull request.
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
