# Intermittent Failures

Instruments and priors for diagnosing a failure that does not reproduce on
every run. Everything here exists because intermittence breaks the usual
evidence rules: a single observation — pass or fail — carries almost no
information, so the investigation has to work in rates, distributions, and
varied conditions instead.

## Rate arithmetic

Comparisons are the currency of an intermittent investigation — "this change
fixes it", "this condition makes it worse" — and every comparison needs a
baseline failure rate to compare against. Measure one as early as the failure
allows; when the rate is too low or the runs too expensive to measure
directly, raising the reproduction rate (below) comes first and the baseline
is measured at the raised rate. The arithmetic is unforgiving: a test that
fails one run in ten passes a single rerun 90% of the time. The zero-failure
rule covers most confirmation needs — to claim the failure rate has genuinely
dropped from its baseline `p`, observe `n` consecutive clean runs where
`(1-p)^n` falls below your accepted doubt; for 95% confidence `n ≈ 3/p`, so a
one-in-ten failure needs about 29 clean runs and a one-in-a-hundred failure
about 300. Report rates with their run counts ("3 failures in 40 runs"),
never as impressions.

## Classify the mechanism by varying the conditions

The cheapest discriminating instrument for an intermittent failure is to rerun
it under deliberately varied conditions and read the pattern. Read each row as
a rate comparison — enough runs under the varied condition, against the
baseline, to tell whether the rate actually moved (the arithmetic above):

| Variation | If the failure follows it, suspect |
|---|---|
| Rerun in the same process and host, unchanged | randomness, a race — the nondeterminism is internal |
| Rerun alone versus within the full suite | ordering: fails only after others → polluted by shared state; fails only alone → depends on another's leavings |
| Rerun in a different order | ordering dependence generally; randomising unspecified orders (collection iteration, test shuffling) flushes hidden assumptions |
| Rerun at a shifted or frozen clock | time assumptions — midnight boundaries, timezone handling, expiry windows |
| Rerun on a different host or fresh environment | environment coupling, leftover state, resource contention |
| Rerun under load or in parallel with itself | races and shared-resource conflicts that idle runs never interleave |

Three of these rows carry direct large-scale evidence: GitHub's CI, retrying
failures under just same-conditions, shifted-time, and different-host
variations, classified about 90% of its intermittent failures. The remaining
rows extend the same move along the ordering and load dimensions. The table is
usually the cheapest place to start; leave it for bespoke experiments whenever
the failure's shape already points somewhere specific.

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

## Raising the reproduction rate

An intermittent failure becomes tractable in proportion to its failure rate,
so it is often worth an experiment budget just to make it fail more:

- repeat the case in a tight loop, in-process, to multiply samples cheaply;
- run it in parallel with itself or under CPU/IO load to widen race windows;
- shrink shared resources — a connection pool of size one turns a slow leak
  into an immediate, deterministic failure;
- fix the random seed to freeze one behaviour, then sweep seeds to find a
  failing one you can replay at will;
- force the orderings the failure needs — scheduler pressure, reordered
  tests, randomised iteration — rather than waiting for them to occur.

Each of these is a condition change, so it doubles as classification: the
lever that moves the rate is itself evidence about the mechanism.

## Mechanism to fix, with the evidence trap

The same study's fix distribution matches its cause distribution: async-wait
failures are overwhelmingly fixed by waiting on the actual condition rather
than lengthening a delay; order-dependence by making setup and cleanup own the
shared state; concurrency by real synchronisation or by removing the sharing.
The trap when confirming any of these: the fix
changes the failure rate, so a fixed version passing a handful of runs proves
little — return to the rate arithmetic above and confirm at the run counts
the baseline demands.
