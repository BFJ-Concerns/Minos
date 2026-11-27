import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { cpSync, existsSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

import { RECORD_KIND, answerFrom, judgementArgs, judgementRecord, loadStage } from "./judgement-stage.mjs";
import { openRun } from "./run-driver.mjs";
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
import { invokeWorkflow, loadWorkflow } from "../workflows/workflow-invocation-fixture.mjs";
const args = JSON.parse(readFileSync(process.argv[3].slice(1), "utf8"));
const answer = process.env.LAUNCHER_ANSWER === undefined ? null : JSON.parse(process.env.LAUNCHER_ANSWER);
const { result } = await invokeWorkflow(loadWorkflow("judgement.js"), args, () => ({
  identityEnvelope: {
    output: answer,
    failure: answer ? null : { kind: "session-lost", message: "the worker session ended" },
    answeredBy: answer ? { engine: "codex", model: "gpt-6.1-sol" } : null,
  },
}));
process.stdout.write(JSON.stringify(result) + "\\n");
`;

test("the judgement workflow sends the prompt it was handed on the stage's routing and returns the answer under the stage's name", async () => {
  const { result, calls } = await invokeWorkflow(script, workflowArgs, () => ({ choice: "this" }));

  assert.deepEqual(result, { stage: "sample-stage", answer: { choice: "this" }, answeredBy: null, failure: null });
  assert.equal(calls.length, 1);
  assert.equal(calls[0].prompt, "Decide.");
  assert.deepEqual(calls[0].opts, {
    engine: "claude",
    schema: answerSchema,
    model: "claude-opus-5-5",
    effort: "medium",
    strip: ["skills", "agents"],
    identity: true,
    label: "sample-stage",
    phase: "Judge",
  });
});

test("the judgement workflow names the model that answered, which may be another than the one routed", async () => {
  const { result } = await invokeWorkflow(script, workflowArgs, (label) => ({
    identityEnvelope: {
      label, phase: "Judge", output: { choice: "this" }, failure: null,
      answeredBy: { engine: "codex", model: "gpt-6.1-sol", family: "gpt" },
    },
  }));

  assert.deepEqual(result, { stage: "sample-stage", answer: { choice: "this" }, answeredBy: "gpt-6.1-sol", failure: null });
});

test("a failed leg carries the runtime's failure reason", async () => {
  const { result } = await invokeWorkflow(script, workflowArgs, () => null);

  assert.deepEqual(result, { stage: "sample-stage", answer: null, answeredBy: null, failure: "worker exhausted" });
});

test("a workflow input missing its stage, prompt, schema or routing fails before dispatch", async () => {
  for (const missing of ["stage", "prompt", "schema", "routing"]) {
    const { [missing]: omitted, ...args } = workflowArgs;
    let dispatched = false;
    await assert.rejects(invokeWorkflow(script, args, () => { dispatched = true; return {}; }), missing);
    assert.equal(dispatched, false, missing);
  }
});

test("the runner prints a stage's answer and fails a stage that gave none with nothing on standard output", async (t) => {
  const stage = await loadStage("push-inspection");
  const root = installedTree(t, answeringLauncher);
  const argsFile = join(root, "args.json");
  writeFileSync(argsFile, JSON.stringify(judgementArgs(stage, inspectionInput, { env: { MINOS_PROVISIONED_ENGINES: "claude" } })));
  const runner = (env) => spawnSync(
    join(root, "workflows", "run-judgement"),
    [join(root, "workflows", "judgement.js"), "--json-args", `@${argsFile}`],
    { encoding: "utf8", env: { ...process.env, ...env } },
  );

  const answer = { outcome: "full", reason: "The push changes a parser." };
  const answered = runner({ LAUNCHER_ANSWER: JSON.stringify(answer) });
  assert.equal(answered.status, 0, answered.stderr);
  assert.deepEqual(JSON.parse(answered.stdout), { stage: "push-inspection", answer, answeredBy: "gpt-6.1-sol" });

  const unanswered = runner({});
  assert.equal(unanswered.status, 1);
  assert.equal(unanswered.stdout, "");
  assert.match(unanswered.stderr, /stage push-inspection failed: the worker session ended/);

  const invalid = runner({ LAUNCHER_ANSWER: JSON.stringify({ ...answer, reason: " " }) });
  assert.equal(invalid.status, 1);
  assert.equal(invalid.stdout, "");
  assert.match(invalid.stderr, /stage push-inspection gave an unusable answer: the reason is blank/);
});

test("the runner rejects an invalid recorded input before launching the workflow", async (t) => {
  const stage = await loadStage("push-inspection");
  const launched = join(operationRoot(t, "minos-judgement-launch-"), "launched");
  const root = installedTree(t, `
    import { writeFileSync } from "node:fs";
    writeFileSync(${JSON.stringify(launched)}, "yes");
  `);
  const args = judgementArgs(stage, inspectionInput, { env: { MINOS_PROVISIONED_ENGINES: "claude" } });
  const argsFile = join(root, "args.json");
  writeFileSync(argsFile, JSON.stringify({ ...args, input: { ...inspectionInput, head: "" } }));
  const result = spawnSync(join(root, "workflows", "run-judgement"),
    [join(root, "workflows", "judgement.js"), "--json-args", `@${argsFile}`], { encoding: "utf8" });
  assert.equal(result.status, 1);
  assert.equal(result.stdout, "");
  assert.match(result.stderr, /push-inspection input:/);
  assert.equal(existsSync(launched), false);
});

test("dispatch records a failed stage and publishes no result for a semantically invalid answer", async (t) => {
  const root = installedTree(t, answeringLauncher);
  const stage = await loadStage("push-inspection");
  const env = {
    ...process.env,
    MINOS_PROVISIONED_ENGINES: "claude",
    MINOS_RUN_DIR: root,
    MINOS_REVIEW_WORKFLOW: join(root, "workflows", "review.js"),
    MINOS_SETUP_WORKSPACE: fileURLToPath(new URL("../scripts/run-body/setup-workspace", import.meta.url)),
    LAUNCHER_ANSWER: JSON.stringify({ outcome: "full", reason: " " }),
  };
  writeFileSync(join(root, "push-inspection-args.json"), JSON.stringify(judgementArgs(stage, inspectionInput, { env })));
  const dispatch = spawnSync(join(dirname(env.MINOS_SETUP_WORKSPACE), "dispatch-stage"),
    ["push-inspection", "judgement.js", "run-judgement"], { encoding: "utf8", env });
  assert.equal(dispatch.status, 1, dispatch.stderr);
  assert.equal(existsSync(join(root, "push-inspection-result.json")), false);
  assert.equal(existsSync(join(root, "push-inspection-result.done")), true);
  assert.equal(JSON.parse(readFileSync(join(root, "timings.ndjson"), "utf8")).exit_status, 1);
  assert.match(readFileSync(join(root, "push-inspection.log"), "utf8"), /gave an unusable answer/);
});

test("the driver records the answering model and gets a failed leg's reason through the shipped runner", async (t) => {
  const root = installedTree(t, answeringLauncher);
  const stage = await loadStage("push-inspection");
  const run = openRun({
    PATH: process.env.PATH,
    MINOS_BIN: "unused",
    MINOS_OWNER: "owner", MINOS_REPO_NAME: "repo", MINOS_PR: "1",
    MINOS_HEAD_SHA: inspectionInput.head, MINOS_TARGET_SHA: inspectionInput.target,
    MINOS_RUN_DIR: root, MINOS_WORKSPACE: root, MINOS_ORIENTATION: "unused",
    MINOS_PROVISIONED_ENGINES: "claude",
    MINOS_REVIEW_WORKFLOW: join(root, "workflows", "review.js"),
    MINOS_SETUP_WORKSPACE: fileURLToPath(new URL("../scripts/run-body/setup-workspace", import.meta.url)),
    LAUNCHER_ANSWER: JSON.stringify({ outcome: "full", reason: "The push adds a parser." }),
  });

  assert.deepEqual(await run.judge(stage, inspectionInput), JSON.parse(run.env.LAUNCHER_ANSWER));
  assert.equal(run.judgementModels.get(stage.name), "gpt-6.1-sol");
  const record = JSON.parse(readFileSync(join(root, "judgements", "push-inspection.json"), "utf8"));
  assert.equal(record.routing.model, "claude-opus-5-5");
  assert.equal(record.answeredBy, "gpt-6.1-sol");

  delete run.env.LAUNCHER_ANSWER;
  await assert.rejects(run.judge(stage, inspectionInput), /stage push-inspection failed: the worker session ended/);
  assert.equal(existsSync(join(root, "push-inspection@2-result.json")), false);
  assert.equal(existsSync(join(root, "judgements", "push-inspection@2.json")), false);
});

test("the walkthrough stage requires every changed path and distinct flows", async () => {
  const stage = await loadStage("walkthrough");
  const files = ["parser.go", "parser_test.go", " "];
  const flow = { title: "Parse", trace: "The client asks the parser." };
  const answer = {
    overview: "The parser changes.",
    groups: [{ title: "Parser", summary: "Token handling is expanded.", files }],
    flows: [flow],
  };
  const input = {
    target: inspectionInput.target, head: inspectionInput.head, files, diagrams: true,
    earlier: { head: inspectionInput.head, overview: answer.overview, groups: answer.groups },
  };

  const args = judgementArgs(stage, input, { env: { MINOS_PROVISIONED_ENGINES: "claude codex" } });
  assert.ok(args.prompt.includes(`git diff ${input.target}...${input.head}`));
  assert.ok(args.prompt.includes(`<earlier-walkthrough head="${input.head}">`));
  assert.ok(args.prompt.includes(answer.groups[0].summary));
  assert.ok(args.schema.required.includes("flows"));
  const result = { stage: stage.name, answer };
  assert.equal(answerFrom(stage, input, result), answer);
  assert.throws(() => answerFrom(stage, input, {
    ...result, answer: { ...answer, groups: [{ ...answer.groups[0], files: files.slice(0, 1) }] },
  }), /no group names "parser_test.go"/);
  assert.throws(() => answerFrom(stage, input, {
    ...result, answer: { ...answer, flows: [flow, flow] },
  }), /two flows share a title/);
});

test("the diagrams stage requires an answer for each requested flow once", async () => {
  const stage = await loadStage("walkthrough-diagrams");
  const flows = [{ title: "Parse", trace: "The client asks the parser." }, { title: "Render", trace: "The parser renders tokens." }];
  const input = { target: inspectionInput.target, head: inspectionInput.head, flows };
  const args = judgementArgs(stage, input, { env: { MINOS_PROVISIONED_ENGINES: "claude codex" } });
  assert.ok(args.prompt.includes(flows[0].trace));
  assert.deepEqual(args.schema.required, ["diagrams"]);

  const diagrams = flows.map(({ title }) => ({ flow: title, participants: [], steps: [], undrawn: "The trace is indeterminate." }));
  const result = { stage: stage.name, answer: { diagrams } };
  assert.equal(answerFrom(stage, input, result), result.answer);
  assert.throws(() => answerFrom(stage, input, {
    ...result, answer: { diagrams: [diagrams[0], diagrams[0]] },
  }), /a flow is answered twice/);
  assert.throws(() => answerFrom(stage, input, {
    ...result, answer: { diagrams: [diagrams[0]] },
  }), /the flow "Render" has no entry/);
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
