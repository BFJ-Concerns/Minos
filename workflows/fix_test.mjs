import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import { isFixWavePlan, prepareFixWave } from "./fix-wave-plan.mjs";

const scriptPath = fileURLToPath(new URL("./fix.js", import.meta.url));
const inputScriptPath = fileURLToPath(new URL("./fix-inputs.mjs", import.meta.url));
const source = await readFile(scriptPath, "utf8");
const body = source.replace(/^export const meta =/m, "const meta =");
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
const script = new AsyncFunction("agent", "parallel", "pipeline", "phase", "log", "args", body);

function finding(title, severity, path, line) {
  return { title, severity, confidence: 90, path, line, explanation: `${title} breaks the contract` };
}

function args(findings, overrides = {}) {
  return {
    review: {
      status: "complete",
      reviewed: { target: "target111", head: "head222" },
      confirmedFindings: findings,
    },
    threshold: "High",
    clusterCap: 5,
    maximumRounds: null,
    runRecord: { round: 0, confirmedUnfixed: [] },
    workspace: "/run/workspace",
    verification: { build: "make build", tests: "make test" },
    guidance: { grounding: "annexe", path: "/run/subject-Annexe/README.md", content: "COMMISSION_VIOLET_719" },
    fixerBrief: { path: "workflows/review-briefs/fixer.md", readPath: "/minos/workflows/review-briefs/fixer.md", content: "MINOS_FIX_EVIDENCE_V1" },
    verification: { build: "make build", tests: "make test" },
    ...overrides,
  };
}

function assignedFindings(prompt) {
  const match = prompt.match(/Confirmed findings: (\[[^\n]+\])/);
  assert.ok(match, "fix prompt carries finding data");
  return JSON.parse(match[1]);
}

async function run(input, respond = (label, prompt) => ({
  commit: `${label}-commit`,
  fixes: assignedFindings(prompt).map((item) => ({ findingKey: item.key, status: "fixed", writeUp: `Repaired ${item.title}.` })),
})) {
  const calls = [];
  const agent = async (prompt, options) => {
    calls.push({ prompt, options });
    return respond(options.label, prompt, options);
  };
  const parallel = async (thunks) => Promise.all(thunks.map((thunk) => thunk().catch(() => null)));
  const plan = isFixWavePlan(input) ? input : prepareFixWave(input);
  if (plan.status !== "complete" || plan.classification === "terminal")
    return { result: plan, calls };
  const result = await script(agent, parallel, async () => [], () => {}, () => {}, plan);
  return { result, calls };
}

function verificationActionsUnderstoodByWorker(prompt) {
  const actions = new Set();
  for (const sentence of prompt.split(/(?<=[.!?])\s+|\n+/)) {
    const instruction = sentence.trim();
    const isWorkerDirective = /^(?:before [^,]+,\s*)?(?:you (?:must|need to|are required to)\s+)?(?:run|execute)\b/i.test(instruction);
    if (!isWorkerDirective || !/\bconfigured\b/i.test(instruction)) continue;
    const encodedCommand = instruction.match(/("(?:\\.|[^"])*")\s*[.!?]?$/);
    if (!encodedCommand || JSON.parse(encodedCommand[1]).trim() === "") continue;
    if (/\bbuild\b/i.test(instruction)) actions.add("build");
    if (/\btests?\b/i.test(instruction)) actions.add("tests");
  }
  return [...actions];
}

test("the recording worker reads obligations from directive meaning and command presence", () => {
  assert.deepEqual(verificationActionsUnderstoodByWorker(
    `Run this configured build command before returning: "make build"\n` +
    `Execute the configured test command prior to completion: "make test"\n`,
  ), ["build", "tests"]);
  assert.deepEqual(verificationActionsUnderstoodByWorker(
    `Execute the configured build check before returning: "make build"\n`,
  ), ["build"]);
  assert.deepEqual(verificationActionsUnderstoodByWorker(
    `Run the configured tests prior to completion: "make test"\n`,
  ), ["tests"]);
  assert.deepEqual(verificationActionsUnderstoodByWorker(
    "Return one result for every findingKey.\n",
  ), []);
  assert.deepEqual(verificationActionsUnderstoodByWorker(
    `Run the configured build command before returning: "   "\n`,
  ), []);
});

