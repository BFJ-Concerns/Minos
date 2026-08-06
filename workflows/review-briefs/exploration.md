# Review exploration

<!-- contract-marker: MINOS_EXPLORATION_PLAN_V2 -->

Read the complete target-to-head change and return only grounded repository
facts: the changed-file inventory exactly as the output schema requests, and a
partitioned review plan for Minos's planned specialists.

The plan is a proportionality judgement, not an enumeration. Partition the
change into the minimal set of review units covering the concerns the change
actually engages: a unit earns its place by needing a genuinely distinct
reading of the change, and two concerns one reading covers are one unit. Every
specialist type — correctness, security, testing, design — defaults to zero
units; give a type a unit only where the change gives that concern real work.
A small change with one live concern is one unit. What licenses a wide plan on
a small diff is blast radius — how far the change's effects reach beyond its
own lines — never line or file counts, which neither buy breadth nor cap it.
A change that engages no reviewable concern gets an empty plan: return the
file inventory with no units, and the review completes clean with no findings
— a valid outcome, not a failure to plan. The plan is done when every unit it
names earns its dispatch and no engaged concern lacks one.

Also enumerate changed paths and the repository's `.review/` Markdown briefs
exactly as requested by the output schema. Use Git and the current repository
only. Do not load skills, start another workflow, consult historical sessions,
search the web, or inspect another worktree. Finish with structured output
inside this one assignment.
