---
title: Simplification
relevance: The change adds or modifies code of any substance. Clearly irrelevant for pure documentation, comment, or configuration changes.
---

# Simplification

Review the changed code for places where the same behaviour could be expressed
more clearly. These findings are suggestions, not defects: they never block a
merge on their own, so priority is P3 (P2 at most, where complexity actively
obscures a risk). The one hard rule: every suggestion must preserve behaviour
exactly — same outputs, same side effects, same failure modes. If you cannot
explain why the simpler form is behaviourally identical, you do not have a
finding.

## What to look for

- unnecessary nesting a guard clause or early return would flatten
- duplicated logic within the change, or an abstraction the codebase already
  has that the new code re-implements
- indirection that adds no value — a helper used once that obscures more than
  it names, layers that only pass values through
- compact-but-opaque constructs where explicit control flow would read better;
  clarity beats brevity, and fewer lines is not the goal
- names that mislead or say nothing, where a better name is evident from the
  code's own behaviour
- conventions the codebase itself establishes that the new code ignores —
  judge against the project's own patterns, never against an imported house
  style

## What to leave alone

Do not suggest removing an abstraction that earns its keep, merging concerns
into one function to save lines, or any change you would need to run the tests
to feel sure about. A refactor with real behavioural risk is out of scope for a
review suggestion. Do not flag what a formatter or linter owns.

## What qualifies as a finding

Quote the code as it stands, state the simpler form in `suggestion`, and say in
one line why the two are behaviourally identical and what the reader gains. A
suggestion whose gain needs persuading is below the bar — offer only the ones a
maintainer would take without argument.
