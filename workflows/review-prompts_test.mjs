import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import { cpSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

import { promptSettingsFromEnvironment } from "./prompt-settings.mjs";
import { STAGE_RUN_SLOTS, parseConcern, reviewPrompts, slotNames } from "./review-prompts.mjs";

const repositoryRoot = fileURLToPath(new URL("..", import.meta.url));
// Every prompt setting as it stands when a repository sets none.
const unset = promptSettingsFromEnvironment({});

// A copy of the shipped prompts a test may break.
function promptCopy(t) {
  const root = mkdtempSync(join(tmpdir(), "minos-review-prompts-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  cpSync(join(repositoryRoot, "workflows", "review-briefs"), join(root, "workflows", "review-briefs"), { recursive: true });
  return root;
}

test("every shipped prompt has its file slots filled and carries only its stage's run slots", () => {
  const { concerns, prompts } = reviewPrompts(repositoryRoot, unset);
  assert.deepEqual(concerns.map(({ name }) => name), ["failure", "fit", "logic", "security", "tests"]);
  assert.ok(concerns.every(({ summary }) => summary.length > 0));
  const stages = [
    ["exploration", prompts.exploration],
    ["repository", prompts.repository],
    ["brief-relevance", prompts.briefRelevance],
    ["brief-partition", prompts.briefPartition],
    ...Object.values(prompts.assignment).map((text) => ["assignment", text]),
    ["merge", prompts.merge],
    ["verifier", prompts.verifier],
    ...Object.values(prompts.specialist).map((text) => ["specialist", text]),
    ["general", prompts.general],
    ...Object.values(prompts.generalAssignment).map((text) => ["general-assignment", text]),
  ];
  for (const [stage, text] of stages) {
    for (const file of ["reviewed-change", "what-makes-a-finding", "writing-a-finding", "severity-scale", "concern"])
      assert.ok(!text.includes(`{{${file}}}`), `${stage} has ${file} filled`);
    assert.ok(slotNames(text).every((name) => STAGE_RUN_SLOTS[stage].includes(name)), stage);
  }
  assert.match(prompts.specialist.security, /## Your concern: \{\{concern-name\}\}\n\n/);
  assert.doesNotMatch(prompts.specialist.security, /^---$/m, "a concern's front matter stays out of its prompt");
  assert.match(prompts.verifier, /- `Critical`: ordinary use loses or corrupts data/);
  assert.deepEqual(Object.keys(prompts.assignment), ["changedPart", "wholePart", "unit", "files"]);
  assert.deepEqual(Object.keys(prompts.generalAssignment), ["wholeChange", "share"]);
  for (const text of [prompts.general, ...Object.values(prompts.specialist)])
    assert.match(text, /## What makes a finding\n\nA finding is something this change gets wrong/, "the proposers of a change share one account of a finding");
  for (const text of [prompts.briefRelevance, prompts.briefPartition])
    assert.match(text, /## The repository and the change\n/, "the brief-planner prompts open on the reviewed change");
});

test("a prompt carrying a slot no stage fills is refused when the input is built", (t) => {
  const root = promptCopy(t);
  writeFileSync(join(root, "workflows", "review-briefs", "merge.md"), "Group these.\n\n{{findings}}\n{{project-documents}}\n");
  assert.throws(() => reviewPrompts(root, unset), /merge\.md carries slots its stage never fills: project-documents/);
});

test("a concern file needs exactly a summary in its front matter and a body", () => {
  assert.deepEqual(parseConcern("x", "---\nsummary: whether it works\n---\nBody.\n"), { name: "x", summary: "whether it works", body: "Body." });
  assert.throws(() => parseConcern("x", "Body only."), /no front matter/);
  assert.throws(() => parseConcern("x", "---\nsummary: s\nowner: me\n---\nBody."), /exactly a non-empty summary/);
  assert.throws(() => parseConcern("x", "---\nsummary: s\n---\n"), /has no body/);
});

test("the repository prompt keeps a whole-scope brief's older defects in findings, not observations", () => {
  const { prompts } = reviewPrompts(repositoryRoot, unset);
  const observationLine = prompts.repository.split("\n").find((line) => line.startsWith("- `outOfScopeObservations`:"));
  assert.ok(observationLine, "the repository prompt says what an observation is");
  assert.match(observationLine, /the change did not introduce, other than those an assignment of a whole part puts in `findings`/);
  assert.match(prompts.repository, /whether the change introduced a fault does not decide where you report it: it goes in `findings` either way/);
});
