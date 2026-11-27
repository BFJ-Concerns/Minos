import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

import { judgementArgs } from "./judgement-stage.mjs";
import { FULL, INCREMENT, NO_REVIEW, stage } from "./judgements/push-inspection.mjs";
import { allowedOutcomes, availableOutcomes, inspectPush } from "./run-steps/inspect-push.mjs";
import { operationRoot } from "./temporary-directory-fixture.mjs";

const target = "1111111111111111111111111111111111111111";
const head = "2222222222222222222222222222222222222222";
const earlierHead = "3333333333333333333333333333333333333333";
const guidance = [{ repository: null, path: "README.md", origin: "checked-in", content: "A library." }];
const laterHead = { head: earlierHead, outcome: "attention", ancestor: true, changedPaths: ["src/parse.go"] };
const env = { MINOS_PROVISIONED_ENGINES: "claude" };

function input(overrides = {}) {
  return { target, head, guidance, earlier: laterHead, outcomes: [NO_REVIEW, INCREMENT, FULL], ...overrides };
}

test("the prompt shows the earlier head and lists only the outcomes this run may choose", () => {
  const { prompt, schema } = judgementArgs(stage, input({ outcomes: [INCREMENT, FULL] }), { env });

  assert.match(prompt, /<earlier-head>\n\{\n  "head": "3{40}",\n  "outcome": "attention",\n  "ancestor": true,\n  "changedPaths": \[\n    "src\/parse\.go"\n  \]\n\}\n<\/earlier-head>/);
  assert.match(prompt, /This run may choose among:\n\n- `increment`\n- `full`\n\nAn outcome missing/);
  assert.deepEqual(schema.properties.outcome.enum, [INCREMENT, FULL], "the answer cannot name an outcome that was ruled out");
});

test("a head with no earlier head is shown as that and nothing more", () => {
  const { prompt } = judgementArgs(stage, input({ earlier: { outcome: "none" }, outcomes: [NO_REVIEW, FULL] }), { env });

  assert.match(prompt, /<earlier-head>\n\{\n  "outcome": "none"\n\}\n<\/earlier-head>/);
});

test("an increment cannot be offered without an earlier head this one descends from", () => {
  for (const earlier of [{ outcome: "none" }, { ...laterHead, ancestor: false }, { ...laterHead, changedPaths: null }])
    assert.match(stage.inputProblem(input({ earlier })), /increment may be offered only with an earlier head/);
  assert.equal(stage.inputProblem(input({ earlier: { ...laterHead, ancestor: false }, outcomes: [NO_REVIEW, FULL] })), null);
});

test("an input missing what the stage is given is refused, naming the part", () => {
  assert.match(stage.inputProblem(input({ head: "" })), /head must be a commit/);
  assert.match(stage.inputProblem(input({ guidance: [] })), /guidance is missing/);
  assert.match(stage.inputProblem(input({ earlier: { ...laterHead, outcome: "working" } })), /earlier\.outcome/);
  assert.match(stage.inputProblem(input({ earlier: { ...laterHead, changedPaths: "src" } })), /earlier\.changedPaths/);
  assert.match(stage.inputProblem(input({ outcomes: [FULL, FULL] })), /outcomes must be/);
  assert.equal(stage.inputProblem(input({ earlier: { ...laterHead, changedPaths: null }, outcomes: [NO_REVIEW, FULL] })), null);
});

test("what is mechanical narrows the outcomes before the stage is asked", () => {
  assert.deepEqual(availableOutcomes({ outcome: "none" }, false), [NO_REVIEW, FULL]);
  assert.deepEqual(availableOutcomes({ outcome: "none" }, true), [FULL]);
  assert.deepEqual(availableOutcomes(laterHead, false), [NO_REVIEW, INCREMENT, FULL]);
  assert.deepEqual(availableOutcomes(laterHead, true), [INCREMENT, FULL]);
  assert.deepEqual(availableOutcomes({ ...laterHead, ancestor: false }, false), [NO_REVIEW, FULL]);
  assert.deepEqual(
    availableOutcomes({ ...laterHead, changedPaths: [] }, true),
    [NO_REVIEW, INCREMENT, FULL],
    "a head whose files are as they were may go unreviewed whatever briefs the repository keeps",
  );
});