test("a High plus two Lows is working, posts all findings, and dispatches all fixes", async () => {
  const findings = [
    finding("unsafe transition", "High", "a.go", 4),
    finding("weak wording", "Low", "b.go", 7),
    finding("missing edge", "Low", "c.go", 9),
  ];
  const { result, calls } = await run(args(findings));
  assert.equal(result.classification, "working");
  assert.equal(result.sweepReview.comments.length, 3);
  assert.equal(calls.length, 1);
  assert.equal(result.dispatches.length, 1);
  assert.deepEqual(
    { engine: calls[0].options.engine, model: calls[0].options.model, isolation: calls[0].options.isolation },
    { engine: "codex", model: "gpt-5.6-sol", isolation: "worktree" },
  );
  assert.equal("fallbackModel" in calls[0].options, false);
});

test("a sweep containing only Lows is terminal and dispatches or posts nothing", async () => {
  const findings = [finding("wording", "Low", "a.go", 4), finding("small edge", "Low", "b.go", 7)];
  const { result, calls } = await run(args(findings));
  assert.equal(result.classification, "terminal");
  assert.equal(result.terminalReason, "below-threshold");
  assert.equal(result.sweepReview, null);
  assert.equal(result.dispatches.length, 0);
  assert.equal(result.overflow.length, 2);
  assert.equal(calls.length, 0);
});

test("the effectful dispatcher refuses a terminal preparation", async () => {
  const plan = prepareFixWave(args([finding("wording", "Low", "a.go", 4)]));
  const calls = [];
  const result = await script(
    async () => { calls.push("called"); },
    async (thunks) => Promise.all(thunks.map((thunk) => thunk())),
    async () => [],
    () => {},
    () => {},
    plan,
  );
  assert.equal(result.status, "incomplete");
  assert.match(result.reason, /working or single-wave preparation/);
  assert.deepEqual(calls, []);
});

test("same-file findings stay together while small disjoint clusters pack only to the cap", async () => {
  const findings = [
    finding("a1", "High", "a.go", 1),
    finding("a2", "Low", "a.go", 2),
    finding("b1", "Low", "b.go", 1),
    finding("c1", "Low", "c.go", 1),
    finding("d1", "Low", "d.go", 1),
  ];
  const { result } = await run(args(findings, { clusterCap: 2 }));
  assert.deepEqual(result.dispatches.map((entry) => entry.files), [["a.go"], ["b.go", "c.go"], ["d.go"]]);
  assert.deepEqual(result.dispatches.map((entry) => entry.findingKeys.length), [2, 2, 1]);
});

test("a wave records one lead push for all distinct agent commits", async () => {
  const findings = [finding("first", "High", "a.go", 1), finding("second", "Low", "b.go", 2)];
  const { result } = await run(args(findings, { clusterCap: 1 }));
  assert.deepEqual(result.integration.commits, ["fix-cluster-1-commit", "fix-cluster-2-commit"]);
  assert.equal(result.integration.pushCount, 1);
  assert.deepEqual(result.integration.author, { name: "Minos", email: "minos@example.invalid" });
});

test("fix assignments carry only the configured build and test obligations", async () => {
  const original = finding("transition", "High", "internal/state.go", 41);
  const cases = [
    {
      verification: { build: "cargo build --workspace --locked", tests: "cargo test --workspace --locked" },
      build: true,
      tests: true,
    },
    {
      verification: { build: "", tests: "cargo test --workspace --locked" },
      build: false,
      tests: true,
    },
    { verification: { build: "", tests: "" }, build: false, tests: false },
    { verification: { build: "   ", tests: "\t " }, build: false, tests: false, whitespaceOnly: true },
    { verification: undefined, build: false, tests: false },
  ];
  for (const expected of cases) {
    const { calls } = await run(args([original], { verification: expected.verification }));
    const prompt = calls[0].prompt;
    assert.equal(
      prompt.includes('Run this configured build command before returning: "cargo build --workspace --locked"'),
      expected.build,
    );
    assert.equal(
      prompt.includes('Run this configured test command before returning: "cargo test --workspace --locked"'),
      expected.tests,
    );
    if (!expected.build) assert.doesNotMatch(prompt, /configured build/i);
    if (!expected.tests) assert.doesNotMatch(prompt, /configured test/i);
    if (expected.whitespaceOnly) assert.doesNotMatch(prompt, /\b(?:build|test|configured)\b/i);
  }
});

