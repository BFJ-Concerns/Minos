# Intermittent Failures

Instruments and priors for diagnosing a failure that does not reproduce on
every run.

## Rate arithmetic

When evidence is statistical, comparisons such as "this condition makes it
worse" need a baseline failure rate. Measure one; when failures are rare or
runs expensive, prefer controlling the suspected mechanism directly (below).
If that still leaves reproduction probabilistic, compare before and after
under the same controlled conditions.
The arithmetic is unforgiving: a test that fails one run in ten passes a
single rerun 90% of the time. When using the zero-failure rule to test for a
drop from baseline `p`, observe `n` consecutive clean runs where
`(1-p)^n` falls below your accepted doubt; for 95% confidence `n ≈ 3/p`, so a
one-in-ten failure needs about 29 clean runs and a one-in-a-hundred failure
about 300. Report rates with their run counts ("3 failures in 40 runs"),
never as impressions.

## Classify the mechanism by varying the conditions

Choose a controlled variation that distinguishes the surviving hypotheses.
Observe whether it controls the failure; use the rate arithmetic above when
the result remains probabilistic. Read host conditions from existing failing
runs; do not wait for load to recur:

| Variation | If the failure follows it, suspect |
|---|---|
| Rerun in the same process and host, unchanged | randomness, a race — the nondeterminism is internal |
| Rerun alone versus within the full suite | ordering: fails only after others → polluted by shared state; fails only alone → depends on another's leavings |
| Rerun in a different order | ordering dependence generally; randomising unspecified orders (collection iteration, test shuffling) flushes hidden assumptions |
| Rerun with a shifted or frozen test clock | time assumptions — midnight boundaries, timezone handling, expiry windows |
| Rerun on a different host or fresh environment | environment coupling, leftover state, resource contention |
| Force competing operations into a chosen interleaving with barriers or a controlled scheduler | races and shared-resource conflicts tied to that ordering |

Three of these rows carry direct large-scale evidence: GitHub's CI, retrying
failures under just same-conditions, shifted-time, and different-host
variations, classified about 90% of its intermittent failures. The remaining
rows extend the same move along the ordering and interleaving dimensions. The
table is usually the cheapest place to start; leave it for bespoke experiments
whenever the failure's shape already points somewhere specific.

## Where the causes actually live

The best-evidenced cause distribution comes from a study of roughly two
hundred fixed flaky tests across 51 open-source Java projects in the Apache
ecosystem, and later studies across other languages broadly agree: waiting on
asynchronous results accounts for the largest share (roughly 45%), concurrency
for about 20%, and test-order dependence for about 12% — the rest spreads
thinly across resource leaks, network, time, randomness, and floating point.
Treat these as priors for ordering suspects, weighted by how much your stack
resembles that population. Two corrections the numbers make to intuition:

- **Suspect the harness and the environment as much as the product code.**
  Order dependence, leaked resources, and environment coupling live in test
  and infrastructure code; an investigation that only reads the code under
  test is blind to the majority class. Large, resource-heavy, multi-service
  tests flake at dramatically higher rates than small hermetic ones — size of
  the moving surface predicts flakiness better than the code's age or churn.
- **Do not assume "flaky test" means "broken test".** When the nondeterminism
  is in the product — a real race, a real unguarded await — the intermittent
  test is the only witness to a production bug, and silencing it (retry,
  quarantine, deletion) ships the bug.

## Controlling the reproduction

Prefer a replayable witness to more samples. Control the suspected condition
within the test's own fixtures and resources:

- use barriers, latches or a test scheduler to pause and release competing
  operations at the suspected race window;
- advance an injected clock to exercise expiry or deadlines without wall-clock
  sleeps;
- reduce a test-owned connection pool's capacity so leaked connections exhaust
  it in a reproducible sequence;
- fix the random seed to freeze one behaviour, then sweep seeds to find a
  failing one you can replay at will;
- set test order or collection iteration order explicitly;
- if repetition is still useful, run the case sequentially.

The controlled case must exercise the original failing mechanism. Forcing an
unrelated failure proves nothing about it; a remaining gap belongs in a
suspected or stopped result.

## Mechanism to fix, with the evidence trap

The same study's fix distribution matches its cause distribution: async-wait
failures are overwhelmingly fixed by waiting on the actual condition rather
than lengthening a delay; order-dependence by making setup and cleanup own the
shared state; concurrency by real synchronisation or by removing the sharing.
Make correctness tests independent of incidental host speed: wait for the
completion condition, control time and isolate state. Keep a bounded timeout
to catch hangs; unless elapsed time is the behaviour under test, it must not
stand in for successful completion. Preserve assertions of real timing
requirements and product correctness.

Confirm the repair with the same controlled witness that exposed the original
mechanism. If evidence remains statistical, report comparable rates and run
counts within the investigation's budget, and state any remaining uncertainty.
