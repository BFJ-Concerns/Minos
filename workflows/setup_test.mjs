import "./isolate-from-live-run.mjs";
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
    head: "head-setup-719",
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
      content: `SETUP_BRIEF_SENTINEL_OCHRE_719

In full mode, use the repository's configured commands and manifests to choose
which caches to warm, then run each configured build and test command exactly
once to completion. Passwordless sudo is available when an evidenced system
package is the appropriate installation.`,
    },
    objections: null,
    ...overrides,
  };
}

test("setup workflow sends recorded conflicts to Opus before provisioning", async () => {
  const calls = [];
  const result = await setupWorkflow(
    async (prompt, options) => {
      calls.push({ prompt, options });
      if (options.model === "claude-opus-5") {
        return {
          reconciliation: {
            attempted: true,
            resolutions: [{ path: "package.json", note: "preserved both dependency updates" }],
          },
        };
      }
      return {
        environment: { ready: true, actions: ["npm ci"], cause: null },
        commandExecutions: {
          head: "head-setup-719",
          build: { command: "npm run build", exitStatus: 0 },
          test: { command: "npm test", exitStatus: 0 },
        },
      };
    },
    async () => [],
    async () => [],
    () => {},
    () => {},
    setupInput(),
  );
  assert.equal(result.status, "complete");
  assert.equal(calls.length, 2);
  assert.equal(calls[0].options.engine, "claude");
  assert.equal(calls[0].options.model, "claude-opus-5");
  assert.equal(calls[0].options.effort, "high");
  assert.equal(calls[0].options.isolation, undefined);
  assert.equal(calls[0].options.label, "setup-reconciliation");
  assert.match(calls[0].prompt, /GUIDANCE_SENTINEL_VIOLET_719/);
  assert.match(calls[0].prompt, /SETUP_BRIEF_SENTINEL_OCHRE_719/);
  assert.match(calls[0].prompt, /package\.json/);
  assert.match(calls[0].prompt, /which caches to warm/);
  assert.match(calls[0].prompt, /Passwordless sudo is available/);
  assert.match(calls[0].prompt, /do not inspect, install, upgrade, warm, or otherwise alter the environment/i);
  assert.match(calls[0].prompt, /do not run configured build or test commands/i);
  assert.match(calls[0].prompt, /takes precedence over any environment instructions in the shipped setup brief/i);

  assert.equal(calls[1].options.engine, "codex");
  assert.equal(calls[1].options.model, "gpt-5.6-terra");
  assert.equal(calls[1].options.effort, "low");
  assert.equal(calls[1].options.label, "setup-provision");
  assert.match(calls[1].prompt, /GUIDANCE_SENTINEL_VIOLET_719/);
  assert.match(calls[1].prompt, /SETUP_BRIEF_SENTINEL_OCHRE_719/);
  assert.doesNotMatch(calls[1].prompt, /Resolve these conflicted paths/);
  assert.match(calls[1].prompt, /Install missing global toolchains/);
  assert.match(calls[1].prompt, /npm run build/);
  assert.match(calls[1].prompt, /npm test/);
  assert.match(calls[1].prompt, /run each non-empty configured command exactly once to completion/);
  assert.match(calls[1].prompt, /re-run that command after the evidenced repair/);
  assert.match(calls[1].prompt, /never re-run without a proven, repaired environment fault/);
  assert.match(calls[1].prompt, /raw outcomes are evidence only; do not classify/);
  assert.match(calls[1].prompt, /commit, or push/);
});

test("clean setup provisions without dispatching an Opus reconciliation leg", async () => {
  const calls = [];
  const result = await setupWorkflow(
    async (prompt, options) => {
      calls.push({ prompt, options });
      return {
        environment: { ready: true, actions: ["npm ci"], cause: null },
        commandExecutions: {
          head: "head-setup-719",
          build: { command: "npm run build", exitStatus: 0 },
          test: { command: "npm test", exitStatus: 0 },
        },
      };
    },
    async () => [],
    async () => [],
    () => {},
    () => {},
    setupInput({ conflicts: [], preimageDir: null }),
  );
  assert.equal(result.status, "complete");
  assert.equal(calls.length, 1);
  assert.equal(calls[0].options.model, "gpt-5.6-terra");
  assert.equal(calls[0].options.effort, "low");
  assert.ok(calls.every(({ options }) => options.model !== "claude-opus-5"));
  assert.match(calls[0].prompt, /Reconciliation is already clean/);
  assert.match(calls[0].prompt, /Install missing global toolchains/);
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
        commandExecutions: {
          head: "head-setup-719",
          build: { command: "npm run build", exitStatus: null },
          test: { command: "npm test", exitStatus: null },
        },
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
  assert.match(captured, /do not inspect, install, upgrade/i);
  assert.match(captured, /takes precedence over any environment instructions in the shipped setup brief/);
  assert.doesNotMatch(captured, /Install missing global toolchains/);
});

