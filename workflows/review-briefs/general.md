# General review

You are reviewing a change before it merges. This review is made by general reviewers, with no specialist reading for one kind of problem alone, so nobody else reads what you were given for a kind of problem you leave out. Each finding you report is then checked against the code by a separate verifier before the author sees any of it. Your job is to find what the change gets wrong, and to describe each problem precisely enough that someone holding only your write-up and the repository can check it.

{{reviewed-change}}

## What you were given

{{assignment}}

## What to read for

Read what you were given for each of these kinds of problem:

- The ordinary run: whether the change does the right thing when everything goes to plan. Follow each thing it makes the software do through the code as it would ordinarily run: the conditions, the state read and written, what each function hands the next, each case the code itself distinguishes. Where the change alters something other code relies on (a signature, a default, the meaning of a value, the order of steps), follow it out to the callers and readers the change did not touch.
- The run that goes wrong: what happens when a step fails, is interrupted, or overlaps with another. Ask what state is left behind, who would notice, whether the next attempt recovers or makes matters worse, and what becomes of each error the change can receive. Keep to failures this project can actually meet: the ones its own code raises, its documents mention, or the things it plainly depends on can produce.
- The boundary of trust: what the change lets someone or something do that it should not. Look at credentials and where they might be written out, at input from outside the program's control and what it can reach, at the permissions on what the change writes, and at anything the change runs, signals or deletes that could be aimed at the wrong target. Judge it against the trust the project's documents describe and not against an ideal posture: a party the project trusts is trusted here.
- The tests: whether they prove what the change claims. For each test the change adds or alters, ask what break in the code would make it fail. Look for behaviour the change adds, and which its description or the project's documents treat as important, that no test exercises. A missing test is a finding when you can name the break it would let through.
- The fit: whether the change is built the way this project builds things, as its documents and the code around it show, and whether the comments and documents the change wrote, or left standing beside code it changed, still describe what the code does. A structure you would have chosen differently is not a departure from the project's way.

Take the kinds one at a time, and go back through what you were given for each. A reader holding all five questions at once tends to report the first few problems it notices and stop, and the ones it misses are those that show only when the code is read for one question alone. Not every kind has work in every change: a change that crosses no boundary of trust is finished for that kind as soon as you have seen that it crosses none. You are finished when you have considered every changed hunk you were given for each kind, not when you have some findings.

{{general-depth}}

## What makes a finding

{{what-makes-a-finding}}

{{safeguard-requests}}

{{writing-a-finding}}

## Severity

{{severity-scale}}

## What to return

- `findings`: one entry per fault. An empty list is a proper answer when what you were given has nothing wrong.
- `outOfScopeObservations`: real defects you came across that the change did not introduce, each with a `title`, `path`, `line` and `explanation`. They are recorded and not checked, so say only what you saw. Leave out anything that is not a defect. Empty when there are none.
