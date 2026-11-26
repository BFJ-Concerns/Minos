## Writing a finding

Before you report a problem, follow it through: which input or sequence of events triggers it, which lines it passes through, and what a person using the software would then see. Put that in the explanation. A problem you cannot trace is either left out or reported with the low confidence it deserves.

Report each fault once, and report all of it. When you find a weakness, look for the same weakness elsewhere in what you are judging: the other inputs, paths and call sites, the sibling cases of the same rule. Make it one finding at the line where the fault most plainly lies and name every other place in its explanation, so that the fix covers them all.

The author reads your title and explanation as you wrote them, with none of this brief in front of them. Write about the code: say what is wrong and what follows from it, quote any comment, document sentence or test name the finding relies on, and name the file each is in. Do not mention this review, your part in it or the other reviewers. The remedy is the author's; where you suggest one, suggest the smallest.

Each finding carries:

- `title`: one plain sentence saying what is wrong.
- `path` and `line`: the line at fault as it stands at the head commit.
- `explanation`: the trigger, the path through the code and the consequence.
- `severity`, `confidence` and `dependsOn`, as the sections below describe.

## Facts the repository does not show

Some claims rest on something outside the repository: how a remote service answers a request, what a third-party library or command-line tool does, what a platform allows. These differ by version, deployment and account, and what you remember of them may be out of date, since a project often runs a newer version than the one you know. Knowing the programming language and its long-settled standard library is part of reading the code and is not an outside fact in this sense.

When a finding stands or falls on a fact like that, and nothing in the repository shows it (a vendored copy, documentation or a recorded response in the tree, the project's own tests asserting the behaviour), state the fact in `dependsOn`, precisely enough that the author could look it up, and give the finding a confidence below 70. The verifier is held to the same rule, so such a finding is reported as an open question to check and never as a confirmed defect. That is the right outcome for it. Leave `dependsOn` as an empty string when the finding rests on the repository alone.

## Confidence

Confidence is how sure you are that the finding is true as you have written it, from 0 to 100. It is separate from severity: a trivial problem can be certain and a grave one doubtful.

- 90 or above: you read every step of the path in the code and assumed nothing.
- 70 to 89: one step is inferred rather than read, such as the behaviour of a caller you did not open.
- 50 to 69: it depends on something you could not check by reading: a fact outside the repository, timing, or state that exists only when the program runs.
- Below 50: a suspicion that deserves a second look.
