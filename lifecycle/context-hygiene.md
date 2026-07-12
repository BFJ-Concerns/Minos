# Context hygiene — keeping one session lean enough to finish

Your context is a scarce lifecycle resource. The normal supported journey is one
lead session from admission through merge or a substantive stop, and it must
complete without the engine auto-compacting — the live proof depends on it. That
is only possible if your active context stays lean: trusted instructions, the
current forge snapshot, a compact plan and decision record, the current review
assembly, the unresolved findings, and the evidence for the *next* decision —
and little else. These conventions are how you hold that line. They never narrow
coverage: fresh workers still perform the bounded full-file and whole-diff reads
review soundness requires, so hygiene removes duplication, never reach.

## The lifecycle index

Keep a single compact **lifecycle index** in attempt-local scratch: your working
map of the run. It records, tersely —

- the current head and target revisions, by immutable identity;
- what the service has already published (the reviews and their verdicts, the
  current `Minos` status), by reference not by quoted body;
- the plan and the consequential decisions taken so far, one line each;
- the unresolved questions, each mapped to the artefact that will answer it and
  the immutable head or target that artefact is bound to;
- for each full artefact, its path and a digest.

The index is working memory, not product authority and not recovery state: a
successor rebuilds its own from the forge and the ledger, never from your
scratch. Its job is to let you re-derive your compact current state at a glance
after a movement, instead of carrying stale excerpts forward.

## File-backed artefacts, referenced by path and digest

Full worker reports, command and CI logs, coverage records, diffs, and
prior-head evidence live as files in attempt-local scratch (at the run
directory the environment supplies), and enter your context only as a path and
a digest — never pasted in whole. Concretely, do **not** inject into your own
context:

- whole worker transcripts, or a worker's full report when its compact return
  and a path will do;
- raw command output or complete CI logs — have the mechanical scripts reduce
  large logs and inventories before you read them;
- repeated or unchanged diffs already represented in the index;
- unrelated PR conversation.

When you need a detail, read it from its file at the moment you need it and let
it fall away again. The scratch space is evidence for this attempt, deleted with
the workspace; it is never product state, and nothing load-bearing for a
successor lives only there.

## The compact-return contract for workers

Every worker you convene is briefed to return **compactly**, and you hold them to
it. A worker receives only the source, history, criteria, and prior findings its
role needs — not the whole run's context — and returns:

- its decision;
- the specific evidence it cites (quotes, file-and-line anchors);
- any unresolved questions;
- the path and digest of its full scratch artefact.

Never a raw transcript, never the complete logs, never a re-paste of the diff it
was given. The full working is in the artefact at its path; the compact return
is what enters your context. A worker that returns a wall of transcript has
broken the contract — take its artefact by reference and move on.

## Movement refreshes the index and re-forms the briefs

After every head or target movement and every consequential forge mutation,
update the index and **re-derive** the compact current state rather than carrying
stale excerpts into new work. In particular, when the head moves (a repair or a
sync landed) or the target moves:

- refresh the observed head/target in the index;
- discard convergence and clearance that the movement invalidated;
- **re-form every worker brief from the current facts** — a fresh review of the
  new head is briefed from the new diff and the current governing content, never
  from the prior head's excerpts.

This is why a repaired head gets a genuinely fresh whole-PR review and not a
diff-of-the-diff: the briefs are rebuilt, not patched.

## Compaction is a degraded contingency, not the hand-off

The routine hand-off between decisions is the index and the file-backed
artefacts — not an engine compaction. If the engine does compact, treat it as a
degraded fallback: before any consequential action afterwards, reload the compact
index and revalidate the load-bearing facts — the lease is still yours, the head
and target are unchanged, the trusted inputs and worker admissions still hold,
and the outcomes you believe you published actually are. If you cannot
re-establish those facts, exit without the mutation and let reconciliation start
a fresh successor; do not act on a half-remembered state.

The mechanical fallback is `"$MINOS_BIN" run-guard --config "$MINOS_CONFIG"
revalidate INDEX_PATH`. Run it after reloading the index and before the first
post-compaction forge mutation. It checks the fenced owner, current head and
target, trusted configuration identities, the non-empty lifecycle index, and
the captured lead-model evidence. Its success does not replace re-checking the
worker admissions and published outcomes recorded in the index; those remain
the lead's judgement-bearing revalidation.

## Instrumentation you keep

The launch wrapper records these automatically in
`$MINOS_RUN_DIR/instrumentation.json`; keep the lifecycle index and full worker
artefacts under `$MINOS_RUN_DIR` so their sizes are included:

- **lead prompt growth** across the journey;
- **input and output tokens** consumed;
- **scratch artefact sizes** — the files you back your context with;
- **engine compaction events** — each one, since the representative journey must
  complete with none.

The deployment profile names the model's context window and an
operator-approved token envelope. Exceeding the envelope is a measured design
failure to simplify — a signal to make the session leaner — not an automatic
timeout that kills honest work, and never a reason to buy a false clean result
by truncating coverage. An oversized PR stays honestly partial rather than
trading coverage for a fit.
