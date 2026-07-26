import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const workflowsDir = dirname(fileURLToPath(import.meta.url));
const setupPath = join(workflowsDir, "setup.js");
const setupSource = readFileSync(setupPath, "utf8");
const lifecycle = readFileSync(join(workflowsDir, "..", "lifecycle", "lifecycle.md"), "utf8");
const setupBody = setupSource.replace(/^export const meta =/m, "const meta =");
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
const setupWorkflow = new AsyncFunction("agent", "parallel", "pipeline", "phase", "log", "args", setupBody);

function setupInput(overrides = {}) {
  return {
    mode: "full",
    workspace: "/run/workspace",
    conflicts: ["package.json"],
    preimageDir: "/run/reconciliation/preimages",
    buildCommand: "npm run build",
    testCommand: "npm test",
    guidance: {
      grounding: "annexe",
      path: "/run/project-Annexe/README.md",
      content: "GUIDANCE_SENTINEL_VIOLET_719",
    },
    setupBrief: {
      path: "workflows/setup-briefs/setup-agent.md",
      readPath: "/opt/minos/workflows/setup-briefs/setup-agent.md",
      content: "SETUP_BRIEF_SENTINEL_OCHRE_719",
    },
    objections: null,
    ...overrides,
  };
}

test("setup workflow binds inputs and dispatches one non-isolated Opus leg", async () => {
  const calls = [];
  const result = await setupWorkflow(
    async (prompt, options) => {
      calls.push({ prompt, options });
      return {
        reconciliation: {
          attempted: true,
          resolutions: [{ path: "package.json", note: "preserved both dependency updates" }],
        },
        environment: { ready: true, actions: ["npm ci"], cause: null },
      };
    },
    async () => [],
    async () => [],
    () => {},
    () => {},
    setupInput(),
  );
  assert.equal(result.status, "complete");
  assert.equal(calls.length, 1);
  assert.equal(calls[0].options.engine, "claude");
  assert.equal(calls[0].options.model, "claude-opus-5");
  assert.equal(calls[0].options.effort, "high");
  assert.equal(calls[0].options.isolation, undefined);
  assert.equal(calls[0].options.label, "setup");
  assert.match(calls[0].prompt, /GUIDANCE_SENTINEL_VIOLET_719/);
  assert.match(calls[0].prompt, /SETUP_BRIEF_SENTINEL_OCHRE_719/);
  assert.match(calls[0].prompt, /package\.json/);
  assert.match(calls[0].prompt, /npm run build/);
  assert.match(calls[0].prompt, /npm test/);
  assert.match(calls[0].prompt, /Do not commit, push/);
});

test("reconciliation-only retry carries objections and excludes provisioning work", async () => {
  let captured = "";
  const result = await setupWorkflow(
    async (prompt) => {
      captured = prompt;
      return {
        reconciliation: {
          attempted: true,
          resolutions: [{ path: "package.json", note: "retained the target engine floor" }],
        },
        environment: { ready: true, actions: [], cause: null },
      };
    },
    async () => [],
    async () => [],
    () => {},
    () => {},
    setupInput({
      mode: "reconcile-only",
      objections: [{ path: "package.json", objection: "the target-side engine floor was dropped" }],
    }),
  );
  assert.equal(result.status, "complete");
  assert.match(captured, /target-side engine floor was dropped/);
  assert.match(captured, /Do not inspect, install, upgrade/);
  assert.doesNotMatch(captured, /Install missing global toolchains/);
});

test("setup workflow fails closed when its input or leg result is absent", async () => {
  let calls = 0;
  const invalid = await setupWorkflow(
    async () => { calls += 1; },
    async () => [],
    async () => [],
    () => {},
    () => {},
    { mode: "full" },
  );
  assert.equal(invalid.status, "incomplete");
  assert.equal(calls, 0);

  const missing = await setupWorkflow(
    async () => null,
    async () => [],
    async () => [],
    () => {},
    () => {},
    setupInput(),
  );
  assert.deepEqual(missing, { status: "incomplete", reason: "setup agent returned no result" });
});

