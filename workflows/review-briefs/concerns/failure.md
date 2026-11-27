---
summary: what happens when a step fails, is interrupted, or overlaps with another
---
What happens when things do not go to plan.

Follow the paths where a step fails or is interrupted, or where two things overlap: an error returned or raised, a process killed or restarted part-way through, a step replayed, a timeout, two actors touching the same state, events arriving in an order the code did not expect, cleanup that has to happen whatever else does.

For each one, ask what state is left behind, who would notice, and whether the next attempt recovers or makes matters worse. Look at what the change does with each error it can receive: whether it is passed on, handled, or lost, and whether what is reported afterwards is still true.

Keep to failures this project can actually meet: the ones its own code raises, its documents mention, or the things it plainly depends on can produce.

Your ground is the run that goes wrong. A wrong result when nothing has failed is `logic`'s. A failure that only a hostile or untrusted party would cause on purpose is `security`'s, though what the code does once a step has failed is yours whoever caused it. A failure path with no test is `tests`'s to report; yours is the path that misbehaves.
