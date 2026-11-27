import assert from "node:assert/strict";
import test from "node:test";

import { findingDisposition, findingGrade } from "./finding-disposition.mjs";

const verdict = (overrides = {}) => ({
  verdict: "confirmed",
  confidence: 90,
  severity: "High",
  reason: "handler.go:41 returns before the lock is released",
  outsideFact: "",
  wouldSettle: "",
  ...overrides,
});

test("a verdict proved from the repository keeps the verifier's word", () => {
  assert.equal(findingDisposition(verdict()), "confirmed");
  assert.equal(findingDisposition(verdict({ verdict: "refuted", severity: "None" })), "refuted");
  assert.equal(findingDisposition(verdict({ verdict: "unsettled", wouldSettle: "run it against a live queue" })), "unsettled");
});

test("a verdict that names an outside fact is unsettled whatever it calls itself", () => {
  const outsideFact = "the service rejects a message without a topic";
  assert.equal(findingDisposition(verdict({ outsideFact })), "unsettled");
  assert.equal(findingDisposition(verdict({ verdict: "refuted", severity: "None", outsideFact })), "unsettled");
  assert.equal(findingDisposition(verdict({ outsideFact: "  " })), "confirmed");
});

test("a finding left standing without a grade has no verdict", () => {
  assert.equal(findingDisposition(verdict({ severity: "None" })), "no-verdict");
  assert.equal(findingDisposition(verdict({ verdict: "unsettled", severity: "None" })), "no-verdict");
});

test("anything short of a whole verdict is no verdict", () => {
  for (const broken of [
    null,
    undefined,
    "confirmed",
    verdict({ verdict: "upheld" }),
    verdict({ confidence: 90.5 }),
    verdict({ confidence: 101 }),
    verdict({ reason: undefined }),
    verdict({ outsideFact: undefined }),
    verdict({ wouldSettle: undefined }),
  ])
    assert.equal(findingDisposition(broken), "no-verdict");
});

test("a finding left standing is graded on the scale, by its proposer where the verifier graded it as refuted", () => {
  const outsideFact = "the service rejects a message without a topic";
  assert.equal(findingGrade(verdict({ severity: "Low" }), "High"), "Low");
  assert.equal(findingGrade(verdict({ verdict: "unsettled", severity: "Medium", wouldSettle: "ask" }), "High"), "Medium");
  assert.equal(findingGrade(verdict({ verdict: "refuted", severity: "None", outsideFact }), "High"), "High");
  assert.equal(findingGrade(verdict({ verdict: "refuted", severity: "None" }), "High"), null);
  assert.equal(findingGrade(verdict({ severity: "None" }), "High"), null);
});
