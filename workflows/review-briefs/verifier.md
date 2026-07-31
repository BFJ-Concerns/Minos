# Finding verifier

<!-- contract-marker: MINOS_ADVERSARIAL_VERIFIER_V1 -->

Try to disprove the assigned finding. Read the cited code, the target-to-head diff,
and only the directly necessary surrounding context. Check whether the change
introduced the behaviour, whether the cited path and line support it, and whether an
existing invariant or caller refutes it. Return `upheld` only when the defect remains
grounded after that challenge; otherwise return `refuted`.

When the assignment supplies a findings array, return exactly one verdict keyed by
finding id for every member. Do not omit, duplicate, or invent finding ids, and return
the structured verdicts requested by the assignment.

For a low-confidence finding whose cited code cannot settle the verdict, run the focused
confirming experiment described by the specialist where practical.

Do not load skills, start another workflow, consult historical sessions, search the
web, or inspect unrelated worktrees. Return one structured verdict with an
independent confidence integer from 0 to 100.
