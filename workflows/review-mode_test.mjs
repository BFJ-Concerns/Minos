import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import { existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import test from "node:test";

import { MODES, MODE_SETTINGS, QUICK, REVIEWS, SPECIALIST, modeSettingFromEnvironment } from "./review-mode.mjs";
import { chooseReviewMode } from "./run-steps/choose-review-mode.mjs";

test("the setting is read as one of its values, unset or empty as the specialist review, and anything else refused", () => {
  assert.equal(modeSettingFromEnvironment({}), SPECIALIST);
  assert.equal(modeSettingFromEnvironment({ MINOS_REVIEW_MODE: "" }), SPECIALIST);
  for (const setting of MODE_SETTINGS) assert.equal(modeSettingFromEnvironment({ MINOS_REVIEW_MODE: setting }), setting);
  for (const value of ["Quick", "fast", "general"])
    assert.throws(() => modeSettingFromEnvironment({ MINOS_REVIEW_MODE: value }), /MINOS_REVIEW_MODE must be one of specialist, quick/);
});

test("every mode has an input builder and a workflow beside this module", () => {
  assert.deepEqual(MODES, [SPECIALIST, QUICK]);
  for (const mode of MODES)
    for (const file of [REVIEWS[mode].builder, REVIEWS[mode].workflow])
      assert.ok(existsSync(fileURLToPath(new URL(file, import.meta.url))), `${mode} has ${file}`);
});

test("the step settles the mode a setting names, and nothing for a head that needs no review", async () => {
  const reviewed = { inspection: { reviewBase: "1111111" } };
  assert.deepEqual(await chooseReviewMode({ env: {} }, reviewed), { reviewMode: { mode: SPECIALIST } });
  assert.deepEqual(await chooseReviewMode({ env: { MINOS_REVIEW_MODE: QUICK } }, reviewed), { reviewMode: { mode: QUICK } });
  assert.equal(await chooseReviewMode({ env: { MINOS_REVIEW_MODE: QUICK } }, { inspection: { reviewBase: null } }), undefined);
  await assert.rejects(chooseReviewMode({ env: { MINOS_REVIEW_MODE: "fast" } }, reviewed), /MINOS_REVIEW_MODE must be one of/);
});
