import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import { cpSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

import { PROMPT_SETTINGS, promptSettingsFromEnvironment, settingSections } from "./prompt-settings.mjs";
import { reviewPrompts } from "./review-prompts.mjs";

const repositoryRoot = fileURLToPath(new URL("..", import.meta.url));
const promptDirectory = join(repositoryRoot, "workflows", "review-briefs");
const passage = (slot, value) => readFileSync(join(promptDirectory, slot, `${value}.md`), "utf8").trim();

test("a setting's variable is read as one of its values, unset or empty as its default, and anything else refused", () => {
  const defaults = { "file-safeguard-requests": "false", depth: "standard" };
  assert.deepEqual(promptSettingsFromEnvironment({}), defaults);
  assert.deepEqual(promptSettingsFromEnvironment({ MINOS_FILE_SAFEGUARD_REQUESTS: "", MINOS_REVIEW_DEPTH: "" }), defaults);
  assert.deepEqual(promptSettingsFromEnvironment({ MINOS_FILE_SAFEGUARD_REQUESTS: "true", MINOS_REVIEW_DEPTH: "thorough" }),
    { "file-safeguard-requests": "true", depth: "thorough" });
  for (const value of ["yes", "TRUE", "1"])
    assert.throws(() => promptSettingsFromEnvironment({ MINOS_FILE_SAFEGUARD_REQUESTS: value }),
      /MINOS_FILE_SAFEGUARD_REQUESTS must be one of false, true/);
  for (const value of ["deep", "Standard", "0"])
    assert.throws(() => promptSettingsFromEnvironment({ MINOS_REVIEW_DEPTH: value }),
      /MINOS_REVIEW_DEPTH must be one of light, standard, thorough/);
});

test("each of a setting's slots reads differently under each of its values", () => {
  for (const { values, slots } of Object.values(PROMPT_SETTINGS))
    for (const slot of slots)
      assert.equal(new Set(values.map((value) => passage(slot, value))).size, values.length, `${slot} passages differ`);
});

test("the specialists, the general reviewer, the brief reviewer and the verifier read the safeguard-request passages their repository's setting chooses", () => {
  for (const value of ["false", "true"]) {
    const { prompts } = reviewPrompts(repositoryRoot, promptSettingsFromEnvironment({ MINOS_FILE_SAFEGUARD_REQUESTS: value }));
    const other = value === "true" ? "false" : "true";
    for (const [label, text] of [["repository", prompts.repository], ["general", prompts.general], ...Object.entries(prompts.specialist)]) {
      assert.ok(text.includes(passage("safeguard-requests", value)), `${label} under ${value} carries ${value}.md`);
      assert.ok(!text.includes(passage("safeguard-requests", other)), `${label} under ${value} leaves out ${other}.md`);
      assert.ok(!text.includes("{{safeguard-requests}}"), `${label} has the slot filled`);
    }
    const chosen = passage("verifier-safeguard-requests", value);
    const unchosen = passage("verifier-safeguard-requests", other);
    if (chosen !== "") assert.ok(prompts.verifier.includes(chosen), `the verifier under ${value} carries ${value}.md`);
    if (unchosen !== "") assert.ok(!prompts.verifier.includes(unchosen), `the verifier under ${value} leaves out ${other}.md`);
    assert.ok(!prompts.verifier.includes("{{verifier-safeguard-requests}}"), "the verifier has the slot filled");
  }
});

// Depth reaches the plan and the reading of the reviewers of a change,
// specialist and general, and nothing else: the prompts that judge, merge
// or filter findings read the same at every depth.
test("the exploration leg, the specialists and the general reviewer read the depth passages their repository's setting chooses, and no other prompt changes", () => {
  const depths = PROMPT_SETTINGS.depth.values;
  const under = Object.fromEntries(depths.map((depth) =>
    [depth, reviewPrompts(repositoryRoot, promptSettingsFromEnvironment({ MINOS_REVIEW_DEPTH: depth })).prompts]));
  for (const depth of depths) {
    const { exploration, specialist, general } = under[depth];
    for (const [slot, texts] of [
      ["exploration-depth", [["exploration", exploration]]],
      ["specialist-depth", Object.entries(specialist)],
      ["general-depth", [["general", general]]],
    ]) {
      for (const [label, text] of texts) {
        assert.ok(!text.includes(`{{${slot}}}`), `${label} has the slot filled`);
        for (const other of depths) {
          const chosen = passage(slot, other);
          if (chosen === "") continue;
          assert.equal(text.includes(chosen), other === depth, `${label} under ${depth} ${other === depth ? "carries" : "leaves out"} ${slot}/${other}.md`);
        }
      }
    }
    for (const stage of ["repository", "briefRelevance", "briefPartition", "assignment", "generalAssignment", "merge", "verifier"]) {
      assert.ok(under[depth][stage] !== undefined, `the prompts carry ${stage}`);
      assert.deepEqual(under[depth][stage], under.standard[stage], `${stage} reads the same under ${depth}`);
    }
  }
});

test("prompts are refused without a value for every setting, and when a value's passage is missing", (t) => {
  assert.throws(() => reviewPrompts(repositoryRoot, {}), /prompt setting file-safeguard-requests must be one of false, true/);
  assert.throws(() => reviewPrompts(repositoryRoot, { "file-safeguard-requests": "maybe", depth: "standard" }), /must be one of false, true/);
  assert.throws(() => reviewPrompts(repositoryRoot, { "file-safeguard-requests": "false" }), /prompt setting depth must be one of light, standard, thorough/);
  const root = mkdtempSync(join(tmpdir(), "minos-prompt-settings-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  cpSync(promptDirectory, join(root, "workflows", "review-briefs"), { recursive: true });
  rmSync(join(root, "workflows", "review-briefs", "safeguard-requests", "true.md"));
  // A passage no repository has chosen is still found missing.
  assert.throws(() => reviewPrompts(root, promptSettingsFromEnvironment({})), /safeguard-requests\/true\.md/);
  assert.throws(() => settingSections(() => { throw new Error("unreadable"); }, promptSettingsFromEnvironment({})), /unreadable/);
});

// The classification stage and the lead are given no prompt setting: the
// class a finding takes is the same under either value, and only the
// composer routes it. Their text has to be true under both values.
test("text that takes no safeguard-request passage says where a classed request goes under either value", () => {
  const read = (path) => readFileSync(join(repositoryRoot, path), "utf8").replace(/\s+/g, " ");
  for (const path of ["workflows/judgements/verdict-classification.md", "lifecycle/lifecycle.md"]) {
    const text = read(path);
    for (const unconditional of [/posted and filed nowhere/, /published and filed nowhere/, /kept off the filing destination/])
      assert.doesNotMatch(text, unconditional, `${path} says a classed request is never filed`);
  }
  assert.match(read("workflows/judgements/verdict-classification.md"),
    /By default it is filed nowhere\. A repository that has asked for its safeguard requests has each filed apart from the defects/);
  assert.match(read("lifecycle/lifecycle.md"), /`\$MINOS_FILE_SAFEGUARD_REQUESTS` is `true`: there the composer/);
});