test("setup input builder emits deterministic repository content and objections", () => {
  const runDir = mkdtempSync(join(tmpdir(), "minos-setup-inputs-"));
  const workspace = join(runDir, "workspace");
  mkdirSync(workspace);
  const guidance = join(runDir, "README.md");
  writeFileSync(guidance, "INPUT_GUIDANCE_SENTINEL_SIENNA_719\n");
  const orientation = join(runDir, "orientation.json");
  writeFileSync(orientation, JSON.stringify({
    repository: workspace,
    grounding: "repository",
    guidance,
  }));
  writeFileSync(join(runDir, "reconciliation.json"), JSON.stringify({
    outcome: "conflict",
    conflicts: ["package.json"],
    preimageDir: join(runDir, "reconciliation", "preimages"),
  }));
  const objections = join(runDir, "objections.json");
  writeFileSync(objections, JSON.stringify([{
    path: "package.json",
    objection: "OBJECTION_SENTINEL_AUBURN_719",
  }]));

  const stdout = execFileSync(
    process.execPath,
    [join(workflowsDir, "setup-inputs.mjs"), "--reconcile-only", "--objections", objections],
    {
      encoding: "utf8",
      env: {
        ...process.env,
        MINOS_RUN_DIR: runDir,
        MINOS_ORIENTATION: orientation,
        MINOS_WORKSPACE: workspace,
        MINOS_BUILD_CMD: "npm run build",
        MINOS_TEST_CMD: "npm test",
      },
    },
  );
  const result = JSON.parse(stdout);
  assert.equal(result.mode, "reconcile-only");
  assert.equal(result.workspace, workspace);
  assert.deepEqual(result.conflicts, ["package.json"]);
  assert.equal(result.guidance.content, "INPUT_GUIDANCE_SENTINEL_SIENNA_719\n");
  assert.match(result.setupBrief.content, /Prepare the reviewed repository/);
  assert.equal(result.objections[0].objection, "OBJECTION_SENTINEL_AUBURN_719");
  assert.equal(result.buildCommand, "npm run build");
  assert.equal(result.testCommand, "npm test");
});

test("setup input builder loudly rejects a missing reconciliation record", () => {
  const runDir = mkdtempSync(join(tmpdir(), "minos-setup-inputs-missing-"));
  const guidance = join(runDir, "README.md");
  writeFileSync(guidance, "# Guidance\n");
  const orientation = join(runDir, "orientation.json");
  writeFileSync(orientation, JSON.stringify({ repository: runDir, guidance }));
  const result = spawnSync(process.execPath, [join(workflowsDir, "setup-inputs.mjs")], {
    encoding: "utf8",
    env: { ...process.env, MINOS_RUN_DIR: runDir, MINOS_ORIENTATION: orientation },
  });
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /reconciliation\.json/);
});

test("lifecycle runs setup every time and keeps resolution checking with the lead", () => {
  const stageOne = lifecycle.slice(lifecycle.indexOf("1. The setup script"), lifecycle.indexOf("2. Publish"));
  assert.match(stageOne, /Run the setup workflow on every run/);
  assert.match(stageOne, /setup-inputs\.mjs[\s\S]*setup\.js/);
  assert.match(stageOne, /environment\.ready: false[\s\S]*incomplete terminal/);
  assert.match(stageOne, /show-resolutions[\s\S]*preserves\s+both parents' intent/);
  assert.match(stageOne, /reopen-conflict[\s\S]*redispatch once/);
  assert.match(stageOne, /complete-reconciliation/);
  assert.match(stageOne, /merge-base-to-head comment geometry/);
  assert.match(stageOne, /pinned target-to-current-pull-request-head range/);
  assert.match(stageOne, /Never edit\s+a conflicted file yourself/);
});
