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
  assert.deepEqual(promptSettingsFromEnvironment({}), { "file-safeguard-requests": "false" });
  assert.deepEqual(promptSettingsFromEnvironment({ MINOS_FILE_SAFEGUARD_REQUESTS: "" }), { "file-safeguard-requests": "false" });
  assert.deepEqual(promptSettingsFromEnvironment({ MINOS_FILE_SAFEGUARD_REQUESTS: "true" }), { "file-safeguard-requests": "true" });
  for (const value of ["yes", "TRUE", "1"])
    assert.throws(() => promptSettingsFromEnvironment({ MINOS_FILE_SAFEGUARD_REQUESTS: value }),
      /MINOS_FILE_SAFEGUARD_REQUESTS must be one of false, true/);
});

test("every setting's value has a non-empty passage for each of its slots", () => {
  for (const { values, slots } of Object.values(PROMPT_SETTINGS))
    for (const slot of slots)
      for (const value of values) assert.notEqual(passage(slot, value), "", `${slot}/${value}.md`);
});

test("the specialists and the brief reviewer read the safeguard-request passage their repository's setting chooses", () => {
  for (const value of ["false", "true"]) {
    const { prompts } = reviewPrompts(repositoryRoot, promptSettingsFromEnvironment({ MINOS_FILE_SAFEGUARD_REQUESTS: value }));
    const other = value === "true" ? "false" : "true";
    for (const [label, text] of [["repository", prompts.repository], ...Object.entries(prompts.specialist)]) {
      assert.ok(text.includes(passage("safeguard-requests", value)), `${label} under ${value} carries ${value}.md`);
      assert.ok(!text.includes(passage("safeguard-requests", other)), `${label} under ${value} leaves out ${other}.md`);
      assert.ok(!text.includes("{{safeguard-requests}}"), `${label} has the slot filled`);
    }
  }
});

test("prompts are refused without a value for every setting, and when a value's passage is missing", (t) => {
  assert.throws(() => reviewPrompts(repositoryRoot, {}), /prompt setting file-safeguard-requests must be one of false, true/);
  assert.throws(() => reviewPrompts(repositoryRoot, { "file-safeguard-requests": "maybe" }), /must be one of false, true/);
  const root = mkdtempSync(join(tmpdir(), "minos-prompt-settings-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  cpSync(promptDirectory, join(root, "workflows", "review-briefs"), { recursive: true });
  rmSync(join(root, "workflows", "review-briefs", "safeguard-requests", "true.md"));
  // A passage no repository has chosen is still found missing.
  assert.throws(() => reviewPrompts(root, promptSettingsFromEnvironment({})), /safeguard-requests\/true\.md/);
  assert.throws(() => settingSections(() => { throw new Error("unreadable"); }, promptSettingsFromEnvironment({})), /unreadable/);
});
