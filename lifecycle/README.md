# `lifecycle/` — the service-owned lifecycle instruction

This directory holds the text a fresh lead-agent session receives to own one
pull request's whole journey. It is thin, service-owned adaptation composed over
the Foundry's general skills — never Foundry canon, and never a bespoke parallel
skill estate. It replaces the four per-stage missions in `missions/`
(`review.md`, `fix.md`, `finish.md`, `flaky.md`), which are retired: the
lifecycle rebuild folds review, repair, fresh re-review, final clearance, and
optional merge into one accountable session, so there are no per-stage runs and
no stage machine to hand off between.

- **`lifecycle.md`** — the primary instruction, read in full at launch: the
  journey from orientation to a defined product-state exit, the ownership and
  independence rules, the composition bridge to the pinned `review-panel` skill,
  and the exit vocabulary.
- **`failure-taxonomy.md`** — the three lanes a failure takes (in-run
  adaptation, retryable exit, terminal-configuration failure) and why each is
  handled the way it is.
- **`context-hygiene.md`** — the lifecycle index, the file-backed artefact
  convention, the compact-return contract for workers, index refresh on
  head/target movement, and the instrumentation the live proof reads.

The lead reads `lifecycle.md` first and the two companions before it first needs
each. Detail that a lead consults at a specific moment lives in the companions so
the launch spine stays lean — itself an instance of the context-hygiene the
service depends on.

## For integration

The launcher (`scripts/run-body/run-body`) pipes `lifecycle.md` into one fresh
lead session, and `docs/go-live.md` installs this directory as the service-owned
instruction set. The retired per-stage missions have been removed.
`lifecycle.md`'s closing sections and the unit report name every wave-1 seam the
text leans on (the coordination ledger's ownership operations, the forge
adapter's guarded verbs, the product-surface status and verdict vocabulary, the
review-soundness scripts, and Ensemble's pinned verification workflows); those
bind at integration.
