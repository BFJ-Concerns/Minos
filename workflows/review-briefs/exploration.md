# Review exploration

<!-- contract-marker: MINOS_EXPLORATION_PLAN_V3 -->

Read the complete target-to-head change and return the changed-file inventory
exactly as the output schema requests, together with a partitioned review plan
for Minos's planned specialists. Ground everything you return in the
repository itself.

{{reviewed-change}}

## The plan

The plan is a proportionality judgement, not an enumeration. The review has
these specialist concerns, each a different kind of problem to read the
change for:

{{concerns}}

Every concern defaults to zero units; give one a unit only where the change
gives that concern real work. A concern's only specialist reads the whole
change for its own kind of problem, so one unit for each engaged concern is
the ordinary plan, and a small change with one live concern is one unit. What
licenses a wide plan on a small diff is blast radius — how far the change's
effects reach beyond its own lines — never line or file counts, which neither
buy breadth nor cap it.

{{exploration-depth}}

Where a concern has more than one unit, its units divide the change between
them: each unit's `scope` is its share, the changed paths its specialist
answers for, and every changed path falls in one of those shares, so that no
part of the change goes unread for the concern.
A change that engages no reviewable concern gets an empty plan: return the
file inventory with no units, and the review completes clean with no findings
— a valid outcome, not a failure to plan. The plan is done when every unit it
names earns its dispatch and no engaged concern lacks one.

Each unit's `id` is a short unique slug, and its `specialistType` names the
concern. Its `concern` says in a sentence what this change gives that
specialist to judge; the specialist is handed that sentence, so make it
specific to this change. Its `scope` lists the changed paths where that work
lies. For a concern's only unit, the scope is where the specialist starts and
reads most closely before it goes on to the rest of the change, so a path in
no unit's scope is still read by every such specialist. For a unit that
shares its concern, the scope is the share described above: its specialist is
shown the other units' shares and is finished when it has read its own. A
scope never bounds what a specialist may read or report.

The output schema's `applicability.reason` field carries the judgement behind
the plan's size: state, in a sentence or two, why the change earns exactly the
units you named — which concerns it engages and which it does not. For an
empty plan this reason is the whole review's record of why nothing was
dispatched, so make it stand on its own: say what the change is and why no
concern has real work, not merely "nothing applies".

Enumerate the changed paths exactly as the output schema requests — the
inventory is returned in full even when the plan is empty. Report a binary
change as zero lines added and deleted; the path's presence is what records
it. Use Git and the
current repository only. Do not load skills, start another workflow, consult historical sessions,
search the web, or inspect another worktree. Finish with structured output
inside this one assignment.
