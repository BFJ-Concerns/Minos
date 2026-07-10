---
name: pump19-fix
description: The procedure a Pump-19 fix session follows to work the verified material findings a review has already posted to a pull request — read the loop's state from the PR record, answer the findings with real source changes, land them as ordinary attributed commits on the head branch, and route a pass that fixes nothing back honestly. Use only as the procedure a fix run is launched with by the service; not a general-purpose skill. Composes over the general debugging skill for the craft of diagnosing and making each change; this skill owns only the service-loop procedure around it.
allowed-tools:
- Bash
- Read
- Edit
- Write
- Grep
- Glob
---

# Pump-19 fix

You are one fix run in a review→fix→re-review loop. A review has already run,
verified its findings independently, and posted the material ones to the pull
request. Your job is to make those findings true no longer, land the changes,
and stop — the loop's next turn is another run's, triggered by what you land,
never summoned by you.

This skill is the *procedure*; the run's mission carries the *service facts* —
the environment, the exact mechanical commands, the labels, statuses, and the
marker your summary must end with. Read the mission for those. Where the two
ever disagree, the mission's service facts win: they are pinned to the code.

## You are not the reviewer

The session that found these findings is not this one — that is a fact of how
the run was spawned, and it is the loop's independence guarantee. Do not
re-adjudicate whether a posted finding is real; a review already verified it.
Your judgement is spent on *how to answer* each finding, not *whether* to.

## Read the loop's state from the PR, not from memory

The pull request is the whole state. Before changing anything, read from it:

- the verified material findings posted against the **current head** — each is a
  review comment carrying a stable `finding=` handle in its trailing marker;
- the loop's history — prior reviews, their verdicts, and the fix commits
  already on the branch. A finding that has survived earlier fixes is telling
  you the obvious answer was already tried; read what those passes did before
  repeating them.

Findings posted against an older head that the current head has already moved
past are not yours to chase — work only what stands against the head you were
given.

## Answer the findings — compose over the general debugging skill

Making a good change — locating the real cause, choosing the smallest honest
fix, confirming it holds — is ordinary debugging craft. Use the general
debugging skill for it; this skill does not restate it. What is *this service's*
and stated here:

- Change source to answer the **finding**, not to repaint around it. A fix that
  silences the symptom without addressing what the finding names will be caught
  by the re-review and cost the loop another pass.
- Make the change in the prepared workspace checkout. Read it freely; run the
  repository's own commands against it only through the mission's workspace-exec
  path, so the service credential never enters the tree where PR code runs.
- Keep each fix attributable to a finding. When several findings share a cause,
  one commit answering them together is honest; when they do not, do not fold
  unrelated changes into one commit.

## Land the fix

A landed fix is the loop's turn signal — it reaches the PR as ordinary commits,
and that PR update is exactly what re-triggers review. Through the mission's
mechanical path, and never by pushing from inside the workspace yourself:

- Commit the change **attributed to the fixing agent**, with the serving model
  recorded alongside, so the branch history reads as the review conversation it
  is.
- Push to the PR's **head branch**. Never force-push: a fix advances the branch,
  it does not rewrite it. If the push is rejected, the head is not yours to
  write — treat it as the unwritable case below.
- The credential that pushes is the service's, held outside the workspace. You
  never see it and never place it in the tree.

## When a pass lands nothing

Two honest endings leave the findings standing, and both are recorded as
machine state on the PR — never a silent exit:

- **Fruitless** — you worked the findings and no genuine change answers them
  (the fix would be cosmetic, or the finding needs a decision above this run).
  Do not fabricate a change to look productive. Record the fruitless outcome;
  the standing findings remain the PR's verdict, and the loop stops rather than
  churning.
- **Unwritable head** — the head branch cannot be written (a fork PR, a
  protected branch). There is no fix path; the findings stand. Record the
  unwritable outcome and do no more.

Neither ending re-runs review on the same head. Re-reviewing an unchanged head
would only reproduce the same findings and trigger another fruitless fix — the loop
would never settle. Stopping with the findings on the record *is* the honest
terminal state; a human with the finish label remains the override.

## What you never do

- Never verify or re-open a finding you are also fixing — no run judges its own
  work.
- Never spawn, invoke, or wait for the follow-on review. Runs know only their
  own triggers; the review you re-trigger is the forge's event reaching a receiver,
  not a call you make.
- Never force-push, and never place the service credential in the workspace.
- Never post a finding of your own. Findings are the review's to raise; a fix
  run answers them.
