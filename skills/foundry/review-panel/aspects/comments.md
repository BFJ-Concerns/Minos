---
title: Comment Accuracy
relevance: The change adds or edits comments, docstrings, or documentation-bearing code; changes code whose existing comments might no longer hold; or introduces non-obvious logic that may need explanatory context it does not have. Clearly irrelevant only for changes with none of these — e.g. mechanical renames or formatting-only diffs.
---

# Comment Accuracy

Review the comments the change adds, edits, or invalidates. A comment that
contradicts its code is worse than no comment: a future reader believes the
comment, acts on it, and the code does something else. Read every comment as
that future reader — someone months away, with no memory of this change.

## What to examine

- **Accuracy**: check every claim a comment makes against the code it
  describes. Signatures, parameter and return descriptions, described
  behaviour, referenced names, claimed edge-case handling, complexity or
  performance claims — each must match what the code actually does.
- **Invalidated neighbours**: a change can falsify a comment without touching
  its line. Read the unchanged comments near the changed code and flag any the
  change has made stale — including TODO/FIXME notes the change has actually
  resolved.
- **Ambiguity**: wording that reads two ways, examples that no longer match the
  implementation, references to renamed or removed code.
- **Value**: a comment that restates what the code plainly says adds reading
  cost without information — flag it for removal, with the rationale. Comments
  that explain *why* (constraints, non-obvious reasons, warnings) are the ones
  worth having, and worth improving when they fall short.
- **Missing context**: a non-obvious precondition, side effect, or failure
  condition the code relies on but nothing states.

## What qualifies as a finding

An exact contradiction between comment and code, a comment the change has made
stale, or a concrete way the comment would mislead that future reader — quote
the comment and point at the code that disagrees with it. For a
restates-the-obvious removal candidate, the quoted comment and the line it
restates are the evidence. Suggest the corrected wording where the fix is not
obvious.

This aspect reviews and advises; it does not rewrite the comments itself.
