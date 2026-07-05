# Pump-19

Agent-native code verification — giving an automated author the most assurance it
can get about code *ahead of execution*. It exists because when an agent writes
code and no human routinely exercises the result, the code is in the position of
safety-critical software: the caution has to live in the toolchain, not in
anyone's memory.

**First deliverable: an automated, independent PR review-and-fix service.** When
anyone opens a pull request in an opted-in repository, cross-engine review agents
judge the change, separate agents fix what they raise, fresh agents re-review, and
the loop runs itself until the remaining findings are not worth another pass. It
runs on any repository, standalone and forge-agnostic. Pump-19 is its own project;
consumers integrate it, never the reverse.

The wider oracle harness — deterministic intent-derived tests, a machine-readable
intent artifact, the missing-seam meta-oracle, the provable (SMT) layer, and the
Foundry agent-tooling side that shares this framework — is the **Direction**, not
first-build scope. Don't build toward it yet.

## The commission is the contract — start there

Planning, intent, design, and the find/issue logs live in the sibling annexe
`../Pump-19-Annexe`; its `README.md` is the **commission** — the single source of
truth for what Pump-19 must do and guarantee, with the full constraints, decisions,
and glossary. Read it before writing code here. This repo is code-only and
currently empty; the first implementation seeds from Widget's
`crates/widget-verification` (a working prototype of the judgement half), and the
build queue lives in the annexe's `TASKS.md`.

## Architecture stance: a small core, everything else an adaptation

The load-bearing design decision — keep these seams real as code lands.

- **A small deterministic dispatch-and-enforce core.** It watches forge and run
  events, evaluates trigger criteria, launches each run in an isolated workspace,
  and enforces the soundness invariants itself. The verification is LLM-driven;
  the control that keeps it sound is not.
- **Everything an organisation varies is an external, versioned adaptation the
  core invokes — never a patch to the core:** prompt *content*, *mechanical*
  scripts/containers the agent merely executes, and the *trigger rules*.
  Adaptations live outside the core release and survive its upgrades.
- **The typed, versioned contract is the real boundary** (findings, patches,
  decisions, run state, model provenance, events). Adaptations build against the
  contract, not core internals; new needs ride its open extension fields.
- **Independent, criteria-triggered runs, not a fixed pipeline.** The
  review→fix→re-review loop is *emergent* — runs know only their own triggers,
  never each other — which is what lets an organisation recompose it.

Resist the two failures this prevents: absorbing adaptation logic into the core,
and letting the core reach around the contract into an adaptation's internals.

## Independence is the soundness basis — enforced by the core

An agent's own tests share its blind spots, so verifiers must be independent of
the author *by construction* and *across model families* (verifiers that share a
blind spot agree confidently and wrongly). The session-level invariants are hard:
fresh agent sessions each pass, fixers disjoint from the reviewers whose findings
they fix, and no agent verifying its own findings. Family-level diversity is
applied as the strongest split the deployment offers and recorded when absent —
graduated and loudly degraded, never refused. Every candidate finding passes
independent per-finding verification before it may post; there is no separate
significance judge. The core establishes model provenance itself — it selects and
launches the engine tools; honour-system independence (a self-reported label, or
counting agents) is unsound, since two agents can wrap one model.

## Trust calibration: hard boundaries are for PR code, not your agents

The service's own agents are trusted workers whose judgement is checked by other
agents — per-finding verification, the review-bar check — not by deterministic
scaffolding that second-guesses them. Watch for over-caution here: individual
agent outputs are fallible, but agents as a class are far more capable and
reliable than the posture that shaped earlier generations of this design, and a
brittle check framework that stalls runs on parsing quirks costs more than the
occasional wrong call it would have caught. When you feel the urge to bolt a
deterministic validator onto an agent's judgement, prefer a second agent's
opinion. Reserve the hard, non-negotiable boundaries for what is actually
untrusted — PR code under review: credential-free workspaces, forge writes only
through the core-authorised credentialed step, review-governing content pinned
to the base ref.

When code here would contradict the commission, that is a commissioning question,
not a local call.
