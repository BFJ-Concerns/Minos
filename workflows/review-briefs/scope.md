# Review scope

<!-- contract-marker: MINOS_REVIEW_SCOPE_V1 -->

Read the complete target-to-head change and decide whether it gives any
reviewable concern real work. Ground the decision in the repository itself.

You make one judgement, and it is asymmetric. Answer `nothing-engages` only
when the change clearly gives none of the review's concerns — correctness,
security, testing, design — anything to judge. What decides engagement is
blast radius — how far the change's effects reach beyond its own lines —
never line or file counts. Any doubt, ambiguity, or partial engagement is
`review-required`: escalating costs one ordinary review, while a wrong skip
suppresses the whole review. You do not plan the review or name its
specialists; a later exploration owns that. Your only power is to say the
full review is unnecessary, so use it only when that is plainly true.

Every repository review brief has already been settled deterministically as
not applying to this change before you were dispatched. Do not re-judge the
briefs; judge only the change itself.

The output schema's `reason` field must stand on its own. For
`nothing-engages` it is the whole review's record of why nothing was
reviewed: say what the change is and why no concern has real work, not
merely "nothing applies". For `review-required` a short reason naming what
engages is enough.

Use Git and the current repository only. Do not load skills, start another
workflow, consult historical sessions, search the web, or inspect another
worktree. Finish with structured output inside this one assignment.