test("the repository's setting closes outcomes but never a full review", () => {
  assert.deepEqual(availableOutcomes(laterHead, false, [INCREMENT, FULL]), [INCREMENT, FULL]);
  assert.deepEqual(availableOutcomes(laterHead, false, [FULL]), [FULL]);
  assert.deepEqual(
    availableOutcomes({ ...laterHead, changedPaths: [] }, false, [INCREMENT, FULL]),
    [INCREMENT, FULL],
    "a repository that closes no-review is reviewed even where nothing changed",
  );
  assert.deepEqual(availableOutcomes({ outcome: "none" }, false, [NO_REVIEW, INCREMENT, FULL]), [NO_REVIEW, FULL]);
});

test("the allowed outcomes are read from the run's environment and refused when malformed", () => {
  assert.deepEqual(allowedOutcomes({ MINOS_PUSH_INSPECTION_OUTCOMES: '["no-review","full"]' }), [NO_REVIEW, FULL]);
  assert.throws(() => allowedOutcomes({}), /MINOS_PUSH_INSPECTION_OUTCOMES is required/);
  assert.throws(() => allowedOutcomes({ MINOS_PUSH_INSPECTION_OUTCOMES: "full" }), /not valid JSON/);
  assert.throws(() => allowedOutcomes({ MINOS_PUSH_INSPECTION_OUTCOMES: '["increment"]' }), /full among them/);
  assert.throws(() => allowedOutcomes({ MINOS_PUSH_INSPECTION_OUTCOMES: '["skip","full"]' }), /drawn from/);
});

// A run as the step sees it: a clone holding one guidance document, the
// forge's answer about the earlier head, and a stage that answers as told.
function fixtureRun(t, { earlier, answer, brief = false, diverged, allowed = [NO_REVIEW, INCREMENT, FULL] }) {
  const root = operationRoot(t, "minos-inspect-push-");
  const workspace = join(root, "workspace");
  mkdirSync(join(workspace, "src"), { recursive: true });
  writeFileSync(join(workspace, "README.md"), "A library.\n");
  if (brief) {
    mkdirSync(join(workspace, ".review", "src"), { recursive: true });
    writeFileSync(join(workspace, ".review", "src", "parsing.md"), "Check every parser rejects trailing input.\n");
  }
  let pinnedTarget = target;
  let reviewedHead = head;
  if (diverged) {
    const git = (...args) => execFileSync("git", ["-C", workspace, ...args], { encoding: "utf8" }).trim();
    const commit = (message) => {
      git("add", ".");
      git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "-qm", message);
      return git("rev-parse", "HEAD");
    };
    git("init", "-q");
    writeFileSync(join(workspace, "src", "existing.go"), "package src\n");
    const base = commit("base");
    writeFileSync(join(workspace, diverged.shared), "Identical edit on each branch.\n");
    writeFileSync(join(workspace, diverged.targetOnly), "Target edit only.\n");
    pinnedTarget = commit("target moves");
    git("checkout", "-q", "--detach", base);
    writeFileSync(join(workspace, diverged.shared), "Identical edit on each branch.\n");
    reviewedHead = commit("author independently makes the shared edit");
  }
  const orientation = join(root, "orientation.json");
  writeFileSync(orientation, JSON.stringify({
    repository: workspace,
    head: reviewedHead,
    guidance: [{ source: { path: "README.md" }, location: join(workspace, "README.md"), origin: "checked-in" }],
    misconfigurations: [],
  }));
  const asked = [];
  return {
    asked,
    run: {
      env: { MINOS_PUSH_INSPECTION_OUTCOMES: JSON.stringify(allowed) },
      head: reviewedHead,
      target: pinnedTarget,
      workspace,
      orientation,
      forge: async (...args) => {
        assert.deepEqual(args, ["earlier", reviewedHead, pinnedTarget]);
        return earlier;
      },
      judge: async (judged, given) => {
        asked.push({ judged, given });
        return answer;
      },
    },
  };
}

