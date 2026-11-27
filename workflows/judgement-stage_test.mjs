import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { cpSync, existsSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

import { RECORD_KIND, answerFrom, judgementArgs, judgementRecord, loadStage } from "./judgement-stage.mjs";
import { operationRoot } from "./temporary-directory-fixture.mjs";
import { invokeWorkflow, loadWorkflow } from "./workflow-invocation-fixture.mjs";

const workflowsDir = dirname(fileURLToPath(import.meta.url));
const script = loadWorkflow("judgement.js");

const answerSchema = {
  type: "object",
  additionalProperties: false,
  required: ["choice"],
  properties: { choice: { type: "string" } },
};
const workflowArgs = {
  stage: "sample-stage",
  prompt: "Decide.",
  schema: answerSchema,
  routing: { engine: "claude", model: "claude-opus-5-5", effort: "medium" },
};

// A push-inspection input, the one shipped stage, for the parts of the
// machinery that need a real stage to run.
const inspectionInput = {
  target: "1111111111111111111111111111111111111111",
  head: "2222222222222222222222222222222222222222",
  guidance: [{ repository: null, path: "README.md", origin: "checked-in", content: "A library." }],
  earlier: { outcome: "none" },
  outcomes: ["no-review", "full"],
};

// A tree shaped as the installer leaves it: the shipped workflows beside a
// runtime directory, whose launcher here is a stand-in that answers from
// the arguments file it was handed instead of asking a model.
function installedTree(t, launcherSource) {
  const root = operationRoot(t, "minos-judgement-tree-");
  cpSync(workflowsDir, join(root, "workflows"), { recursive: true });
  mkdirSync(join(root, "runtime"));
  writeFileSync(join(root, "runtime", "ensemble.mjs"), launcherSource);
  return root;
}

const answeringLauncher = `
import { readFileSync } from "node:fs";
const args = JSON.parse(readFileSync(process.argv[3].slice(1), "utf8"));
const answer = process.env.LAUNCHER_ANSWER === undefined ? null : JSON.parse(process.env.LAUNCHER_ANSWER);
process.stdout.write(JSON.stringify({ stage: args.stage, answer }) + "\\n");
`;

test("the judgement workflow sends the prompt it was handed on the stage's routing and returns the answer under the stage's name", async () => {
  const { result, calls } = await invokeWorkflow(script, workflowArgs, () => ({ choice: "this" }));

  assert.deepEqual(result, { stage: "sample-stage", answer: { choice: "this" } });
  assert.equal(calls.length, 1);
  assert.equal(calls[0].prompt, "Decide.");
  assert.deepEqual(calls[0].opts, {
    engine: "claude",
    schema: answerSchema,
    model: "claude-opus-5-5",
    effort: "medium",
    strip: ["skills", "agents"],
    label: "sample-stage",
    phase: "Judge",
  });
});

test("a leg that gave no answer comes back as none, never as a default", async () => {
  const { result } = await invokeWorkflow(script, workflowArgs, () => null);

  assert.deepEqual(result, { stage: "sample-stage", answer: null });
});

test("a workflow input missing its stage, prompt, schema or routing fails before dispatch", async () => {
  for (const missing of ["stage", "prompt", "schema", "routing"]) {
    const { [missing]: omitted, ...args } = workflowArgs;
    let dispatched = false;
    await assert.rejects(invokeWorkflow(script, args, () => { dispatched = true; return {}; }), missing);
    assert.equal(dispatched, false, missing);
  }
});

test("the runner prints a stage's answer and fails a stage that gave none with nothing on standard output", (t) => {
  const root = installedTree(t, answeringLauncher);
  const argsFile = join(root, "args.json");
  writeFileSync(argsFile, JSON.stringify(workflowArgs));
  const runner = (env) => spawnSync(
    join(root, "workflows", "run-judgement"),
    [join(root, "workflows", "judgement.js"), "--json-args", `@${argsFile}`],
    { encoding: "utf8", env: { ...process.env, ...env } },
  );

  const answered = runner({ LAUNCHER_ANSWER: JSON.stringify({ choice: "this" }) });
  assert.equal(answered.status, 0, answered.stderr);
  assert.deepEqual(JSON.parse(answered.stdout), { stage: "sample-stage", answer: { choice: "this" } });

  const unanswered = runner({});
  assert.equal(unanswered.status, 1);
  assert.equal(unanswered.stdout, "");
  assert.match(unanswered.stderr, /stage sample-stage gave no answer/);
});

test("a stage's arguments carry its input, its complete prompt, the answer's schema and the judgement routing", async () => {
  const stage = await loadStage("push-inspection");
  const args = judgementArgs(stage, inspectionInput, { env: { MINOS_PROVISIONED_ENGINES: "claude codex" } });

  assert.equal(args.stage, "push-inspection");
  assert.deepEqual(args.input, inspectionInput);
  assert.doesNotMatch(args.prompt, /\{\{[a-z-]+\}\}/, "no slot is sent unfilled");
  assert.match(args.prompt, new RegExp(inspectionInput.head));
  assert.equal(args.schema.type, "object");
  assert.deepEqual(args.routing, { engine: "claude", model: "claude-opus-5-5", effort: "medium" });
});

test("an input its stage refuses builds no arguments", async () => {
  const stage = await loadStage("push-inspection");

  assert.throws(
    () => judgementArgs(stage, { ...inspectionInput, outcomes: [] }, { env: { MINOS_PROVISIONED_ENGINES: "claude" } }),
    /push-inspection input: outcomes must be a non-empty list/,
  );
});

test("only a name with a definition under workflows/judgements loads as a stage", async () => {
  await assert.rejects(loadStage("no-such-stage"), /no judgement stage is named no-such-stage/);
  await assert.rejects(loadStage("../role-routing"), /is not a judgement stage name/);
});

test("an answer is taken only from its own stage, and only when the stage finds no problem in it", async () => {
  const stage = await loadStage("push-inspection");
  const answer = { outcome: "full", reason: "The push adds a parser." };

  assert.deepEqual(answerFrom(stage, inspectionInput, { stage: "push-inspection", answer }), answer);
  assert.throws(() => answerFrom(stage, inspectionInput, { stage: "push-inspection", answer: null }), /gave no answer/);
  assert.throws(() => answerFrom(stage, inspectionInput, { stage: "another-stage", answer }), /gave no answer/);
  assert.throws(
    () => answerFrom(stage, inspectionInput, { stage: "push-inspection", answer: { outcome: "full", reason: " " } }),
    /gave an unusable answer: the reason is blank/,
  );
});

test("a replay puts a record's input to its stage through the runner and prints the recorded answer beside the new one", async (t) => {
  const root = installedTree(t, answeringLauncher);
  const stage = await loadStage("push-inspection");
  const recorded = { outcome: "full", reason: "The lead reviewed the whole pull request." };
  const recordFile = join(root, "record.json");
  writeFileSync(recordFile, JSON.stringify(judgementRecord(stage, inspectionInput, recorded, null)));
  const workspace = join(root, "clone");
  mkdirSync(workspace);
  const replay = (record, env) => spawnSync(
    process.execPath,
    [join(root, "workflows", "replay-judgement.mjs"), record, workspace],
    { encoding: "utf8", env: { ...process.env, MINOS_PROVISIONED_ENGINES: "claude", ...env } },
  );

  const replayedAnswer = { outcome: "no-review", reason: "The push only reformats a table." };
  const answered = replay(recordFile, { LAUNCHER_ANSWER: JSON.stringify(replayedAnswer) });
  assert.equal(answered.status, 0, answered.stderr);
  assert.deepEqual(JSON.parse(answered.stdout), {
    stage: "push-inspection",
    recorded,
    replayed: replayedAnswer,
    routing: { engine: "claude", model: "claude-opus-5-5", effort: "medium" },
  });

  assert.equal(replay(recordFile, {}).status, 1, "a stage with no answer fails the replay");

  const foreign = join(root, "foreign.json");
  writeFileSync(foreign, JSON.stringify({ stage: "push-inspection", input: inspectionInput }));
  const refused = replay(foreign, {});
  assert.equal(refused.status, 2);
  assert.match(refused.stderr, new RegExp(RECORD_KIND));
});

test("every shipped stage has its prompt, a definition that loads and a row in the stage table", async () => {
  const directory = join(workflowsDir, "judgements");
  const readme = readFileSync(join(directory, "README.md"), "utf8");
  const names = readdirSync(directory).filter((entry) => entry.endsWith(".mjs")).map((entry) => entry.slice(0, -".mjs".length));
  assert.ok(names.length > 0);
  for (const name of names) {
    assert.equal((await loadStage(name)).name, name);
    assert.ok(existsSync(join(directory, `${name}.md`)), `${name} has a prompt`);
    assert.ok(readme.includes(`| \`${name}\` |`), `${name} has a row in the stage table`);
  }
});
