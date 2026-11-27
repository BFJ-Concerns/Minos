import assert from "node:assert/strict";

// The findings a merge or verifier prompt carries: the JSON array under its
// closing "## The findings" heading.
export function findingsFromVerifierPrompt(prompt) {
  const marker = "## The findings\n";
  const offset = prompt.lastIndexOf(marker);
  assert.notEqual(offset, -1, "the prompt carries a findings array");
  return JSON.parse(prompt.slice(offset + marker.length));
}

// A verdict a verifier could return for a finding, confirmed unless the
// test says otherwise.
export function verdictFor(findingId, overrides = {}) {
  return {
    findingId,
    verdict: "confirmed",
    confidence: 91,
    severity: "High",
    reason: "reproduced",
    outsideFact: "",
    wouldSettle: "",
    ...overrides,
  };
}
