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
blind spot agree confidently and wrongly). The core establishes and checks this at
every launch rather than trusting configuration: at least two distinct model
families across reviewers, reviewers disjoint from fixers, the significance judge
independent of the reviewers, and fresh agents each pass. It establishes model
provenance itself and fails closed when it cannot verify it — honour-system
independence (a self-reported label, or counting agents) is unsound, since two
agents can wrap one model.

When code here would contradict the commission, that is a commissioning question,
not a local call.
