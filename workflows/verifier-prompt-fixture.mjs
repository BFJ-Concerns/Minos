import assert from "node:assert/strict";

export function findingsFromVerifierPrompt(prompt) {
  const marker = "Findings: ";
  const offset = prompt.lastIndexOf(marker);
  assert.notEqual(offset, -1, "verifier prompt carries a findings array");
  return JSON.parse(prompt.slice(offset + marker.length));
}