const forgeEarlier = { head: earlierHead, outcome: "attention", ancestor: true, changed_paths: ["src/parse.go"] };

test("the step asks the stage with what it gathered and reviews an increment from the earlier head", async (t) => {
  const { run, asked } = fixtureRun(t, { earlier: forgeEarlier, answer: { outcome: INCREMENT, reason: "Two new commits rework the parser." } });

  const settled = await inspectPush(run);

  assert.equal(asked.length, 1);
  assert.equal(asked[0].judged, stage);
  assert.equal(stage.inputProblem(asked[0].given), null);
  assert.deepEqual(asked[0].given.earlier, laterHead);
  assert.deepEqual(asked[0].given.outcomes, [NO_REVIEW, INCREMENT, FULL]);
  assert.deepEqual(settled, {
    inspection: { outcome: INCREMENT, reason: "Two new commits rework the parser.", reviewBase: earlierHead },
  });
});

test("a full review is read from the target and a head needing none has no base", async (t) => {
  const full = fixtureRun(t, { earlier: forgeEarlier, answer: { outcome: FULL, reason: "The push rewrites the module." } });
  assert.equal((await inspectPush(full.run)).inspection.reviewBase, target);

  const none = fixtureRun(t, { earlier: forgeEarlier, answer: { outcome: NO_REVIEW, reason: "The push reformats one table." } });
  assert.equal((await inspectPush(none.run)).inspection.reviewBase, null);
});

test("a repository brief that engages what the author changed takes no-review off the list", async (t) => {
  const { run, asked } = fixtureRun(t, {
    earlier: forgeEarlier,
    brief: true,
    answer: { outcome: FULL, reason: "The push rewrites the module." },
  });

  await inspectPush(run);

  assert.deepEqual(asked[0].given.outcomes, [INCREMENT, FULL]);
});

test("the step offers only what the repository allows", async (t) => {
  const { run, asked } = fixtureRun(t, {
    earlier: forgeEarlier,
    allowed: [INCREMENT, FULL],
    answer: { outcome: INCREMENT, reason: "Two new commits rework the parser." },
  });

  await inspectPush(run);

  assert.deepEqual(asked[0].given.outcomes, [INCREMENT, FULL]);
});


test("first and uncomparable heads use the author's paths for push inspection and brief inputs", async (t) => {
  const decisions = [
    { shared: "src/shared.go", targetOnly: "elsewhere.go", engages: true },
    { shared: "elsewhere.go", targetOnly: "src/target.go", engages: false },
  ];
  for (const diverged of decisions) {
    for (const earlier of [{ outcome: "none" }, { ...forgeEarlier, changed_paths: null }]) {
      const { run, asked } = fixtureRun(t, { brief: true, diverged, earlier, answer: { outcome: FULL, reason: "Fixture." } });
      await inspectPush(run);
      assert.deepEqual(asked[0].given.outcomes, diverged.engages ? [FULL] : [NO_REVIEW, FULL]);
      if (earlier.outcome !== "none") continue;

      const env = {
        ...process.env, MINOS_ORIENTATION: run.orientation, MINOS_WORKSPACE: run.workspace,
        MINOS_PROVISIONED_ENGINES: "claude",
      };
      const readInput = (name) => JSON.parse(execFileSync(process.execPath, [
        fileURLToPath(new URL(name, import.meta.url)), run.target, run.head,
      ], { encoding: "utf8", env }));
      assert.deepEqual(readInput("./review-scope-inputs.mjs"), { briefsEngage: diverged.engages });
      const briefInput = readInput("./review-brief-inputs.mjs");
      assert.deepEqual(briefInput.changedPaths, [diverged.shared]);
      assert.equal(briefInput.briefs[0].disposition.triggered === true, diverged.engages);
    }
  }
});
