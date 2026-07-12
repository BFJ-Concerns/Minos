---
name: minos-fix
description: Repair verified material findings inside an active Minos lifecycle. Use when the accountable lead assigns a fresh repair worker one or more independently verified current-head findings; the worker diagnoses and edits the prepared workspace, validates the repair, and returns evidence for the lead to push and re-review. Not a standalone fix run and not a general review skill.
allowed-tools:
- Bash
- Read
- Edit
- Write
- Grep
- Glob
---

# Minos repair step

Repair the verified material findings assigned by the accountable lifecycle
lead. Work only in the prepared workspace and return the repair to that lead;
the lifecycle remains one session and the lead owns every forge mutation.

## Inputs

The assignment names:

- the current head and target revisions;
- each verified finding, its stable finding identity, priority, exact anchor,
  trigger, and verifier evidence;
- the prepared workspace;
- the trusted build and test commands; and
- the scratch path for the full repair artefact.

Stop and report an invalid assignment when any of these are absent or when the
workspace is not at the named head. Do not infer current findings from labels,
old reviews, or session memory.

## Independence

The findings have already passed disjoint verification. Diagnose how to answer
them; do not suppress or re-adjudicate them. A repair worker does not review or
verify its own repair. Record enough evidence for fresh workers, who did not
author the change, to perform the required whole-PR re-review afterwards.

## Repair

For each assigned finding:

1. Read the affected code, its definitions, callers, and relevant tests.
2. Establish the cause and make the smallest complete source change which
   removes it. Address shared causes together; keep unrelated repairs distinct.
3. Add or update a regression test where the behaviour is testable.
4. Run the trusted focused checks through the lifecycle's credential-scrubbed
   workspace command, then run the assigned broader build and test checks.
5. Re-read the resulting diff and confirm every edit belongs to an assigned
   finding.

The workspace never receives forge or model-backend credentials. Do not commit,
push, post a review, set a status, merge, or invoke the follow-on review. The
lead performs the guarded, non-force push against the expected old head and
then convenes fresh whole-PR review and verification.

## Honest no-change outcomes

Return `fruitless` when no genuine source change answers an assigned finding.
Return `blocked` when the repair requires an outside decision or unavailable
dependency. Do not fabricate a change, and do not re-review the unchanged head.
The lead converts the surviving verified findings into the appropriate stopped
or blocked product outcome.

## Return contract

Write the complete repair artefact to the assigned scratch path. Return a
compact result containing:

- `outcome`: `repaired`, `fruitless`, or `blocked`;
- finding identities addressed and still standing;
- changed files and the reason for each change;
- tests and commands run, with their outcomes;
- unresolved questions;
- the artefact path and SHA-256 digest.

Model provenance stays in deployment evidence. It does not enter a commit,
review, status, or repair report intended for the pull request.
