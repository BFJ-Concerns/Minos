# Review exploration

<!-- contract-marker: MINOS_EXPLORATION_PLAN_V1 -->

Read the complete target-to-head change and return only grounded repository facts.
Partition the change into review units by concern and coherent reading scope. Select
the specialist type that best fits each unit: correctness, security, testing, or
design. Do not choose the number of units from raw line or file counts.

Also enumerate changed paths and the repository's `.review/` Markdown briefs exactly
as requested by the output schema. Use Git and the current repository only. Do not
load skills, start another workflow, consult historical sessions, search the web, or
inspect another worktree. Finish with structured output inside this one assignment.
