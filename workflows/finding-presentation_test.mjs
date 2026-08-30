import test from "node:test";
import assert from "node:assert/strict";

import { REVIEW_BODIES, findingComment } from "./finding-presentation.mjs";

// The presentation module is the single authority for review-payload
// text. These tests pin the author-facing contract: a payload carries the
// finding — title, explanation, severity, location — and none of the
// run's internals.

test("a finding comment carries the finding, not the run's internals", () => {
  const comment = findingComment({
    id: "specialist-2-gpt:1",
    source: "Does the change handle concurrent writers safely?",
    title: "Lost update on concurrent write",
    explanation: "Two writers read the same revision and the later write wins silently.",
    severity: "High",
    confidence: 62,
    verifierConfidence: 55,
    combinedConfidence: 58,
    path: "internal/store.go",
    line: 41,
    proposingLabel: "specialist-2-gpt",
    verifyLabel: "verify-1-claude",
  });
  assert.equal(comment.path, "internal/store.go");
  assert.equal(comment.line, 41);
  assert.equal(
    comment.body,
    "**Lost update on concurrent write**\n\n" +
      "Two writers read the same revision and the later write wins silently.\n\n" +
      "Severity: High.",
  );
  // The exact-body assertion above is the contract; these name the leaks
  // it exists to prevent, so a loosened rendering fails with the leak's
  // own name.
  for (const leaked of [
    "Does the change handle",
    "62",
    "55",
    "onfidence",
    "specialist-2-gpt",
    "verify-1-claude",
  ]) {
    assert.ok(!comment.body.includes(leaked), `payload leaked ${leaked}`);
  }
});

test("the review body literals are one frozen authority", () => {
  assert.deepEqual(REVIEW_BODIES, {
    findings: "Confirmed findings in the reviewed code.",
    clean: "No confirmed findings in the reviewed code.",
    brief: "Repository review brief findings.",
    unresolved: "Confirmed code findings remain unresolved.",
  });
  assert.ok(Object.isFrozen(REVIEW_BODIES));
});
