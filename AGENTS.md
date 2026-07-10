# Pump-19

Agent-native code verification — giving an automated author the most assurance it
can get about code *ahead of execution*. It exists because when an agent writes
code and no human routinely exercises the result, the code is in the position of
safety-critical software: the caution has to live in the toolchain, not in
anyone's memory.

**First deliverable: an automated, independent PR review-and-fix service.** When
anyone opens a pull request in an opted-in repository, review agents judge the
change to a strong reviewer's standard, every candidate finding is independently
verified before it may post, separate agents fix what survives, fresh agents
re-review, and the loop runs itself to convergence — every step visible on the PR
itself. It runs on any repository, standalone and forge-agnostic. Pump-19 is its
own project; consumers integrate it, never the reverse.

The wider oracle harness — deterministic intent-derived tests, a machine-readable
intent artifact, the missing-seam meta-oracle, the provable (SMT) layer — is the
**Direction**, not first-build scope. Don't build toward it yet.

## The commission is the contract — start there

Planning, intent, design, and the find/issue logs live in the sibling annexe
`../Pump-19-Annexe`; its `README.md` is the **commission** — the single source of
truth for what Pump-19 must do and guarantee, with the full constraints,
decisions, and glossary. Read it before writing code here. It was corrected
2026-07-08 and again 2026-07-10 within one containment episode; while a root
`CONTAINMENT.md` is present
in this repo it governs what may be touched — read it first. The pre-containment
codebase is quarantined evidence, not a base to extend: nothing is carried
forward by default, and a component earns salvage only by written justification
against the corrected commission. The build queue lives in the annexe's
`TASKS.md`.

## Architecture stance: hooks and labels — the forge already provides the machinery

The load-bearing design decision, operator-dictated after the first build failed.

- **Events come from the forge, full stop.** Webhooks hit small, stateless
  receivers: millisecond criteria check, spawn the run detached, return. A cron
  reconciliation sweep — judging crashes by run-log liveness against a generous
  threshold — is the entire crash-recovery story and the system's only hard
  clock. No daemon, no internal event stream, no recovery machinery.
- **There is no persistence — the PR is the state.** Labels carry the loop's
  stage, commit statuses carry machine-checkable outcomes per head SHA, and the
  posted reviews carry the substance; any crashed component re-derives everything
  from the PR. Idempotency keys on (PR, head SHA, run label).
- **Each run is one accountable agent session**: repo checked out, diff and
  skill in hand, it does the work, posts its own output, manages its labels, and
  exits. Multi-agent fan-out happens through Ensemble invoked by the lead agent
  as a tool — never executed unattended by infrastructure. No arbitrary run
  timeouts: pacing belongs to the lead agent.
- **Prose for humans, markers for machines.** Reviews post as ordinary review
  comments; where a machine needs a decision it reads a label, a commit status,
  or a single trailing marker line — never a schema-validated document.
- **Agent logic composes from the Foundry's *general* skill library** — an
  ordinary agent session equipped with general skills plus a thin,
  service-owned launch instruction; Pump-19-specific text is thin
  service-side adaptation, never Foundry canon. (The second break, corrected
  2026-07-10, misread this as authoring a bespoke Pump-19 skill stack in the
  Foundry.) Everything an organisation varies is an external adaptation:
  run instructions and prompt text, mechanical steps as scripts, trigger
  rules as receiver configuration. The guarantee-carrying judgement gates
  ride the pinned general `agent-review` skill; the mechanical gates ride
  service-owned scripts — organisations extend either, replace neither.

Resist the failure the last build died of: re-implementing what the forge
already provides (state stores, event streams, resident watchers), and caging
the agents in deterministic machinery (timeouts, schema repair, unattended
workflow execution). The quarantined code did both — see `CONTAINMENT.md`.

## Independence is the soundness basis

An agent's own tests share its blind spots, so verifiers are independent of the
author by construction. Every run is a fresh session by construction of the
spawn; fix runs are separate sessions from the reviews whose findings they fix;
no agent verifies its own findings — within a run, role assignment and verifier
disjointness are pinned by the versioned Ensemble workflows the lead invokes.
Family-level diversity is applied as the strongest split the deployment offers,
recorded and loudly degraded when absent — never refused. Every role names an
explicit, current-generation pinned model; each run's posted output records the
engine-resolved model that actually served, and a pin mismatch is a loud
failure, never a shrug.

## Trust calibration: hard boundaries are for PR code, not your agents

The service's own agents are trusted workers whose judgement is checked by other
agents — per-finding verification, the review-bar check — never by deterministic
scaffolding that second-guesses them. When you feel the urge to bolt a validator
onto an agent's judgement, prefer a second agent's opinion. Reserve the hard
boundaries for what is actually untrusted — the PR code under review: it
executes only inside the run's credential-free temp-directory workspace, with
the service's dedicated hard-capped credentials (forge and model-backend)
scrubbed from workspace commands as hygiene. The real containment is bounded
damage, stated honestly: capped dedicated credentials on a disposable,
disposable box. No nested containers — the box is the container,
by operator decision.

When code here would contradict the commission, that is a commissioning
question, not a local call.
