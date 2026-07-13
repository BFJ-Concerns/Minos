# Failure taxonomy — which lane a failure takes

A lifecycle attempt meets failures of very different kinds, and the wrong
response to each is expensive in opposite directions: retrying a deterministic
configuration error is hours of silent waste, while latching a passing
weather-cloud transient is a stall nobody restarts. This document tells you, the
lead, which **lane** a failure takes. It does not own the retry machinery —
backoff cadence and incident deduplication belong to the deployment's
coordination ledger; the taxonomy only tells you which lane a failure enters so
the ledger's semantics apply to the right thing.

Classify by the response the failure requires, not by a fixed list of error
strings. Three lanes:

## Lane 1 — In-run adaptation

A worker or tool stumbles in a way you can absorb without ending the run: a
single reviewer returns nothing, a checker times out, an engine briefly refuses,
a fan-out partially fails. This is the ordinary texture of conducting agents.
You are the agent conducting agents precisely so you can notice and adapt:
retry the worker, reassign it to a pin-admissible peer, or degrade the fan-out
loudly (recording the degradation) rather than refusing to run. Ensemble owns
most of this — its workflows retry and degrade internally — and what surfaces to
you, you handle in-run. The run continues; nothing is written to the PR about it.

The load-bearing constraints still hold while you adapt: a degraded family split
is *recorded and represented as limited verification*, never hidden; a worker
whose pin cannot be admitted is inadmissible, not merely retried; and honest
coverage is never narrowed to route around a failed reviewer — a concern whose
reviewer never returned is an unchecked concern, reported as such, not a passed
one.

## Lane 2 — Retryable exit (transient)

A condition a later attempt could genuinely find changed, that you cannot absorb
in-run:

- host capacity or saturation;
- engine or model-backend availability, including a shared-credential
  stale-refresh the readiness path should distinguish from a real outage;
- any transient forge or network failure that a fresh attempt would likely clear.

Take the retryable-exit lane:

1. Write **no** product-state failure and **no** terminal marker to the PR — an
   operational failure is not a product state. The head is left looking exactly
   like a never-run head.
2. Run `"$MINOS_BIN" run-guard --config "$MINOS_CONFIG" retryable-exit
   FAILURE_CATEGORY`. Use exactly `host-capacity`, `engine-unavailable`,
   `stale-oauth`, `forge-unavailable`, or `network-unavailable` according to the
   failed dependency, never raw backend text. The command durably declares the
   failure, then removes eyes.
3. Exit immediately. The wrapper records the incident and backoff, then releases
   the lease strictly last so reconciliation re-derives and a fresh successor
   attempts the remaining journey.

The ledger's attempt/backoff record paces the re-launch (exponential, from minutes
to a capped few hours, indefinitely — there is no fixed attempt ceiling: the
admission cap and the liveness threshold bound the service structurally, not a
spend counter). A retryable exit that recurs is still loud: every operational
failure emits one deduplicated deployment incident carrying the repository, PR,
attempt, observed revisions, retry disposition, and the diagnostic-log location —
so a persistently transient-looking failure surfaces to the operator rather than
retrying invisibly forever.

## Lane 3 — Terminal-configuration failure

A failure a retry cannot change, because the fault is in the wiring or the
inputs, not the weather:

- missing or unreadable required configuration, an unset required variable, a
  missing skill or script;
- invalid inputs the run cannot proceed against;
- a **lead or worker model pin mismatch** — the served model differs from its
  pin. (An unresolvable lead pin is the sharpest case: the pins exist precisely
  to catch a floating alias silently serving the wrong model.)
- a gate-bearing version mismatch — the pinned `review-panel` skill or this
  lifecycle instruction serving a version other than its pin.

These must **not** burn the retry ladder: retrying a deterministic failure just
spends attempts on a fault no attempt can fix, turning a loud problem into hours
of silence. Take the terminal lane:

1. Raise the deduplicated deployment incident with the diagnostic-log location
   (no secrets), so the failure is loud to the operator.
2. Close exactly as every controlled exit closes (`lifecycle.md` step 9):
   **remove the eyes reaction first, then release the lease strictly last** — the
   order never inverts on a terminal-configuration exit either.
3. Do not re-enter the ordinary transient backoff as though a later attempt
   would differ. The recovery is an operator's — fix the wiring, correct the
   pin, restore the credential — after which readiness preflight and the
   reconciliation path admit the work again. Readiness expiry itself prevents a
   new launch and emits its own deployment incident rather than writing backend
   error text to the PR.

The distinction between a *valid adverse* judgement and a *failure to obtain* a
judgement matters here and is not a Lane 3 case: a review-bar check that renders
a valid adverse verdict, or a verifier that legitimately rejects a finding, is
the system working — never degraded away. Only the *checker itself* failing to
run or returning unusable output is a failure, and it takes Lane 1 (retry or
reassign) escalating to the explicit policy-permitted limited path if no checker
can be obtained — not Lane 3, unless the cause is a configuration fault such as a
pin mismatch.

## The through-line

Whichever lane a failure takes, three things are invariant:

- **The PR surface stays clean of operational chatter** — the diagnostic *why*
  lands in the deployment log and the incident, never as a PR comment or an error
  status. A reader sees what the service produced, never how it stumbled.
- **Failure is loud to the operator** — through the deduplicated incident and the
  deployment log; that visibility is not configurable.
- **No manufactured product state** — you never write a clean, converged, or
  merged result to make a failing run look finished. An honest exit into no
  product state (Lane 2) or an incident (Lane 3) is always correct over a
  falsified one.
