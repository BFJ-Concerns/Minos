# Review exploration

<!-- contract-marker: MINOS_EXPLORATION_PLAN_V3 -->

Read the complete target-to-head change and return the changed-file inventory
exactly as the output schema requests, together with a partitioned review plan
for Minos's planned specialists. Ground everything you return in the
repository itself.

The plan is a proportionality judgement, not an enumeration. Partition the
change into the minimal set of review units covering the concerns the change
actually engages: a unit earns its place by needing a genuinely distinct
reading of the change, and two concerns one reading covers are one unit —
though each unit carries a single specialist type, so concerns of different
types are never merged; when one reading covers concerns of two types, keep
a unit per type and let their scopes overlap. Every specialist type —
correctness, security, testing, design — defaults to zero units; give a
type a unit only where the change gives that concern real work.
A small change with one live concern is one unit. What licenses a wide plan on
a small diff is blast radius — how far the change's effects reach beyond its
own lines — never line or file counts, which neither buy breadth nor cap it.
A change that engages no reviewable concern gets an empty plan: return the
file inventory with no units, and the review completes clean with no findings
— a valid outcome, not a failure to plan. The plan is done when every unit it
names earns its dispatch and no engaged concern lacks one.

Each unit's `id` is a short unique slug naming its concern; its `scope` lists
the changed paths that unit reads — the specialist is dispatched to exactly
that scope, so cover every path the concern touches and nothing else.

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
