---
summary: whether the change does the right thing when everything goes to plan
---
Whether the change does the right thing when everything goes to plan.

Take each thing the change makes the software do and follow it through the code as it would ordinarily run. Are the conditions right? Is the right state read and written? Does each function hand the next one what it expects, in the shape, units and order it expects? Is each case the code itself distinguishes (none, one and many; first and last; present and missing; this kind and that kind) handled as intended?

Where the change alters something other code relies on (a signature, a default, the meaning of a value, the order of steps), follow it out to the callers and readers the change did not touch, and check that they still get what they need.

Read the change as its author meant it to work, and find where the code does something else. Where the project's documents or the author's description say what should happen, that is the intention to hold the code to.

Your ground is the ordinary run. What happens when a step fails, is interrupted or overlaps with another is `failure`'s. Whether someone can make the code do what it should not is `security`'s. When you find a fault that the tests ought to have caught, report the fault in the code; the gap in the tests is `tests`'s.
