import { test } from "node:test";
import assert from "node:assert/strict";

import {
  DECISION_KIND,
  DIGEST_KIND,
  findingKey,
  sweepDigest,
  validateSweepDecision,
} from "./completion-policy.mjs";

function finding(title, severity, path, line) {
  return { id: `specialist:${title}`, title, severity, confidence: 90, path, line, explanation: `${title} breaks the contract` };
}

function digestInput(findings, overrides = {}) {
  return {
    review: { status: "complete", confirmedFindings: findings },
    threshold: "High",
    maximumRounds: null,
    runRecord: { round: 0, confirmedUnfixed: [] },
    ...overrides,
  };
}

function decision(classification, basis = "stated grounds for the call") {
  return { kind: DECISION_KIND, classification, basis };
}

test("finding identity survives cosmetic retitles", () => {
  assert.equal(
    findingKey({ path: "a.go", line: 4, title: "  Unsafe   Transition " }),
    findingKey({ path: "a.go", line: 4, title: "unsafe transition" }),
  );
  assert.notEqual(
    findingKey({ path: "a.go", line: 4, title: "unsafe transition" }),
    findingKey({ path: "a.go", line: 5, title: "unsafe transition" }),
  );
});

test("the digest separates dispatchable findings by threshold and suppresses confirmed-unfixed", () => {
  const high = finding("unsafe transition", "High", "a.go", 4);
  const low = finding("weak wording", "Low", "b.go", 7);
  const priorKey = findingKey(high);
  const fresh = sweepDigest(digestInput([high, low]));
  assert.equal(fresh.status, "complete");
  assert.equal(fresh.round, 1);
  assert.deepEqual(fresh.aboveThresholdKeys, [priorKey]);
  assert.deepEqual(fresh.belowThresholdKeys, [findingKey(low)]);
  assert.equal(fresh.thresholdIndication, "working");

  const suppressed = sweepDigest(digestInput([high, low], {
    runRecord: {
      round: 2,
      confirmedUnfixed: [{ key: priorKey, finding: high, attempts: 2, reason: "failed twice" }],
    },
  }));
  assert.equal(suppressed.round, 3);
  assert.deepEqual(suppressed.aboveThresholdKeys, []);
  assert.deepEqual(suppressed.candidates.map((entry) => entry.key), [findingKey(low)]);
  assert.deepEqual(suppressed.priorConfirmedUnfixed, [{ key: priorKey, attempts: 2 }]);
  assert.equal(suppressed.thresholdIndication, "terminal");
});

test("the digest reports a reached maximum-rounds ceiling as terminal indication", () => {
  const digest = sweepDigest(digestInput([finding("unsafe transition", "High", "a.go", 4)], {
    maximumRounds: 1,
    runRecord: { round: 1, confirmedUnfixed: [] },
  }));
  assert.equal(digest.round, 2);
  assert.equal(digest.maximumRoundsReached, true);
  assert.equal(digest.thresholdIndication, "terminal");
});

test("the digest fails closed on malformed input", () => {
  for (const [input, reason] of [
    [null, /needs an input object/],
    [{ review: {} }, /confirmedFindings/],
    [digestInput([], { review: { status: "incomplete", confirmedFindings: [] } }), /complete review/],
    [digestInput([], { threshold: "P1" }), /unknown review threshold P1/],
    [digestInput([], { maximumRounds: 0 }), /positive integer/],
  ]) {
    const digest = sweepDigest(input);
    assert.equal(digest.kind, DIGEST_KIND);
    assert.equal(digest.status, "invalid");
    assert.match(digest.reason, reason);
  }
});

test("a coherent working or terminal decision validates against its digest", () => {
  const digest = sweepDigest(digestInput([finding("unsafe transition", "High", "a.go", 4)]));
  assert.deepEqual(validateSweepDecision(decision("working"), digest), { ok: true });
  assert.deepEqual(validateSweepDecision(decision("terminal"), digest), { ok: true });
});

test("judgement may stop the loop with above-threshold findings still on the table", () => {
  const digest = sweepDigest(digestInput([
    finding("unsafe transition", "Critical", "a.go", 4),
    finding("second defect", "High", "b.go", 9),
  ]));
  assert.equal(digest.thresholdIndication, "working");
  assert.deepEqual(validateSweepDecision(decision("terminal", "the findings restate confirmed-unfixed ground"), digest), { ok: true });
});

test("decision validation fails closed on incoherent decisions", () => {
  const digest = sweepDigest(digestInput([finding("unsafe transition", "High", "a.go", 4)]));
  for (const [candidate, reason] of [
    [null, /must be an object/],
    [{ classification: "working", basis: "b" }, new RegExp(`kind ${DECISION_KIND}`)],
    [decision("converged"), /must be working or terminal/],
    [{ kind: DECISION_KIND, classification: "working", basis: "  " }, /non-empty basis/],
  ]) {
    const verdict = validateSweepDecision(candidate, digest);
    assert.equal(verdict.ok, false);
    assert.match(verdict.reason, reason);
  }
});

test("a working decision needs a dispatchable finding and respects the rounds ceiling", () => {
  const key = findingKey(finding("unsafe transition", "High", "a.go", 4));
  const exhausted = sweepDigest(digestInput([finding("unsafe transition", "High", "a.go", 4)], {
    runRecord: {
      round: 1,
      confirmedUnfixed: [{ key, attempts: 2, reason: "failed twice" }],
    },
  }));
  const noCandidates = validateSweepDecision(decision("working"), exhausted);
  assert.equal(noCandidates.ok, false);
  assert.match(noCandidates.reason, /at least one dispatchable finding/);

  const capped = sweepDigest(digestInput([finding("unsafe transition", "High", "a.go", 4)], {
    maximumRounds: 1,
    runRecord: { round: 1, confirmedUnfixed: [] },
  }));
  const overruled = validateSweepDecision(decision("working"), capped);
  assert.equal(overruled.ok, false);
  assert.match(overruled.reason, /maximum rounds has been reached/);
});

test("validation refuses to judge a decision without a complete digest", () => {
  const verdict = validateSweepDecision(decision("working"), sweepDigest(null));
  assert.equal(verdict.ok, false);
  assert.match(verdict.reason, /complete sweep digest/);
});
