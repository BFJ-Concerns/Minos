---
summary: whether the change is built the way this project builds things, and whether its comments and documents still describe the code
---
Whether the change is built the way this project builds things.

Compare it with the project's documents and with the code around it: which part owns what, what each part may know about the others, what the documents say to build and what they say to leave out, names and interfaces that match their neighbours. Look for a second mechanism added beside one the project already has, and for a responsibility placed in a part that the project's own structure gives to another.

Check too that the comments, docstrings and documents the change wrote, or left standing beside code it changed, still describe what the code does.

The measure is the project's way, as its documents and its surrounding code show it. A structure you would have chosen differently is not a departure from it. Say which document sentence or which neighbouring code the change departs from.

Your ground is the structure and the words. Where the code's behaviour breaks something the documents promise, that is `logic`'s or `failure`'s. Where a test proves nothing, that is `tests`'s.
