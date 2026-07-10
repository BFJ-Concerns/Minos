---
name: root-cause
description: Diagnose a software failure that resists isolation, and prove the cause rather than accept a plausible one. Use when asked to debug, diagnose, or find the cause of a failure; when a fix request arrives with its suspected cause already named by a ticket, comment, teammate, or earlier session ("the ticket says it's the TTL refresh", "it's probably the cache") — the supplied diagnosis needs auditing before the fix; and when a failure looks hard to pin down — intermittent or unreproducible symptoms, a symptom far from any plausible cause, a fix that did not hold or that moved the symptom rather than removing it, or territory such as concurrency, timing, caching, test ordering, or environment differences. Not for ordinary errors whose cause is apparent from the message and a quick read of the code.
user-invocable: true
---

# Root Cause

Establish, with evidence, why a failure happens. The diagnosis is the
deliverable: the surrounding task decides what happens next — report the cause,
fix it, or act on a cause someone else supplied — and this discipline governs
how the cause is established, whichever framing the task uses. Repair remains
ordinary coding work owned by the surrounding task; this discipline governs
what may be claimed about the cause, and says nothing about when to act.

## Does this failure need the discipline?

Most bugs do not. When the error message and a quick read of the nearby code
make the cause apparent, confirm it as cheaply as it deserves and carry on
with whatever the task asked for — this skill no longer applies. The
discipline is for failures that resist isolation:

- reproduction is intermittent, or the failure resists reproduction entirely;
- the symptom appears far from any plausible cause;
- a previous fix for this same symptom did not hold — the moment that happens
  the bug qualifies, and the correct response is re-diagnosis, not a second
  patch;
- the failure sits in territory where causes hide from a straight read:
  concurrency and timing, caching and shared state, test ordering, environment
  differences, resource exhaustion, randomness.

De-escalate as freely as you escalate: when early evidence reveals a simple
cause, prove it cheaply and hand it back. Match the ceremony to the bug, in
both directions.

## What proof means

A cause is proven when evidence distinguishes it from rival explanations — not
when a story fits the facts so far. The sharpest form is a discriminating
experiment: state what you expect to observe if your hypothesis is true and
what the rival predicts instead, *before* looking — a prediction registered
after the observation has no discriminating power. Other demonstrations reach
the same standard: a bisection landing on a specific change, a crash trace that
pins the mechanism, an invariant caught being violated, the symptom appearing
and disappearing as the suspected condition is toggled on and off.

What never clears the bar on its own: correlation, plausibility, proximity in
the stack trace, an authority's confidence, or the symptom going quiet after a
change. Each of these generates hypotheses; none of them proves one.

Whether the evidence in front of you clears the bar is your judgement to make —
the bar describes what proof looks like, not which technique to use. Two habits
keep that judgement honest. Keep at least one rival explanation alive until
evidence kills it; a diagnosis that never had a rival was never tested. And
prefer the next action that would *distinguish* between your hypotheses over
the one that gathers more of what you already have.

## The temptations

Diagnoses rarely fail for lack of method. They fail at specific moments, when a
plausible shortcut presents itself. Each ruling below is indexed by the thought
as it arrives.

**"It passes now."** An intermittent failure has a failure *rate*, and a green
run is a sample, not a verdict — a test failing one time in ten passes nine
reruns unchanged. Establish how often it fails before your change, and hold the
after against that baseline. The claim to make is "the mechanism this change
removes is the one that produced the failures", supported by rate and by
mechanism, never by a lucky pass.

**"I'll just try this and see."** A guessed change is a legitimate experiment
and an illegitimate fix. Before running it, say what each outcome would tell
you; afterwards, treat the result as evidence about the mechanism, not as a
verdict on the bug. This applies with full force to suppression moves — a
sleep, a retry, a loosened assertion, a reordering. Declared as experiments,
they are respectable instruments: a sleep that makes a failure vanish has
discriminated in favour of a timing hypothesis. Left in place as fixes, they
bury the mechanism unproven — and if that mechanism also lives in production
code, the buried bug is a production bug.

**"Someone already knows the cause."** A cause asserted by the user, a ticket,
a code comment, a previous session, or the most senior voice in the room enters
the investigation as a hypothesis — often the best one available, never as a
finding. When it arrives with evidence, audit the evidence against the bar: if
it already clears, accept it and proceed to what the task asked for; if it
falls short, the work is only the missing piece, not a fresh investigation.
Confidence is not part of the evidence.

**"One more patch will land it."** A fix that did not hold is discriminating
evidence — read it before writing more code. Rule out the mundane branch
first: did the change actually run (deployed, rebuilt, the right cache
cleared), and does it cover every path the diagnosis names? When the repair
genuinely landed and the symptom persists, the diagnosis itself becomes the
suspect — the mechanism you modelled is not, or is not the whole of, the
mechanism firing. Stop patching and re-diagnose from the symptom, keeping the
failed fix and its result as evidence: it rules territory out, which is more
than most experiments achieve.

## Honest exits

An investigation ends in one of three states. Name which one you are in; the
discriminator is whether the evidence favours a surviving suspect. The effort
the investigation may spend belongs to the surrounding task where it set one;
where it did not, declare a rough bound of your own at the outset, and treat
reaching it as the moment to exit through whichever state the evidence has
earned.

- **Proven** — the cause, the evidence that distinguishes it from its rivals,
  and the reproduction or equivalent witness, handed to the surrounding task.
- **Suspected** — a leading hypothesis the evidence supports but does not yet
  prove, with the gap named precisely: the experiment not yet run, the rival
  not yet ruled out, the reproduction not yet reliable. This is the exit
  whenever a live suspect survives, whatever ended the investigation. A
  suspected cause is a respectable result when it is labelled as one.
- **Stopped** — the investigation ends with no hypothesis the evidence
  favours. Report what reproduced, what was ruled out and by what evidence,
  what remains uneliminated, and the single experiment that would most advance
  a fresh attempt. This shape is the deliverable that makes stopping honest —
  the next investigator starts from your evidence instead of from the symptom.

A guess dressed as a finding is the one prohibited output. It costs more than
no diagnosis, because it carries authority it did not earn — and by the ruling
above, the next investigator is obliged to treat it as a hypothesis anyway.

## Handing back

The reproduction is part of the diagnosis, and it outlives the investigation:
whoever repairs the fault — you, later in the same task, or someone else
entirely — runs it after the fix to confirm the original symptom is gone.
Where the failure resisted reproduction, hand over the strongest witness the
investigation produced instead — the instrumented invariant check, the crash
signature to watch for — and say plainly that it is a probe, not a
reproduction. Confirm the *exact* symptom: a failure that vanished but left a
materially changed symptom behind has moved, not died. When the reproduction
is a failure rate rather than a deterministic run, confirmation is the rate
falling to the level the diagnosis predicts, over enough runs to tell.

## Long investigations

For an investigation long enough that its early evidence could slip out of
working memory, a running ledger earns its keep: hypotheses with their
registered predictions, experiment results, and what has been ruled out. It
doubles as a drift alarm — a ledger with many observations and no killed
rivals is an investigation that is wandering — and every exit above can be
written straight from it. It is an aid, not an obligation.

<!-- foundry:variant ledger-location start -->

Keep the ledger in the session's scratchpad directory — the path the harness
announces in the system prompt's *Scratchpad Directory* section — rather than
in the repository.

<!-- foundry:variant ledger-location end -->

## Intermittent failures

When the failure is intermittent or reproduction is unreliable, read
`references/intermittent-failures.md`: it carries the classification
instruments, base-rate priors, and rate arithmetic that intermittence demands
and steady failures never need.
