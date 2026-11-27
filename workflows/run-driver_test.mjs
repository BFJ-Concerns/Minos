import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import { chmodSync, existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import test from "node:test";

import { stage as pushInspection } from "./judgements/push-inspection.mjs";
import { CLOSING, COURSE, driveRun, judge, openRun } from "./run-driver.mjs";
import { CHANGES_REQUESTED, CLEAN, HeadMoved, INCOMPLETE, SUPERSEDED, spendsHead } from "./run-endings.mjs";
import { operationRoot } from "./temporary-directory-fixture.mjs";

const quiet = { report: () => {} };

test("the course runs in order, each step handed what the earlier ones settled, and ends on the outcome the last settles", async () => {
  const seen = [];
  const course = [
    async function first(run, settled) { seen.push(["first", settled]); return { base: "abc" }; },
    async function idle(run, settled) { seen.push(["idle", settled]); },
    async function last(run, settled) { seen.push(["last", settled]); return { outcome: CLEAN }; },
  ];
  const closed = [];

  const ending = await driveRun({ id: "run" }, { course, closing: [async (run, settled, end) => closed.push([run, settled, end])] });

  assert.deepEqual(ending, { outcome: CLEAN });
  assert.deepEqual(seen, [["first", {}], ["idle", { base: "abc" }], ["last", { base: "abc" }]]);
  assert.deepEqual(closed, [[{ id: "run" }, { base: "abc", outcome: CLEAN }, { outcome: CLEAN }]]);
});

test("a step that throws ends the run incomplete, naming the step and the cause, and no later step runs", async () => {
  let reached = false;
  const course = [
    async function gather() { return { gathered: true }; },
    async function classify() { throw new Error("stage classification gave no answer"); },
    async function publish() { reached = true; },
  ];
  const closed = [];

  const ending = await driveRun({}, { course, closing: [async (run, settled, end) => closed.push([settled, end])] });

  assert.deepEqual(ending, { outcome: INCOMPLETE, step: "classify", cause: "stage classification gave no answer" });
  assert.equal(reached, false);
  assert.deepEqual(closed, [[{ gathered: true }, ending]], "the closing duties still run, with what was settled before the failure");
});

test("a moved head ends the run superseded and not as the step's failure", async () => {
  const course = [async function claim() { throw new HeadMoved("4444444"); }];

  const ending = await driveRun({}, { course, closing: [] });

  assert.deepEqual(ending, { outcome: SUPERSEDED, step: "claim", cause: "the pull request's head moved to 4444444" });
});

test("a course that settles no outcome has not judged the head", async () => {
  const ending = await driveRun({}, { course: [async function conclude() { return { outcome: "fine" }; }], closing: [] });

  assert.deepEqual(ending, { outcome: INCOMPLETE, step: "conclude", cause: "the course ended without settling an outcome" });
});

test("a closing duty that fails is reported and the duties after it still run", async () => {
  const ran = [];
  const reported = [];
  const closing = [
    async function withdraw() { ran.push("withdraw"); throw new Error("the forge refused the status"); },
    async function recordFailure() { ran.push("recordFailure"); },
  ];

  const ending = await driveRun({}, {
    course: [async () => ({ outcome: CHANGES_REQUESTED })],
    closing,
    report: (message) => reported.push(message),
  });

  assert.deepEqual(ending, { outcome: CHANGES_REQUESTED });
  assert.deepEqual(ran, ["withdraw", "recordFailure"]);
  assert.deepEqual(reported, ["closing duty withdraw failed: the forge refused the status"]);
});

test("only a judged head is spent", () => {
  assert.deepEqual(
    [CLEAN, CHANGES_REQUESTED, INCOMPLETE, SUPERSEDED].map((outcome) => spendsHead({ outcome })),
    [true, true, false, false],
  );
});

test("the shipped course and closing duties are one named function each", () => {
  for (const step of [...COURSE, ...CLOSING]) assert.equal(typeof step, "function");
  const names = [...COURSE, ...CLOSING].map((step) => step.name);
  assert.equal(new Set(names).size, names.length);
  assert.ok(names.every((name) => name !== ""), "an ending names the step that stopped the run");
});

const inspectionInput = {
  target: "1111111111111111111111111111111111111111",
  head: "2222222222222222222222222222222222222222",
  guidance: [{ repository: null, path: "README.md", origin: "checked-in", content: "A library." }],
  earlier: { outcome: "none" },
  outcomes: ["no-review", "full"],
};

test("a judgement writes the stage's arguments, dispatches the one judgement workflow under the stage's name and keeps the record", async (t) => {
  const runDir = operationRoot(t, "minos-judge-");
  const answer = { outcome: "full", reason: "The push adds a parser." };
  const dispatched = [];
  const run = {
    env: { MINOS_PROVISIONED_ENGINES: "claude codex" },
    runDir,
    dispatch: async (...args) => {
      dispatched.push(args);
      assert.equal(JSON.parse(readFileSync(join(runDir, "push-inspection-args.json"), "utf8")).stage, "push-inspection");
      return { stage: "push-inspection", answer };
    },
  };

  assert.deepEqual(await judge(run, pushInspection, inspectionInput), answer);

  assert.deepEqual(dispatched, [["push-inspection", "judgement.js", "run-judgement"]]);
  assert.deepEqual(JSON.parse(readFileSync(join(runDir, "judgements", "push-inspection.json"), "utf8")), {
    kind: "minos-judgement-v1",
    stage: "push-inspection",
    input: inspectionInput,
    answer,
    routing: { engine: "claude", model: "claude-opus-5-5", effort: "medium" },
  });
});

test("a judgement whose answer the stage refuses throws and keeps no record", async (t) => {
  const runDir = operationRoot(t, "minos-judge-");
  const run = {
    env: { MINOS_PROVISIONED_ENGINES: "claude" },
    runDir,
    dispatch: async () => ({ stage: "push-inspection", answer: { outcome: "full", reason: "" } }),
  };

  await assert.rejects(judge(run, pushInspection, inspectionInput), /gave an unusable answer/);
  assert.equal(existsSync(join(runDir, "judgements")), false);
});

// A run whose forge command and stage dispatcher are stand-in scripts, so
// openRun's own forge and dispatch run for real.
function scriptedRun(t, { forgeScript, dispatchScript }) {
  const root = operationRoot(t, "minos-run-driver-");
  const runDir = join(root, "run");
  const workspace = join(root, "workspace");
  const tools = join(root, "run-body");
  for (const directory of [runDir, workspace, tools]) mkdirSync(directory);
  const script = (path, body) => {
    writeFileSync(path, `#!/usr/bin/env sh\n${body}`);
    chmodSync(path, 0o755);
    return path;
  };
  script(join(tools, "dispatch-stage"), dispatchScript);
  return openRun({
    PATH: process.env.PATH,
    MINOS_BIN: script(join(root, "minos"), forgeScript),
    MINOS_SETUP_WORKSPACE: join(tools, "setup-workspace"),
    MINOS_OWNER: "owner",
    MINOS_REPO_NAME: "repository",
    MINOS_PR: "7",
    MINOS_HEAD_SHA: inspectionInput.head,
    MINOS_TARGET_SHA: inspectionInput.target,
    MINOS_RUN_DIR: runDir,
    MINOS_WORKSPACE: workspace,
    MINOS_ORIENTATION: join(runDir, "orientation.json"),
  });
}

test("a run's forge call returns the JSON minos forge printed, and a refused one throws with the result it printed", async (t) => {
  const run = scriptedRun(t, {
    forgeScript: `if [ "$2" = snapshot ]; then printf '{"args":"%s","head_sha":"abc"}\\n' "$*"; else printf '{"outcome":"refused"}\\n'; echo "status refused: head moved" >&2; exit 1; fi\n`,
    dispatchScript: "exit 0\n",
  });

  assert.deepEqual(await run.forge("snapshot"), { args: "forge snapshot", head_sha: "abc" });
  await assert.rejects(run.forge("status", "abc", "def", "clean"), (error) => {
    assert.match(error.message, /minos forge status: status refused: head moved/);
    assert.deepEqual(error.result, { outcome: "refused" });
    return true;
  });
});

test("a run keeps what a successful forge command said on stderr", async (t) => {
  const run = scriptedRun(t, {
    forgeScript: `printf '{"outcome":"applied"}\\n'; [ "$2" = review ] && echo "forge review: inline comments folded into the body: no diff" >&2; exit 0\n`,
    dispatchScript: "exit 0\n",
  });

  await run.forge("status", "abc", "def", "clean");
  await run.forge("review", "abc", "def", "comment", "body.md");
  const note = "minos forge review: forge review: inline comments folded into the body: no diff";
  assert.deepEqual(run.forgeNotes, [note]);
  assert.equal(readFileSync(join(run.runDir, "forge.log"), "utf8"), `${note}\n`);
});

test("a run's dispatch launches the stage from the clone and returns its result, and a stage with none throws", async (t) => {
  const run = scriptedRun(t, {
    forgeScript: "exit 0\n",
    dispatchScript: `[ "$1" = review ] || exit 3\nprintf '{"launched":"%s","from":"%s"}\\n' "$*" "$(pwd)" >"$MINOS_RUN_DIR/$1-result.json"\n`,
  });

  assert.deepEqual(await run.dispatch("review", "review.js"), { launched: "review review.js", from: run.workspace });
  assert.deepEqual(
    await run.dispatch("review", "judgement.js", "run-judgement"),
    { launched: "review judgement.js run-judgement", from: run.workspace },
  );
  await assert.rejects(run.dispatch("review-brief", "review-briefs.js"), /stage review-brief ended without a result; review-brief\.log/);
});

test("a stage the run supervisor stopped fails naming the supervisor's reason", async (t) => {
  const run = scriptedRun(t, { forgeScript: "exit 0\n", dispatchScript: "kill -KILL $$\n" });
  writeFileSync(join(run.runDir, "stage-stopped"), "no run activity for 3600 seconds\n");

  await assert.rejects(run.dispatch("review", "review.js"), (error) => {
    assert.match(error.message, /stage review ended without a result; review\.log has its diagnostics \(stopped by the run supervisor: no run activity for 3600 seconds\)/);
    assert.equal(error.killed, true);
    return true;
  });
});

test("a run cannot open without the coordinates run-body exports", () => {
  assert.throws(() => openRun({ MINOS_BIN: "/usr/local/bin/minos" }), /MINOS_SETUP_WORKSPACE is required/);
});
