import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

const scriptPath = fileURLToPath(new URL("./review.js", import.meta.url));
const inputScriptPath = fileURLToPath(new URL("./review-inputs.mjs", import.meta.url));
const source = await readFile(scriptPath, "utf8");
const lifecycle = await readFile(fileURLToPath(new URL("../lifecycle/lifecycle.md", import.meta.url)), "utf8");
const body = source.replace(/^export const meta =/m, "const meta =");
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
const script = new AsyncFunction("agent", "parallel", "pipeline", "phase", "log", "args", body);

async function runScript(args, respond) {
  const calls = [];
  const agent = async (prompt, opts = {}) => {
    calls.push({ prompt, opts });
    return respond(opts.label || "", prompt, opts);
  };
  const parallel = async (thunks) => Promise.all(thunks.map((thunk) => thunk().catch(() => null)));
  const pipeline = async (items, ...stages) => Promise.all(items.map(async (item, index) => {
    let value = item;
    for (const stage of stages) value = await stage(value, item, index);
    return value;
  }));
  const result = await script(agent, parallel, pipeline, () => {}, () => {}, args);
  return { result, calls };
}

function enumeratedArgs(
  target = "aaa111",
  head = "bbb222",
  { grounding = "annexe", guidanceName = "README.md", guidanceContent = "MINOS_TEST_COMMISSION_INDIGO" } = {},
) {
  const root = mkdtempSync(join(tmpdir(), "minos-review-inputs-"));
  const workspace = join(root, "workspace");
  mkdirSync(workspace);
  const guidancePath = join(workspace, guidanceName);
  writeFileSync(guidancePath, guidanceContent);
  const orientationPath = join(root, "orientation.json");
  writeFileSync(orientationPath, JSON.stringify({ repository: workspace, grounding, guidance: guidancePath }));
  return JSON.parse(execFileSync(process.execPath, [inputScriptPath, target, head], {
    encoding: "utf8",
    env: { ...process.env, MINOS_ORIENTATION: orientationPath },
    stdio: ["ignore", "pipe", "pipe"],
  }));
}

const ARGS = enumeratedArgs();

function unit(id = "logic", specialistType = "correctness", scope = ["internal/x.go"]) {
  return { id, concern: `${specialistType} concern ${id}`, scope, specialistType };
}

function explorationFixture({
  files = [{ path: "internal/x.go", added: 30, deleted: 30 }],
  plan = [unit()],
} = {}) {
  return { files, plan };
}

function finding(overrides = {}) {
  return {
    title: "incorrect transition",
    severity: "High",
    confidence: 88,
    path: "internal/x.go",
    line: 3,
    explanation: "the new branch admits an invalid state",
    ...overrides,
  };
}

function specialistResult(findings = [finding()], applicability = { status: "applicable", reason: "the assigned change exercises the concern" }) {
  return { applicability, findings };
}

function responder({ exploration = explorationFixture(), specialist, verify } = {}) {
  return (label, prompt, opts) => {
    if (label === "exploration") return typeof exploration === "function" ? exploration(label, prompt, opts) : exploration;
    if (label.startsWith("verify-"))
      return verify ? verify(label, prompt, opts) : { verdict: "upheld", confidence: 91, reason: "reproduced" };
    if (specialist) return specialist(label, prompt, opts);
    return specialistResult();
  };
}

function specialistCalls(calls) {
  return calls.filter((call) => call.opts.label?.startsWith("specialist-"));
}