test("fix write-ups keep the finding path and line", async () => {
  const original = finding("transition", "High", "internal/state.go", 41);
  const { result } = await run(args([original]));
  assert.deepEqual(result.fixReview.comments, [{
    path: "internal/state.go",
    body: "Repaired transition.",
    line: 41,
  }]);
});

test("a twice-failed finding becomes confirmed-unfixed and is never redispatched", async () => {
  const original = finding("transition", "High", "internal/state.go", 41);
  const first = await run(args([original]), (label, prompt) => ({
    commit: "",
    fixes: assignedFindings(prompt).map((item) => ({ findingKey: item.key, status: "failed", writeUp: `${label} could not repair it` })),
  }));
  assert.equal(first.calls.length, 2);
  assert.equal(first.result.confirmedUnfixed.length, 1);
  assert.equal(first.result.confirmedUnfixed[0].attempts, 2);

  const next = await run(args([original], { runRecord: first.result.runRecord }));
  assert.equal(next.result.classification, "terminal");
  assert.equal(next.result.round, 2);
  assert.notEqual(next.result.fingerprint, first.result.fingerprint);
  assert.equal(next.result.dispatches.length, 0);
  assert.equal(next.result.requestChanges.length, 1);
  assert.equal(next.result.runRecord.confirmedUnfixed[0].attempts, 2);
  assert.equal(next.result.requestChangesReview.verdict, "request-changes");
  assert.deepEqual(next.result.requestChangesReview.comments[0], {
    path: "internal/state.go",
    body: "**transition**\n\ntransition breaks the contract\n\nSeverity: High. Confidence: 90.",
    line: 41,
  });
  assert.equal(next.calls.length, 0);
});

test("the configured maximum rounds stops another fix dispatch", async () => {
  const original = finding("transition", "High", "internal/state.go", 41);
  const { result, calls } = await run(args([original], {
    maximumRounds: 1,
    runRecord: { round: 1, confirmedUnfixed: [] },
  }));
  assert.equal(result.classification, "terminal");
  assert.equal(result.terminalReason, "maximum-rounds");
  assert.equal(result.requestChanges.length, 1);
  assert.equal(calls.length, 0);
});

test("single-wave mode dispatches every brief finding once and requests build and tests without re-review", async () => {
  const findings = [
    finding("material", "High", "a.go", 1),
    finding("minor", "Low", "b.go", 2),
  ];
  const { result, calls } = await run(args(findings, { singleWave: true, clusterCap: 1 }));
  assert.equal(result.classification, "single-wave");
  assert.equal(calls.length, 2);
  assert.ok(result.dispatches.every((entry) => entry.attempt === 1));
  assert.equal(result.repairsComplete, true);
  assert.equal(result.buildAndTestsRequired, true);
  assert.equal(result.rerunReview, false);
  assert.equal(result.sweepReview, null);
  assert.deepEqual(result.fixReview.comments, [
    { path: "a.go", body: "Repaired material.", line: 1 },
    { path: "b.go", body: "Repaired minor.", line: 2 },
  ]);
});

