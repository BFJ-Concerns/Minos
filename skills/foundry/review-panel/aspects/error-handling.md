---
title: Error Handling
relevance: The change touches error paths — exception handling, error callbacks, fallback or retry logic, logging, external calls, or I/O. Clearly irrelevant only when the diff contains none of these.
---

# Error Handling

Review how the changed code behaves when things go wrong. An error that is
swallowed without record is the failure mode that costs the most later: the
system misbehaves, nothing says why, and the person debugging it months from
now has no trail to follow. This review exists to catch that before it lands.

## What to examine

Locate every place the changed code handles or could suppress an error: catch
blocks (or the language's equivalent — except clauses, Result handling, error
callbacks), conditional error branches, fallback logic and default values used
on failure, places where an error is logged and execution continues, and
optional-chaining or null-coalescing that skips an operation which can fail.

For each one, work through:

- **Record**: when this path fires, does anything durable say so — a log entry
  with enough context (what operation, what inputs, what state) to debug from
  later, at a severity the project's own logging conventions treat as visible?
- **Feedback**: does whoever depends on the operation learn that it failed, in
  terms that let them act — or does the system behave as if it succeeded?
- **Specificity**: what does this handler actually catch? Enumerate the
  unrelated failures a broad catch would also swallow — that list is the
  finding's evidence when the catch is too wide.
- **Fallbacks**: when the code substitutes an alternative on failure, is that
  substitution deliberate and visible, or does it conceal the underlying
  problem? Production code falling back to a stub, mock, or hard-coded value
  deserves a finding: it usually indicates the real dependency was never
  wired, and it hides that fact.
- **Propagation**: should this error travel up to a handler that can actually
  deal with it? Catching too early can prevent cleanup, retries, or an honest
  failure at the right level.

## Patterns that conceal failures

- an empty catch block
- catch, log at debug level, continue as if nothing happened
- returning a default or null on error with no record that an error occurred
- retry logic that exhausts its attempts silently
- a chain of fallbacks tried in order with no statement of why

## What qualifies as a finding

Name the specific failure that would be concealed and where it would surface —
"a network timeout here returns an empty list, which the caller renders as
'no results', so an outage reads as an empty account" is a finding; "this
catch could be more specific" on its own is not. Judge against the project's
own logging and error conventions where it has them, and do not prescribe a
particular logging stack when it does not.
