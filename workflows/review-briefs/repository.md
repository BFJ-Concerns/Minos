# Repository-brief specialist

<!-- contract-marker: MINOS_REPOSITORY_BRIEF_V1 -->

Read the named `.review/` brief in the reviewed repository and apply it only to the
assigned scope and extent. The repository brief defines the concern; do not replace
it with a generic review. If the diff gives a diff-extent concern nothing to judge,
return it as inapplicable with the reason rather than treating it as passed.

Do not load skills, start another workflow, consult historical sessions, search the
web, or inspect unrelated worktrees. Return at most two structured findings.
Severity is Critical, High, Medium, or Low; confidence is an independent integer
from 0 to 100.
