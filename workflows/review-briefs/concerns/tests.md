---
summary: whether the tests prove what the change claims
---
Whether the tests prove what the change claims.

For each test the change adds or alters, ask what break in the code would make it fail: a test that would pass anyway proves nothing. Look for behaviour the change adds, and which its description or the project's documents treat as important, that no test exercises. Look for stand-ins and fixtures that behave differently from the real thing in a way that could hide a fault. Look for tests that can fail for reasons unrelated to what they check, such as timing or the machine they run on.

A missing test is a finding when you can name the break it would let through. Say what that break is and which behaviour of the change goes unproved; a wish for more coverage is not one.

Your ground is the tests. When reading them shows you that the code itself misbehaves, that fault is `logic`'s or `failure`'s: report what is wrong with the tests, and give the fault in the code one entry in `outsideConcernFindings`. Whether a test is laid out the way the project lays out its tests is `fit`'s.
