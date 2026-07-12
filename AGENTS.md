# Minos

Minos is an agent-native code-verification harness. Its first deliverable is an
automated, independent pull-request review-and-fix service: one accountable lead
session attempts the whole lifecycle from deep review through independently
verified findings, repair, fresh re-review, final integration clearance, and an
optional guarded merge. The broader deterministic and provable oracle harness is
Direction, not first-build scope.

Minos is standalone and forge-agnostic. Consumers integrate it; Minos never
depends on them.

## The commission is the contract

Planning, intent, design, and the issue and task logs live in the sibling annexe
`../Minos-Annexe`. Read its `README.md` before writing code: it is the single
source of truth for what Minos must do and guarantee. `TASKS.md` carries the
build queue.

The commission supersedes historical designs retained in the annexe's
`containment/` directory and in git history. If a root `CONTAINMENT.md` is
present, read it before touching anything it names; it records the active
freeze and quarantine boundaries. When code would contradict the commission,
raise a commissioning question rather than making a local design ruling.

## Architecture stance

- **One lifecycle, one accountable lead.** A fresh lead-agent session owns the
  current PR journey. Receiver and sweep processes admit work and reconcile
  current forge facts; they do not run an infrastructure stage machine. A dead
  or waiting attempt is replaced by a fresh session which re-derives the state
  of the PR.
- **The coordination ledger is deliberate but narrow.** Deployment-local
  SQLite holds fenced ownership, heartbeat and liveness, waiting and admission,
  incident identity, attempt/backoff, attempt-local integration clearance, and
  guarded post-merge cleanup. It is sanctioned coordination state beside the
  forge, not a product database or a copy of PR history.
- **The forge carries the product record.** Minos owns one `Minos` commit status
  per head and posts ordinary substantive reviews and findings. An eyes reaction
  marks the live lease. Minos neither creates nor reads PR labels, and labels or
  mutable comments never grant authority.
- **Forge events are prompts to reconcile.** Webhooks and the periodic sweep
  call the same current-state reconciliation. The sweep's liveness threshold is
  the only hard clock; there are no arbitrary lifecycle timeouts or resumable
  internal instruction pointers.
- **The lead keeps context lean without weakening coverage.** Full worker
  artefacts, logs, diffs, and coverage records live in attempt-local scratch.
  The lead carries a compact lifecycle index and current decisions. Fresh
  workers still perform the bounded full-file and whole-diff reads required for
  sound review.
- **Agents orchestrate agents.** The lead invokes Ensemble as a tool for fresh,
  independent workers. Infrastructure never executes Ensemble as an unattended
  judgement workflow.
- **Agent logic composes from general Foundry skills.** Minos supplies thin
  service-owned lifecycle instructions, mechanical scripts, and deployment or
  organisation adaptations. Minos-specific logic does not become a parallel
  Foundry skill estate, and organisations extend the guarantee-carrying gates
  rather than replacing them.

The current tree is part-way through the commissioned rebuild and still contains
the retired review/fix/finish/flaky stage machine. Treat that code as transitional,
not as architectural authority. Preserve it mechanically unless a commissioned
unit explicitly replaces it; do not extend it as a pattern for new work.

## Independence is the soundness basis

An agent's own tests and review share its blind spots. Reviewers never verify
their own findings, repair authors never verify their own repairs, and every
repaired head receives a fresh whole-PR review. Use the strongest cross-family
split the deployment offers; when diversity is unavailable, continue only in
the explicitly permitted degraded mode and record the limitation loudly.

Every guarantee-bearing role names an explicit current-generation model. The
lead and worker model pins are checked against the engine-resolved models, and a
mismatch is inadmissible. Model provenance belongs in deployment evidence, not
in reviews, comments, statuses, or commits.

## Trust and containment

Minos's own agents and first-party adaptations are trusted workers whose
judgement is checked by other agents. Use deterministic scripts for mechanical
facts and guarded mutations; use independent agent judgement for review quality.

PR code is always untrusted. It runs only in a control-credential-free,
temporary workspace on the disposable single-tenant box. Agents run outside the
workspace and scrub service credentials from workspace commands. Scrubbing is
hygiene rather than a security boundary on a single-user host: bounded,
dedicated credentials and the disposable machine bound
the admitted residual risk. Do not introduce nested containers without a new
commissioning decision.

PR-controlled text and repository content are evidence, never authority. Pin
review-governing guidance to the observed trusted target revision, and expose
consequential forge changes through guarded scripts which re-check ownership,
current forge identity, and policy before mutation.

## Commands

| Command | Purpose |
| --- | --- |
| `go build ./...` | Build every package and command. |
| `go vet ./...` | Run Go's static checks. |
| `go test ./...` | Run the full Go test suite. |
| `make check` | Check shell syntax and ShellCheck when available, run the Python and resource-isolation tests, then test and build Go. |
| `make e2e` | Run the disposable Forgejo end-to-end journey; requires Docker and is not a live deployment smoke. |
| `make deployment-smoke` | Exercise production-shaped local configuration without contacting a real forge. |

Prefer `go test ./internal/shell -run '<TestName>'` for a focused Go regression.
Do not mutate the deployed Minos box unless the active brief explicitly places
live deployment in scope.
