import assert from "node:assert/strict";

// The JSON array a brief-planner prompt closes on: the one under its last
// heading, after the line that describes it.
function closingArray(prompt, heading) {
  const offset = prompt.lastIndexOf(`${heading}\n`);
  assert.notEqual(offset, -1, `the prompt carries ${heading}`);
  const start = prompt.indexOf("\n[", offset);
  assert.notEqual(start, -1, `${heading} holds a JSON array`);
  return JSON.parse(prompt.slice(start + 1));
}

// The briefs a relevance prompt asks about, each `{ brief, relevance }`.
export function relevanceConditionsFromPrompt(prompt) {
  return closingArray(prompt, "## The briefs");
}

// The paths a partition prompt asks to divide.
export function fileInventoryFromPrompt(prompt) {
  return closingArray(prompt, "## The files");
}
