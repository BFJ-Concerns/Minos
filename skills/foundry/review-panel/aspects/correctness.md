---
title: Correctness
relevance: Any change to code. Never skip this aspect.
---

# Correctness

Review the changed code for defects that will affect behaviour, and for
violations of the project's own stated conventions.

## What to examine

- **Actual bugs**: logic errors, mishandled null/absent values, off-by-one and
  boundary mistakes, race conditions, resource leaks, state that can become
  inconsistent, and performance problems that will matter at realistic scale.
- **Project conventions**: the repository's own guidance files (its agent
  guidance, contributing notes, or documented style) state rules about imports,
  error handling, logging, naming, and testing. A clear violation of an explicit
  project rule is a finding; a habit the project never wrote down is not.
- **Duplication and missing handling**: copy-pasted logic that already exists
  elsewhere in the codebase, and error or edge cases the surrounding code
  handles but the new code silently does not.

## The confidence bar

Report a finding only at high confidence — the level at which you would stake a
claim in front of the code's author. Internally, rate how sure you are that the
defect is real, from 0 to 100. The score measures certainty alone, never how
much the defect matters:

- 0–25: likely a misreading of the code, or speculation you did not verify
- 26–50: plausible, but you could not confirm it against what the code does
- 51–75: probable — the evidence points there but an innocent explanation remains
- 76–100: verified — you re-read the code and can show the failing behaviour or
  the explicit written rule it violates

Report only findings at 80 or above. Precision is what keeps an automated
review worth reading: one wrong claim costs more trust than three missed
nitpicks. Impact is a separate axis, expressed only through `priority` — a
verified trivial defect is a confident P3 and is still reported; an unverified
grave suspicion is below the bar however serious it would be if true.

## What to leave out

Style points a formatter or linter would catch; preferences with no written
project rule behind them; speculation about code you have not read.
