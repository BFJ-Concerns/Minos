import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import { existsSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

import { stage as reviewModeStage } from "./judgements/review-mode.mjs";
import { AUTOMATIC, MODES, MODE_SETTINGS, QUICK, REVIEWS, SPECIALIST, modeSettingFromEnvironment } from "./review-mode.mjs";
import { chooseReviewMode } from "./run-steps/choose-review-mode.mjs";
import { HeadMoved } from "./run-endings.mjs";
import { operationRoot } from "./temporary-directory-fixture.mjs";

test("the setting is read as one of its values, unset or empty as the specialist review, and anything else refused", () => {
  assert.equal(modeSettingFromEnvironment({}), SPECIALIST);
  assert.equal(modeSettingFromEnvironment({ MINOS_REVIEW_MODE: "" }), SPECIALIST);
  for (const setting of MODE_SETTINGS) assert.equal(modeSettingFromEnvironment({ MINOS_REVIEW_MODE: setting }), setting);
  for (const value of ["Quick", "fast", "general"])
    assert.throws(() => modeSettingFromEnvironment({ MINOS_REVIEW_MODE: value }), /MINOS_REVIEW_MODE must be one of specialist, quick, automatic/);
  assert.equal(modeSettingFromEnvironment({ MINOS_REVIEW_MODE: AUTOMATIC }), AUTOMATIC);
});

test("every mode has an input builder and a workflow beside this module", () => {
  assert.deepEqual(MODES, [SPECIALIST, QUICK]);
  for (const mode of MODES)
    for (const file of [REVIEWS[mode].builder, REVIEWS[mode].workflow])
      assert.ok(existsSync(fileURLToPath(new URL(file, import.meta.url))), `${mode} has ${file}`);
});

test("the step settles the mode a setting names, and nothing for a head that needs no review", async () => {
  const reviewed = { inspection: { reviewBase: "1111111" } };
  const asked = [];
  const judge = async (stage) => { asked.push(stage.name); return { mode: QUICK, reason: "r" }; };
  assert.deepEqual(await chooseReviewMode({ env: {}, judge }, reviewed), { reviewMode: { mode: SPECIALIST, setting: SPECIALIST } });
  assert.deepEqual(await chooseReviewMode({ env: { MINOS_REVIEW_MODE: QUICK }, judge }, reviewed), { reviewMode: { mode: QUICK, setting: QUICK } });
  assert.deepEqual(asked, [], "a setting that names a mode asks no stage");
  assert.equal(await chooseReviewMode({ env: { MINOS_REVIEW_MODE: QUICK } }, { inspection: { reviewBase: null } }), undefined);
  await assert.rejects(chooseReviewMode({ env: { MINOS_REVIEW_MODE: "fast" } }, reviewed), /MINOS_REVIEW_MODE must be one of/);
});

// A run as the step sees it under `automatic`: an orientation to build the
// stage's input from, and a judge answering with `answer`, or throwing it.
function automaticRun(t, answer, { carried } = {}) {
  const runDir = operationRoot(t, "minos-review-mode-");
  const orientation = join(runDir, "orientation.json");
  writeFileSync(orientation, JSON.stringify({
    guidance: [{ source: { path: "AGENTS.md" }, location: join(runDir, "AGENTS.md"), origin: "checked-in" }],
    misconfigurations: [],
  }));
  writeFileSync(join(runDir, "AGENTS.md"), "A parser library.\n");
  if (carried) writeFileSync(join(runDir, "carried-review-result.json"), JSON.stringify(carried));
  const asked = [];
  return {
    asked,
    run: {
      env: { MINOS_REVIEW_MODE: AUTOMATIC }, head: "2222222", runDir, orientation,
      judge: async (stage, input) => {
        asked.push({ stage: stage.name, input });
        if (answer instanceof Error) throw answer;
        return answer;
      },
    },
  };
}

test("under automatic the step asks the review-mode stage about the range the review will read, and carries its choice out", async (t) => {
  const { run, asked } = automaticRun(t, { mode: QUICK, reason: "A comment in a test helper." });
  assert.deepEqual(await chooseReviewMode(run, { inspection: { reviewBase: "1111111" } }), {
    reviewMode: { mode: QUICK, setting: AUTOMATIC, chosen: "A comment in a test helper." },
  });
  assert.deepEqual(asked.map(({ stage, input }) => [stage, input.target, input.head]), [["review-mode", "1111111", "2222222"]]);
  assert.equal(reviewModeStage.inputProblem(asked[0].input), null);
});

test("under automatic a carried verdict for this head and base settles the mode without asking", async (t) => {
  const reviewed = { head: "2222222", target: "1111111" };
  const { run, asked } = automaticRun(t, { mode: SPECIALIST, reason: "r" }, { carried: { mode: QUICK, reviewed } });
  assert.deepEqual(await chooseReviewMode(run, { inspection: { reviewBase: "1111111" } }), {
    reviewMode: { mode: QUICK, setting: AUTOMATIC, carried: true },
  });
  assert.deepEqual(asked, []);

  // A carried verdict from another base is not this head's review.
  const stale = automaticRun(t, { mode: SPECIALIST, reason: "r" }, { carried: { mode: QUICK, reviewed: { ...reviewed, target: "0000000" } } });
  assert.equal((await chooseReviewMode(stale.run, { inspection: { reviewBase: "1111111" } })).reviewMode.mode, SPECIALIST);
  assert.equal(stale.asked.length, 1);
});

test("under automatic a stage with no usable answer leaves the head the specialist review, and a moved head still ends the run", async (t) => {
  const { run } = automaticRun(t, new Error("the reason is blank"));
  assert.deepEqual(await chooseReviewMode(run, { inspection: { reviewBase: "1111111" } }), {
    reviewMode: { mode: SPECIALIST, setting: AUTOMATIC, problem: "the reason is blank" },
  });
  const moved = automaticRun(t, new HeadMoved("head moved"));
  await assert.rejects(chooseReviewMode(moved.run, { inspection: { reviewBase: "1111111" } }), HeadMoved);
});

test("the review-mode stage answers with a mode and a reason, and refuses a blank reason", () => {
  const schema = reviewModeStage.answerSchema({});
  assert.deepEqual(schema.properties.mode.enum, MODES);
  assert.equal(reviewModeStage.answerProblem({ mode: QUICK, reason: "A comment." }), null);
  assert.match(reviewModeStage.answerProblem({ mode: QUICK, reason: " " }), /reason is blank/);
  assert.match(reviewModeStage.inputProblem({ target: "1", head: "" }), /head must be a commit/);
});