test("the script emits an envelope with every routed leg and raw verifier output", async () => {
  const plan = [unit("correct", "correctness"), unit("secure", "security")];
  const { result, calls } = await runScript(ARGS, responder({ exploration: explorationFixture({ plan }) }));

  assert.deepEqual(Object.keys(result).sort(), [
    "briefs", "dispatches", "proposedFindings", "requiredModelEvidence", "reviewed", "reviewers", "stage",
  ]);
  assert.equal(result.stage, "present");
  assert.equal(result.proposedFindings.length, 2);
  assert.deepEqual(result.proposedFindings[0].rawVerifier, { verdict: "upheld", confidence: 91, reason: "reproduced" });
  assert.ok(result.proposedFindings.every((entry) => entry.proposingLabel && entry.verifyLabel));
  assert.deepEqual(
    result.requiredModelEvidence.map(({ role, expectedFamily, pinnedModel }) => ({ role, expectedFamily, pinnedModel })),
    [
      { role: "exploration", expectedFamily: "gpt", pinnedModel: "gpt-5.6-terra" },
      { role: "specialist", expectedFamily: "gpt", pinnedModel: "gpt-5.6-sol" },
      { role: "specialist", expectedFamily: "claude", pinnedModel: "claude-opus-5" },
      { role: "verifier", expectedFamily: "claude", pinnedModel: "claude-opus-5" },
      { role: "verifier", expectedFamily: "gpt", pinnedModel: "gpt-5.6-sol" },
    ],
  );
  assert.ok(calls.every((call) => call.opts.engine === "codex" || call.opts.engine === "claude"));
  assert.ok(calls.every((call) => !("fallbackModel" in call.opts)));
});

test("each finding is verified by the family opposite its specialist", async () => {
  const plan = [unit("correct", "correctness"), unit("secure", "security")];
  const { calls } = await runScript(ARGS, responder({ exploration: explorationFixture({ plan }) }));
  const correctness = calls.find((call) => call.opts.label === "specialist-1-correctness-gpt");
  const security = calls.find((call) => call.opts.label === "specialist-2-security-claude");
  const correctnessVerifier = calls.find((call) => call.opts.label === "verify-1-1-claude");
  const securityVerifier = calls.find((call) => call.opts.label === "verify-2-1-gpt");
  assert.deepEqual([correctness.opts.engine, correctness.opts.model], ["codex", "gpt-5.6-sol"]);
  assert.deepEqual([security.opts.engine, security.opts.model], ["claude", "claude-opus-5"]);
  assert.deepEqual([correctnessVerifier.opts.engine, correctnessVerifier.opts.model], ["claude", "claude-opus-5"]);
  assert.deepEqual([securityVerifier.opts.engine, securityVerifier.opts.model], ["codex", "gpt-5.6-sol"]);
});

test("a missing verifier result stays raw null for post-run adjudication", async () => {
  const { result } = await runScript(ARGS, responder({ verify: () => null }));
  assert.equal(result.proposedFindings.length, 1);
  assert.equal(result.proposedFindings[0].rawVerifier, null);
  assert.ok(result.requiredModelEvidence.some((leg) => leg.role === "verifier"));
});

test("a missing specialist result remains visible as a required leg", async () => {
  const { result } = await runScript(ARGS, responder({ specialist: () => null }));
  assert.equal(result.proposedFindings.length, 0);
  assert.equal(result.reviewers[0].status, "no-result");
  assert.ok(result.requiredModelEvidence.some((leg) => leg.role === "specialist"));
});

test("specialist inapplicability is a skip-like disposition and never reaches a verifier", async () => {
  const { result, calls } = await runScript(ARGS, responder({
    specialist: () => specialistResult(
      [finding({ title: "must not escape" })],
      { status: "inapplicable", reason: "the assigned scope contains no relevant behaviour" },
    ),
  }));
  assert.equal(result.proposedFindings.length, 0);
  assert.equal(calls.filter((call) => call.opts.label?.startsWith("verify-")).length, 0);
  assert.deepEqual(result.briefs, [{
    brief: "workflows/review-briefs/correctness.md",
    title: "correctness concern logic",
    status: "skipped",
    skipKind: "inapplicable",
    reason: "the assigned scope contains no relevant behaviour",
  }]);
});

test("the review plan dispatches every requested specialist and retains the correctness floor", async () => {
  const requested = Array.from({ length: 9 }, (_, index) => unit(`security-${index}`, "security", [`pkg/f${index}.go`]));
  const { result, calls } = await runScript(ARGS, responder({
    exploration: explorationFixture({ plan: requested }),
    specialist: () => specialistResult([]),
  }));
  assert.equal(result.dispatches.length, 10);
  assert.equal(result.dispatches[0].specialistType, "correctness");
  assert.equal(specialistCalls(calls).length, 10);
  assert.deepEqual(specialistCalls(calls).map((call) => call.opts.label), result.dispatches.map((entry) => entry.label));
});

