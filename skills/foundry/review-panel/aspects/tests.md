---
title: Test Coverage
relevance: The change adds or modifies behaviour — logic, validation, parsing, state handling, calculations. Clearly irrelevant for changes confined to comments, documentation, formatting, or configuration with no behavioural effect.
---

# Test Coverage

Review whether the change's behaviour is protected by tests that would actually
fail if it broke. The measure is behavioural coverage, not line coverage: a
test suite can execute every changed line and still let every meaningful
regression through, so do not infer adequacy from percentages, from touched
lines, or from the mere presence of new tests.

## What to examine

1. Derive the behaviour the change promises — from the code, its callers, the
   requirements the change visibly serves, and the tests themselves. Map the
   success paths, the boundaries, the state transitions, and the failure
   conditions.
2. Map the tests onto that behaviour. For each critical path, ask: if this
   broke tomorrow, would a test fail? Pay particular attention to:
   - error-handling paths, which fail silently in production when untested
   - boundary conditions and empty/degenerate inputs
   - negative cases for validation logic — what must be rejected
   - concurrent or asynchronous behaviour, where the change involves it
3. Judge the tests' quality, not just their presence:
   - a test coupled to implementation detail breaks on refactor rather than on
     regression — it protects nothing and costs maintenance
   - an assertion so weak it passes for wrong output protects nothing either
   - tests should read as statements of behaviour a maintainer can trust

## What qualifies as a finding

A missing-test finding must name the concrete regression the current suite
would let pass: the specific input or sequence, the wrong outcome it would
produce, and why no existing test catches it. "More coverage would be good" is
not a finding. Check first whether an existing test — including integration
tests elsewhere in the suite — already covers the scenario.

Rate the priority by consequence: a data-loss or corruption path with no test
is a P0/P1; an untested cosmetic branch is a P3. Do not request tests for
trivial pass-through code with no logic of its own, and weigh the maintenance
cost of each suggested test against the regression it prevents.