test("a fix worker performs exactly the configured verification it is assigned", async (t) => {
  for (const shape of [
    { name: "build and tests", verification: { build: "make build", tests: "make test" }, expected: ["build", "tests"] },
    { name: "build only", verification: { build: "make build", tests: "" }, expected: ["build"] },
    { name: "tests only", verification: { build: "", tests: "make test" }, expected: ["tests"] },
    { name: "nothing configured", verification: { build: "", tests: "" }, expected: [] },
    { name: "whitespace-only build", verification: { build: "   ", tests: "" }, expected: [] },
  ]) {
    await t.test(shape.name, async () => {
      const actions = [];
      const original = finding("transition", "High", "internal/state.go", 41);
      const { result, calls } = await run(
        args([original], { singleWave: true, verification: shape.verification }),
        (label, prompt) => {
          actions.push(...verificationActionsUnderstoodByWorker(prompt));
          const assignmentUnderstood = actions.length === shape.expected.length &&
            actions.every((action, index) => action === shape.expected[index]);
          return {
            commit: assignmentUnderstood ? `${label}-verified-commit` : "",
            fixes: assignedFindings(prompt).map((item) => ({
              findingKey: item.key,
              status: assignmentUnderstood ? "fixed" : "failed",
              writeUp: assignmentUnderstood
                ? "Repair completed every configured verification action."
                : "Configured verification assignment was not understood.",
            })),
          };
        },
      );

      assert.deepEqual(actions, shape.expected);
      assert.equal(calls.length, 1);
      assert.equal(result.repairsComplete, true);
      assert.deepEqual(result.integration.commits, ["brief-fix-cluster-1-verified-commit"]);
    });
  }
});

test("a failed single-wave brief fix is not retried and cannot pass the stage", async () => {
  const original = finding("transition", "High", "internal/state.go", 41);
  const { result, calls } = await run(args([original], { singleWave: true }), (_label, prompt) => ({
    commit: "",
    fixes: assignedFindings(prompt).map((item) => ({ findingKey: item.key, status: "failed", writeUp: "could not repair" })),
  }));
  assert.equal(calls.length, 1);
  assert.equal(result.dispatches.length, 1);
  assert.equal(result.confirmedUnfixed.length, 1);
  assert.equal(result.confirmedUnfixed[0].attempts, 1);
  assert.equal(result.repairsComplete, false);
  assert.equal(result.requestChangesReview, null);
  assert.equal(result.rerunReview, false);
});

test("published review prose discusses code without process or round labels", async () => {
  const { result } = await run(args([finding("transition", "High", "state.go", 4)]));
  const prose = JSON.stringify([result.sweepReview, result.fixReview]);
  assert.doesNotMatch(prose, /Minos|workflow|process|round/i);
});

test("incomplete review input dispatches no fix agents", async () => {
  const { result, calls } = await run(args([], {
    review: { status: "incomplete", reason: "verifier result is missing", confirmedFindings: [] },
  }));
  assert.equal(result.status, "incomplete");
  assert.equal(result.reason, "fix preparation needs a complete review: verifier result is missing");
  assert.equal(calls.length, 0);
});

test("a malformed brief review keeps its diagnostic through effectful dispatch", async () => {
  const root = mkdtempSync(join(tmpdir(), "minos-fix-inputs-malformed-"));
  const guidancePath = join(root, "README.md");
  const orientationPath = join(root, "orientation.json");
  const reviewPath = join(root, "review.json");
  writeFileSync(guidancePath, "COMMISSION_VIOLET_719");
  writeFileSync(orientationPath, JSON.stringify({ repository: root, grounding: "annexe", guidance: guidancePath }));
  writeFileSync(reviewPath, JSON.stringify({
    status: "complete",
    reviewed: { target: "target111" },
    confirmedFindings: [],
  }));
  const plan = JSON.parse(execFileSync(process.execPath, [inputScriptPath, reviewPath, "--single-wave"], {
    encoding: "utf8",
    env: { ...process.env, MINOS_ORIENTATION: orientationPath, MINOS_WORKSPACE: root },
  }));
  const calls = [];
  const result = await script(
    async () => { calls.push("called"); },
    async (thunks) => Promise.all(thunks.map((thunk) => thunk())),
    async () => [],
    () => {},
    () => {},
    plan,
  );
  assert.equal(result.status, "incomplete");
  assert.equal(result.reason, "fix preparation review needs reviewed.head as a non-empty string");
  assert.deepEqual(calls, []);
});