test("every specialist finding reaches opposite-family verification beyond the former bound", async () => {
  const { result, calls } = await runScript(ARGS, responder({
    specialist: () => specialistResult([
      finding({ title: "one" }),
      finding({ title: "two", line: 4 }),
      finding({ title: "three survives", severity: "Medium", line: 5 }),
      finding({ title: "four survives", severity: "Low", line: 6 }),
    ]),
  }));
  assert.deepEqual(result.proposedFindings.map((entry) => entry.title), ["one", "two", "three survives", "four survives"]);
  const beyondBoundVerifier = calls.find((call) => call.opts.label === "verify-1-4-claude");
  assert.ok(beyondBoundVerifier);
  assert.match(beyondBoundVerifier.prompt, /"title":"four survives"/);
});

test("the deterministic input binds shipped role prose and project guidance into every judgement leg", async () => {
  const marker = "MINOS_ESTATE_COMMISSION_CINNABAR_719";
  const args = enumeratedArgs("aaa111", "bbb222", { guidanceContent: marker });
  const { calls } = await runScript(args, responder());
  const judgementCalls = calls.filter((call) =>
    call.opts.label === "exploration" || call.opts.label?.startsWith("specialist-") || call.opts.label?.startsWith("verify-"));
  assert.ok(judgementCalls.every((call) => call.prompt.includes(marker)));
  assert.match(judgementCalls.find((call) => call.opts.label?.startsWith("specialist-")).prompt, /MINOS_CORRECTNESS_EVIDENCE_V1/);
});

test("the deterministic review input rejects empty guidance", () => {
  for (const guidanceContent of ["", " \n\t"])
    assert.throws(() => enumeratedArgs("aaa111", "bbb222", { guidanceContent }), /guidance document is empty/);
});

test("invalid direct input throws before any agent dispatch", async () => {
  const calls = [];
  await assert.rejects(
    runScript({}, (label) => { calls.push(label); return null; }),
    /needs args \{target, head/,
  );
  assert.deepEqual(calls, []);
});

test("an exploration null still emits its required leg for archive adjudication", async () => {
  const { result } = await runScript(ARGS, responder({ exploration: null }));
  assert.deepEqual(result.requiredModelEvidence, [{
    label: "exploration",
    role: "exploration",
    expectedFamily: "gpt",
    pinnedModel: "gpt-5.6-terra",
  }]);
  assert.equal(result.reviewers[0].status, "no-result");
});

test("the lifecycle uses one adjudicated review call and bare Ensemble fix calls", () => {
  assert.match(lifecycle, /invoke the adjudication wrapper once[\s\S]*review\.js[\s\S]*--json-args/);
  assert.match(lifecycle, /node \/opt\/minos\/runtime\/ensemble\.mjs[\s\S]*--json-args[\s\S]*fix\.js/);
  assert.doesNotMatch(lifecycle, /workflowProgress|resumeFromRunId|--workflow-script|briefReview/);
  assert.match(
    lifecycle,
    /rootcause\.js[\s\S]*root-cause skill on `codex` \/ `gpt-5\.6-sol`[\s\S]*isolation: "worktree"[\s\S]*commit a repair[\s\S]*never push/,
  );
  assert.match(lifecycle, /helper returns a non-empty commit[\s\S]*integrate-wave/);
});

test("only an unchanged label-only helper failure may reach the merge path", () => {
  const finishing = lifecycle.slice(lifecycle.indexOf("8. Enter finishing"), lifecycle.indexOf("9. If"));
  assert.match(
    finishing,
    /red-check path[\s\S]*label-only path/,
  );
  assert.match(
    finishing,
    /helper-mutated head has not had a fresh whole review[\s\S]*set `incomplete`[\s\S]*Never continue a helper-mutated head to step 9 or merge/,
  );
  assert.match(
    finishing,
    /helper returns no commit and no pushed head appears[\s\S]*made no[\s\S]*mutation[\s\S]*red-check path[\s\S]*set[\s\S]*`incomplete`[\s\S]*Only on the label-only[\s\S]*unchanged verified head and target continue to step 9/,
  );
});