test("setup workflow fails closed when its input, reconciliation result, or provisioning result is absent", async () => {
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
  assert.deepEqual(missing, { status: "incomplete", reason: "setup reconciliation agent returned no result" });

  const provisioningMissing = await setupWorkflow(
    async () => null,
    async () => [],
    async () => [],
    () => {},
    () => {},
    setupInput({ conflicts: [], preimageDir: null }),
  );
  assert.deepEqual(provisioningMissing, {
    status: "incomplete",
    reason: "setup provisioning agent returned no result",
  });
});

test("setup workflow refuses command evidence outside the requested envelope", async () => {
  const result = await setupWorkflow(
    async () => ({
      reconciliation: { attempted: false, resolutions: [] },
      environment: { ready: true, actions: [], cause: null },
      commandExecutions: {
        head: "different-head",
        build: { command: "npm run build", exitStatus: 0 },
        test: { command: "npm test", exitStatus: 0 },
      },
    }),
    async () => [],
    async () => [],
    () => {},
    () => {},
    setupInput(),
  );
  assert.deepEqual(result, {
    status: "incomplete",
    reason: "setup agent returned command outcomes outside the requested head and commands",
  });
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
    [join(workflowsDir, "setup-inputs.mjs"), "head-builder-719", "--reconcile-only", "--objections", objections],
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
  assert.equal(result.head, "head-builder-719");
  assert.equal(result.workspace, workspace);
  assert.deepEqual(result.conflicts, ["package.json"]);
  assert.equal(result.guidance.content, "INPUT_GUIDANCE_SENTINEL_SIENNA_719\n");
  assert.match(result.setupBrief.content, /Prepare the reviewed repository/);
  for (const required of [
    "$MINOS_SHARED_CACHE_DIR",
    "$SCCACHE_DIR",
    "$GOCACHE",
    "$GOMODCACHE",
    "$npm_config_cache",
    "$XDG_STATE_HOME",
    "$HOME/.cargo/bin",
    "$MINOS_RUN_DIR/rust-target-fallback",
    "$HOME/.local/bin",
  ]) {
    assert.ok(result.setupBrief.content.includes(required), `setup brief must name ${required}`);
  }
  assert.match(result.setupBrief.content, /untracked\s+artefacts instead/);
  assert.match(
    result.setupBrief.content,
    /run each configured build and test command exactly\s+once to completion/i,
  );
  assert.match(
    result.setupBrief.content,
    /In reconciliation-only mode[\s\S]*without running either configured command/,
  );
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
  const result = spawnSync(process.execPath, [join(workflowsDir, "setup-inputs.mjs"), "head-missing-719"], {
    encoding: "utf8",
    env: { ...process.env, MINOS_RUN_DIR: runDir, MINOS_ORIENTATION: orientation },
  });
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /reconciliation\.json/);
});

test("setup input builder rejects an option in place of the head", () => {
  const result = spawnSync(
    process.execPath,
    [join(workflowsDir, "setup-inputs.mjs"), "--reconcile-only"],
    { encoding: "utf8" },
  );
  assert.equal(result.status, 2);
  assert.match(result.stderr, /^usage:/);
  assert.equal(result.stdout, "");
});

test("lifecycle runs setup every time and keeps resolution checking with the lead", () => {
  const stageOne = lifecycle.slice(lifecycle.indexOf("1. The setup script"), lifecycle.indexOf("2. Publish"));
  assert.match(stageOne, /Run the setup workflow on every run/);
  assert.match(stageOne, /setup-inputs\.mjs[\s\S]*setup\.js/);
  assert.match(stageOne, /setup-inputs\.mjs" "\$MINOS_HEAD_SHA"/);
  assert.match(stageOne, /environment\.ready: false[\s\S]*incomplete terminal/);
  assert.match(stageOne, /show-resolutions[\s\S]*preserves\s+both parents' intent/);
  assert.match(stageOne, /reopen-conflict[\s\S]*redispatch once/);
  assert.match(stageOne, /complete-reconciliation[\s\S]*supersedes[\s\S]*non-reusable/);
  assert.match(stageOne, /merge-base-to-head comment geometry/);
  assert.match(stageOne, /pinned target-to-current-pull-request-head range/);
  assert.match(stageOne, /Never edit\s+a conflicted file yourself/);
});

test("lifecycle reuses only current-head genuine setup passes", () => {
  const stageThree = lifecycle.slice(lifecycle.indexOf("3. Read the repository guidance"), lifecycle.indexOf("4. Run every Ensemble"));
  assert.match(stageThree, /--setup-result[\s\S]*setup-result\.json[\s\S]*\$MINOS_HEAD_SHA/);
  assert.match(stageThree, /Only then consume its build and test results and do\s+not execute either command again/);
  assert.match(stageThree, /absent, malformed, partial,[\s\S]*non-passing,[\s\S]*stale-head[\s\S]*execute both configured commands normally/);
  assert.match(stageThree, /Never reuse a\s+`skipped` outcome for a configured command/);
});