test("direct fix input with empty guidance dispatches no fix agents", async () => {
  const { result, calls } = await run(args([], {
    guidance: { grounding: "annexe", path: "/run/subject-Annexe/README.md", content: " \n\t" },
  }));
  assert.equal(result.status, "incomplete");
  assert.equal(calls.length, 0);
});

test("the deterministic fix input rejects empty and whitespace-only guidance", () => {
  for (const guidanceContent of ["", " \n\t"]) {
    const root = mkdtempSync(join(tmpdir(), "minos-fix-inputs-empty-"));
    const guidancePath = join(root, "README.md");
    const orientationPath = join(root, "orientation.json");
    const reviewPath = join(root, "review.json");
    writeFileSync(guidancePath, guidanceContent);
    writeFileSync(orientationPath, JSON.stringify({ repository: root, grounding: "annexe", guidance: guidancePath }));
    writeFileSync(reviewPath, JSON.stringify({ status: "complete", confirmedFindings: [] }));
    assert.throws(
      () => execFileSync(process.execPath, [inputScriptPath, reviewPath], {
        encoding: "utf8",
        env: { ...process.env, MINOS_ORIENTATION: orientationPath, MINOS_WORKSPACE: root },
        stdio: ["ignore", "pipe", "pipe"],
      }),
      /guidance document is empty/,
    );
  }
});

test("the deterministic fix input carries configured loop knobs and the run record", () => {
  const root = mkdtempSync(join(tmpdir(), "minos-fix-inputs-"));
  const guidancePath = join(root, "README.md");
  const orientationPath = join(root, "orientation.json");
  const reviewPath = join(root, "review.json");
  const recordPath = join(root, "record.json");
  writeFileSync(guidancePath, "COMMISSION_VIOLET_719");
  writeFileSync(orientationPath, JSON.stringify({ repository: root, grounding: "annexe", guidance: guidancePath }));
  writeFileSync(reviewPath, JSON.stringify({
    status: "complete",
    reviewed: { target: "target111", head: "head222" },
    confirmedFindings: [],
  }));
  writeFileSync(recordPath, JSON.stringify({ round: 3, confirmedUnfixed: [] }));
  const input = JSON.parse(execFileSync(process.execPath, [inputScriptPath, reviewPath, recordPath], {
    encoding: "utf8",
    env: {
      ...process.env,
      MINOS_ORIENTATION: orientationPath,
      MINOS_WORKSPACE: root,
      MINOS_REVIEW_THRESHOLD: "Medium",
      MINOS_FIX_CLUSTER_CAP: "4",
      MINOS_MAX_ROUNDS: "8",
      MINOS_BUILD_CMD: "make build",
      MINOS_TEST_CMD: "make test",
    },
  }));
  assert.equal(input.threshold, "Medium");
  assert.equal(input.clusterCap, 4);
  assert.equal(input.maximumRounds, 8);
  assert.equal(input.runRecord.round, 3);
  assert.deepEqual(input.verification, { build: "make build", tests: "make test" });
  assert.equal(input.guidance.content, "COMMISSION_VIOLET_719");
});

test("the deterministic fix input selects single-wave mode explicitly", () => {
  const root = mkdtempSync(join(tmpdir(), "minos-fix-inputs-single-wave-"));
  const guidancePath = join(root, "README.md");
  const orientationPath = join(root, "orientation.json");
  const reviewPath = join(root, "review.json");
  writeFileSync(guidancePath, "COMMISSION_VIOLET_719");
  writeFileSync(orientationPath, JSON.stringify({ repository: root, grounding: "annexe", guidance: guidancePath }));
  writeFileSync(reviewPath, JSON.stringify({
    status: "complete",
    reviewed: { target: "target111", head: "head222" },
    confirmedFindings: [],
  }));
  const input = JSON.parse(execFileSync(process.execPath, [inputScriptPath, reviewPath, "--single-wave"], {
    encoding: "utf8",
    env: { ...process.env, MINOS_ORIENTATION: orientationPath, MINOS_WORKSPACE: root },
  }));
  assert.equal(input.kind, "minos-fix-wave-plan-v1");
  assert.equal(input.classification, "single-wave");
  assert.equal(input.input.workspace, root);
});
