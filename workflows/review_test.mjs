// Drives workflows/review.js without engine traffic. The script runs in the
// same async-body shape as the Workflow tool, with agent() answered by
// synthetic fixtures and actual-model evidence supplied through the external
// Workflow progress-record seam used on resume.
import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";

const scriptPath = fileURLToPath(new URL("./review.js", import.meta.url));
const inputScriptPath = fileURLToPath(new URL("./review-inputs.mjs", import.meta.url));
const source = await readFile(scriptPath, "utf8");
const lifecycle = await readFile(fileURLToPath(new URL("../lifecycle/lifecycle.md", import.meta.url)), "utf8");
const agentsGuidance = await readFile(fileURLToPath(new URL("../AGENTS.md", import.meta.url)), "utf8");
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
  const pipeline = async (items, ...stages) =>
    Promise.all(items.map(async (item, index) => {
      let value = item;
      try {
        for (const stage of stages) value = await stage(value, item, index);
        return value;
      } catch {
        return null;
      }
    }));
  const result = await script(agent, parallel, pipeline, () => {}, () => {}, args);
  return { result, calls };
}

function enumeratedArgs(target = "aaa111", head = "bbb222") {
  return JSON.parse(execFileSync(process.execPath, [inputScriptPath, target, head], { encoding: "utf8" }));
}

const ARGS = enumeratedArgs();

function unit(id = "logic", specialistType = "correctness", scope = ["internal/x.go"]) {
  return { id, concern: `${specialistType} concern ${id}`, scope, specialistType };
}

function explorationFixture({
  files = [{ path: "internal/x.go", added: 30, deleted: 30 }],
  plan = [unit()],
  briefs = [],
} = {}) {
  return { files, plan, briefs };
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

function responder({ exploration = explorationFixture(), scopes, specialist, verify } = {}) {
  return (label, prompt, opts) => {
    if (label === "exploration") return typeof exploration === "function" ? exploration(label, prompt, opts) : exploration;
    if (label === "scope-files") return typeof scopes === "function" ? scopes(label, prompt, opts) : scopes ?? { scopes: [] };
    if (label.startsWith("verify-"))
      return verify ? verify(label, prompt, opts) : { verdict: "upheld", confidence: 91, reason: "reproduced" };
    if (specialist) return specialist(label, prompt, opts);
    if (label.startsWith("repository-"))
      return { applicable: true, reason: "the concern applies", findings: [finding()] };
    return { findings: [finding()] };
  };
}

function runRecordFor(result, overrides = {}, extra = {}) {
  return {
    workflowProgress: result.requiredModelEvidence.map((leg, index) => ({
      type: "workflow_agent",
      index: index + 1,
      label: leg.label,
      state: "done",
      model: leg.pinnedModel,
      attempt: 1,
      ...(overrides[leg.label] || {}),
    })),
    ...extra,
  };
}

async function runRecorded(args, respond, { overrides = {}, extra = {} } = {}) {
  const first = await runScript(args, respond);
  const runRecord = runRecordFor(first.result, overrides, extra);
  const resumed = await runScript({ ...args, runRecord }, respond);
  return { first, ...resumed, runRecord };
}

function reviewCalls(calls) {
  return calls.filter((call) => call.opts.label?.startsWith("specialist-") || call.opts.label?.startsWith("repository-"));
}

test("right-family run-record evidence completes the workflow", async () => {
  const { result } = await runRecorded(ARGS, responder());
  assert.equal(result.status, "complete");
  assert.equal(result.complete, true);
  assert.equal(result.verdictsComplete, true);
  assert.equal(result.confirmedFindings.length, 1);
  assert.equal(result.confirmedFindings[0].verdict, "confirmed");
});

test("verification always crosses to the opposite configured family", async () => {
  const { result, calls } = await runRecorded(ARGS, responder());
  const specialist = result.reviewers[0];
  const verifyCall = calls.find((call) => call.opts.label?.startsWith("verify-"));
  assert.ok(verifyCall);
  assert.equal(specialist.family, "gpt");
  assert.equal(verifyCall.opts.model, "claude-opus-4-8");
  assert.equal(result.findings[0].verify.expectedFamily, "claude");
});

test("every exploration, specialist, and verifier leg has an explicit commissioned model pin", async () => {
  const plan = [unit("correct", "correctness"), unit("secure", "security")];
  const { result, calls } = await runRecorded(ARGS, responder({ exploration: explorationFixture({ plan }) }));
  const expected = new Set(["anthropic-gpt-5.6-terra", "anthropic-gpt-5.6-sol", "claude-opus-4-8"]);
  for (const call of calls) {
    assert.ok(expected.has(call.opts.model), `${call.opts.label} used ${call.opts.model}`);
    assert.notEqual(call.opts.model, undefined);
  }
  assert.ok(result.requiredModelEvidence.every((leg) => expected.has(leg.pinnedModel)));
});

test("agent self-report is neither requested nor carried as model evidence", async () => {
  const { result, calls } = await runRecorded(ARGS, responder());
  assert.ok(calls.every((call) => !call.prompt.includes("selfReportedModel")));
  assert.ok(result.modelEvidence.every((evidence) => !("selfReportedModel" in evidence)));
  assert.ok(result.findings.every((entry) => !("selfReportedModel" in entry.review)));
});

test("an absent run record and an empty progress record both fail closed", async () => {
  const absent = await runScript(ARGS, responder());
  assert.equal(absent.result.status, "incomplete");
  assert.equal(absent.result.complete, false);
  assert.ok(absent.result.modelEvidence.every((evidence) => evidence.confirmed === false));

  const empty = await runScript(
    { ...ARGS, runRecord: { workflowProgress: [] } },
    responder()
  );
  assert.equal(empty.result.status, "incomplete");
  assert.equal(empty.result.complete, false);
  assert.ok(empty.result.modelEvidence.every((evidence) => evidence.state === "absent"));
  assert.ok(empty.result.findings.every((entry) => entry.verdict === "no-verdict"));
});

test("model-less done legs and non-terminal right-model legs fail closed", async () => {
  const first = await runScript(ARGS, responder());
  const specialist = first.result.requiredModelEvidence.find((leg) => leg.role === "specialist");

  const modelLess = runRecordFor(first.result, {
    [specialist.label]: { model: undefined },
  });
  const missingModel = await runScript({ ...ARGS, runRecord: modelLess }, responder());
  const missingEvidence = missingModel.result.modelEvidence.find((entry) => entry.label === specialist.label);
  assert.equal(missingModel.result.status, "incomplete");
  assert.equal(missingEvidence.actualModel, null);
  assert.equal(missingEvidence.actualFamily, "unknown");
  assert.equal(missingEvidence.confirmed, false);

  const running = runRecordFor(first.result, {
    [specialist.label]: { state: "running" },
  });
  const notDone = await runScript({ ...ARGS, runRecord: running }, responder());
  const runningEvidence = notDone.result.modelEvidence.find((entry) => entry.label === specialist.label);
  assert.equal(notDone.result.status, "incomplete");
  assert.equal(runningEvidence.state, "running");
  assert.equal(runningEvidence.actualModel, null);
  assert.equal(runningEvidence.confirmed, false);
});

test("a wrong-family actual fallback yields no verdict and an incomplete run", async () => {
  const first = await runScript(ARGS, responder());
  const specialistLabel = first.result.requiredModelEvidence.find((leg) => leg.role === "specialist").label;
  const runRecord = runRecordFor(first.result, {
    [specialistLabel]: { fallbackModel: "claude-opus-4-8" },
  });
  const { result } = await runScript({ ...ARGS, runRecord }, responder());
  assert.equal(result.status, "incomplete");
  assert.equal(result.findings[0].verdict, "no-verdict");
  assert.match(result.findings[0].verification, /actual-model evidence/);
});

test("an absent actual model is no verdict even when the verifier returned upheld", async () => {
  const first = await runScript(ARGS, responder());
  const runRecord = runRecordFor(first.result);
  runRecord.workflowProgress = runRecord.workflowProgress.filter((record) => !record.label.startsWith("verify-"));
  const { result } = await runScript({ ...ARGS, runRecord }, responder());
  assert.equal(result.complete, false);
  assert.equal(result.findings[0].verdict, "no-verdict");
  assert.equal(result.findings[0].verify.state, "absent");
});

test("a missing verifier result is no verdict, never a refutation", async () => {
  const respond = responder({ verify: () => null });
  const { result } = await runRecorded(ARGS, respond);
  assert.equal(result.findings[0].verdict, "no-verdict");
  assert.match(result.findings[0].verification, /returned no result/);
  assert.equal(result.complete, false);
});

test("a refuted finding is dropped from confirmed findings while the run stays complete", async () => {
  const respond = responder({ verify: () => ({ verdict: "refuted", confidence: 95, reason: "guarded upstream" }) });
  const { result } = await runRecorded(ARGS, respond);
  assert.equal(result.status, "complete");
  assert.equal(result.findings[0].verdict, "refuted");
  assert.equal(result.confirmedFindings.length, 0);
});

test("a specialist with no findings completes without verifier calls", async () => {
  const respond = responder({ specialist: () => ({ findings: [] }) });
  const { result, calls } = await runRecorded(ARGS, respond);
  assert.equal(result.status, "complete");
  assert.equal(result.findings.length, 0);
  assert.equal(calls.filter((call) => call.opts.label?.startsWith("verify-")).length, 0);
});

test("a specialist that returns no result makes the run incomplete", async () => {
  const { result } = await runRecorded(ARGS, responder({ specialist: () => null }));
  assert.equal(result.status, "incomplete");
  assert.ok(result.incomplete.some((line) => line.includes("specialist")));
});

test("missing target and head fail closed before any agent dispatch", async () => {
  const { result, calls } = await runScript({}, responder());
  assert.equal(result.status, "incomplete");
  assert.equal(calls.length, 0);
  assert.match(result.incomplete[0], /target, head/);
});

test("missing deterministic brief input fails closed before exploration", async () => {
  const { result, calls } = await runScript({ target: "a", head: "b", instructionBriefs: [] }, responder());
  assert.equal(result.status, "incomplete");
  assert.equal(calls.length, 0);
  assert.match(result.incomplete[0], /omitted shipped role briefs/);
});

test("an exploration stage with no result fails closed", async () => {
  const { result } = await runScript(ARGS, responder({ exploration: null }));
  assert.equal(result.status, "incomplete");
  assert.match(result.incomplete[0], /exploration/);
});

test("specialist breadth follows the exploration plan, not raw diff size", async () => {
  const smallFiles = [{ path: "a.go", added: 1, deleted: 0 }];
  const hugeFiles = Array.from({ length: 40 }, (_, index) => ({ path: `pkg/f${index}.go`, added: 500, deleted: 500 }));
  const plan = [unit("correct", "correctness"), unit("tests", "testing")];
  const small = await runScript(ARGS, responder({ exploration: explorationFixture({ files: smallFiles, plan }), specialist: () => ({ findings: [] }) }));
  const huge = await runScript(ARGS, responder({ exploration: explorationFixture({ files: hugeFiles, plan }), specialist: () => ({ findings: [] }) }));
  assert.equal(reviewCalls(small.calls).length, 2);
  assert.equal(reviewCalls(huge.calls).length, 2);
});

test("plan clamps add correctness and cap an over-wide plan before dispatch", async () => {
  const requested = Array.from({ length: 9 }, (_, index) => unit(`security-${index}`, "security", [`pkg/f${index}.go`]));
  const { result, calls } = await runRecorded(
    ARGS,
    responder({ exploration: explorationFixture({ plan: requested }), specialist: () => ({ findings: [] }) })
  );
  assert.equal(result.plan.requested.length, 9);
  assert.equal(result.plan.dispatched.length, 6);
  assert.equal(result.plan.dispatched[0].specialistType, "correctness");
  assert.ok(result.plan.clamps.some((line) => line.includes("mandatory correctness")));
  assert.ok(result.plan.clamps.some((line) => line.includes("capped")));
  assert.deepEqual(
    reviewCalls(calls).map((call) => call.opts.label),
    result.plan.dispatched.map((entry) => entry.label)
  );
});

test("the deterministic enumerator binds a distinctive Markdown value and path into the specialist prompt", async () => {
  const { calls } = await runRecorded(ARGS, responder({ specialist: () => ({ findings: [] }) }));
  const call = calls.find((entry) => entry.opts.label?.startsWith("specialist-"));
  const correctnessBrief = ARGS.instructionBriefs.find((entry) => entry.path.endsWith("correctness.md"));
  assert.ok(call);
  assert.ok(correctnessBrief.readPath.startsWith("/"));
  assert.ok(call.prompt.includes(`Read and follow the Markdown role brief at ${correctnessBrief.readPath}.`));
  assert.match(call.prompt, /MINOS_CORRECTNESS_EVIDENCE_V1/);
  assert.match(call.prompt, /Assigned scope: internal\/x\.go/);
});

test("both confidences travel with confirmed findings and low combined confidence flags the run record", async () => {
  const respond = responder({
    specialist: () => ({ findings: [finding({ confidence: 40 })] }),
    verify: () => ({ verdict: "upheld", confidence: 60, reason: "barely upheld" }),
  });
  const { result } = await runRecorded(ARGS, respond);
  const confirmed = result.confirmedFindings[0];
  assert.equal(confirmed.confidence, 40);
  assert.equal(confirmed.verifierConfidence, 60);
  assert.equal(confirmed.combinedConfidence, 50);
  assert.equal(confirmed.operatorAttention, true);
  assert.deepEqual(result.operatorAttention, [{
    finding: confirmed.id,
    title: confirmed.title,
    combinedConfidence: 50,
    threshold: 70,
  }]);
});

test("operator attention is never raised for a refuted or high-confidence finding", async () => {
  const high = await runRecorded(ARGS, responder());
  assert.equal(high.result.operatorAttention.length, 0);
  const refuted = await runRecorded(
    ARGS,
    responder({
      specialist: () => ({ findings: [finding({ confidence: 20 })] }),
      verify: () => ({ verdict: "refuted", confidence: 20, reason: "not real" }),
    })
  );
  assert.equal(refuted.result.operatorAttention.length, 0);
});

test("a cancelled workflow is an infrastructure failure, not an ordinary incomplete verdict", async () => {
  const { result } = await runRecorded(ARGS, responder(), { extra: { terminal: "cancelled" } });
  assert.equal(result.status, "infrastructure-failure");
  assert.equal(result.infrastructureFailure.kind, "cancelled");
  assert.equal(result.complete, false);
});

test("an OOM workflow is reported as a distinct infrastructure failure", async () => {
  const { result } = await runRecorded(ARGS, responder(), { extra: { terminal: { status: "out-of-memory" } } });
  assert.equal(result.status, "infrastructure-failure");
  assert.equal(result.infrastructureFailure.kind, "oom");
});

test("failed attempts remain visible after an exact call later completes", async () => {
  const first = await runScript(ARGS, responder());
  const failed = [{ label: "specialist-1-correctness-gpt", attempt: 1, state: "error", reason: "stalled" }];
  const runRecord = runRecordFor(first.result, {}, { attempts: failed });
  const record = runRecord.workflowProgress.find((entry) => entry.label === "specialist-1-correctness-gpt");
  record.attempt = 2;
  record.lastAttemptReason = "stalled";
  const { result } = await runScript({ ...ARGS, runRecord }, responder());
  assert.equal(result.status, "complete");
  assert.deepEqual(result.accounting.attempts.attempts, failed);
  assert.equal(result.accounting.attempts.legs.find((entry) => entry.label === record.label).attempt, 2);
});

test("stage and total agent budgets bound every dispatched call", async () => {
  const plan = Array.from({ length: 9 }, (_, index) => unit(`u${index}`, index === 8 ? "correctness" : "security"));
  const respond = responder({
    exploration: explorationFixture({ plan }),
    specialist: () => ({ findings: [finding({ title: "a" }), finding({ title: "b", line: 4 })] }),
  });
  const { result, calls } = await runRecorded(ARGS, respond);
  assert.ok(result.accounting.budgets.specialists.used <= result.accounting.budgets.specialists.maximum);
  assert.ok(result.accounting.budgets.verification.used <= result.accounting.budgets.verification.maximum);
  assert.ok(result.accounting.budgets.total.used <= result.accounting.budgets.total.maximum);
  assert.equal(calls.length, result.accounting.budgets.total.used);
});

function repositoryBrief(path, frontmatter, { scopeExists = true } = {}) {
  const front = frontmatter ? `---\n${frontmatter}\n---\n` : "";
  return { path, content: `${front}Judge the concern.`, scopeExists };
}

test("a repository brief the diff gives nothing to judge is reported skipped, never passed", async () => {
  const exploration = explorationFixture({ briefs: [repositoryBrief(".review/error-tone.md", "title: Error Tone")] });
  const respond = responder({
    exploration,
    specialist: (label) => label.startsWith("repository-")
      ? { applicable: false, reason: "version-only bump", findings: [] }
      : { findings: [] },
  });
  const { result } = await runRecorded(ARGS, respond);
  const report = result.briefs.find((entry) => entry.brief === ".review/error-tone.md");
  assert.equal(report.status, "skipped");
  assert.match(report.reason, /version-only/);
});

test("an occasion-gated repository brief runs only for its named occasion", async () => {
  const brief = repositoryBrief(".review/release.md", "occasion: release");
  const respond = responder({ exploration: explorationFixture({ briefs: [brief] }), specialist: () => ({ findings: [] }) });
  const without = await runScript(ARGS, respond);
  assert.equal(without.result.briefs[0].status, "skipped");
  const matched = await runScript({ ...ARGS, occasion: "release" }, respond);
  assert.ok(matched.calls.some((call) => call.opts.label?.startsWith("repository-")));
});

test("a diff-extent scoped repository brief is skipped without dispatch when its scope did not change", async () => {
  const exploration = explorationFixture({
    files: [{ path: "cmd/main.go", added: 2, deleted: 1 }],
    briefs: [repositoryBrief(".review/internal/api/limits.md", null)],
  });
  const { result, calls } = await runScript(ARGS, responder({ exploration, specialist: () => ({ findings: [] }) }));
  assert.equal(result.briefs[0].status, "skipped");
  assert.equal(calls.filter((call) => call.opts.label?.startsWith("repository-")).length, 0);
});

test("a nonexistent repository-brief scope runs repo-wide and records a warning", async () => {
  const exploration = explorationFixture({
    briefs: [repositoryBrief(".review/ghost/limits.md", null, { scopeExists: false })],
  });
  const respond = responder({ exploration, specialist: (label) => label.startsWith("repository-")
    ? { applicable: true, reason: "audited", findings: [] }
    : { findings: [] } });
  const { result, calls } = await runRecorded(ARGS, respond);
  const report = result.briefs.find((entry) => entry.brief === ".review/ghost/limits.md");
  const call = calls.find((entry) => entry.opts.label?.startsWith("repository-"));
  assert.equal(report.status, "run");
  assert.match(report.warning, /matches no repository directory/);
  assert.match(call.prompt, /Assigned scope: the whole repository/);
});

test("a full-extent repository brief runs even when its scope did not change", async () => {
  const exploration = explorationFixture({
    files: [{ path: "cmd/main.go", added: 2, deleted: 1 }],
    briefs: [repositoryBrief(".review/internal/api/registry.md", "extent: full")],
  });
  const respond = responder({
    exploration,
    scopes: { scopes: [{ scope: "internal/api", files: ["internal/api/a.go"], bytes: 100 }] },
    specialist: (label) => label.startsWith("repository-")
      ? { applicable: true, reason: "audited", findings: [] }
      : { findings: [] },
  });
  const { result } = await runRecorded(ARGS, respond);
  assert.equal(result.briefs.find((entry) => entry.brief.endsWith("registry.md")).status, "run");
});

const filesNamed = (count) => Array.from({ length: count }, (_, index) => `pkg/f${index}.go`);

function fullBriefResponder(frontmatter, fileCount, bytes = 0) {
  return responder({
    exploration: explorationFixture({ briefs: [repositoryBrief(".review/pkg/style.md", frontmatter)] }),
    scopes: { scopes: [{ scope: "pkg", files: filesNamed(fileCount), bytes }] },
    specialist: (label) => label.startsWith("repository-")
      ? { applicable: true, reason: "audited", findings: [] }
      : { findings: [] },
  });
}

test("a large per-file full brief is split while a whole-tree brief remains one assignment", async () => {
  const perFile = await runScript(ARGS, fullBriefResponder("extent: full", 90));
  assert.equal(perFile.calls.filter((call) => call.opts.label?.startsWith("repository-")).length, 3);
  const wholeTree = await runScript(ARGS, fullBriefResponder("extent: full\nsweep: whole-tree", 90));
  assert.equal(wholeTree.calls.filter((call) => call.opts.label?.startsWith("repository-")).length, 1);
});

test("the 61-tiny-files whole-tree counterexample runs under the weighted reading budget", async () => {
  const { result, calls } = await runRecorded(ARGS, fullBriefResponder("extent: full\nsweep: whole-tree", 61, 610));
  assert.equal(result.briefs.find((entry) => entry.brief.endsWith("style.md")).status, "run");
  assert.equal(calls.filter((call) => call.opts.label?.startsWith("repository-")).length, 1);
});

test("genuinely large and few-huge whole-tree scopes are both reported not run", async () => {
  const many = await runScript(ARGS, fullBriefResponder("extent: full\nsweep: whole-tree", 250, 25_000));
  const huge = await runScript(ARGS, fullBriefResponder("extent: full\nsweep: whole-tree", 5, 500_000));
  const manyReport = many.result.briefs.find((entry) => entry.brief.endsWith("style.md"));
  const hugeReport = huge.result.briefs.find((entry) => entry.brief.endsWith("style.md"));
  assert.equal(manyReport.status, "not-run");
  assert.equal(manyReport.readingVolume, 525_000);
  assert.equal(hugeReport.status, "not-run");
  assert.equal(hugeReport.readingVolume, 510_000);
});

test("a full repository brief with no returned inventory is not run", async () => {
  const respond = responder({
    exploration: explorationFixture({ briefs: [repositoryBrief(".review/pkg/style.md", "extent: full")] }),
    scopes: () => null,
    specialist: () => ({ findings: [] }),
  });
  const { result } = await runScript(ARGS, respond);
  assert.equal(result.briefs.find((entry) => entry.brief.endsWith("style.md")).status, "not-run");
});

test("a full repository brief absent from the returned inventory is not run", async () => {
  const respond = responder({
    exploration: explorationFixture({ briefs: [repositoryBrief(".review/pkg/style.md", "extent: full")] }),
    scopes: { scopes: [{ scope: "other", files: ["other/a.go"], bytes: 10 }] },
    specialist: () => ({ findings: [] }),
  });
  const { result } = await runScript(ARGS, respond);
  assert.equal(result.briefs.find((entry) => entry.brief.endsWith("style.md")).status, "not-run");
});

test("repository-brief findings use the same opposite-family verifier and fail-closed model join", async () => {
  const exploration = explorationFixture({ briefs: [repositoryBrief(".review/error-tone.md", "title: Error Tone")] });
  const first = await runScript(ARGS, responder({ exploration }));
  const repoFinding = first.result.findings.find((entry) => entry.source === "Error Tone");
  assert.ok(repoFinding);
  assert.equal(repoFinding.verify.expectedFamily, "gpt");
  const runRecord = runRecordFor(first.result, {
    [repoFinding.verify.label]: { fallbackModel: "claude-opus-4-8" },
  });
  const { result } = await runScript({ ...ARGS, runRecord }, responder({ exploration }));
  assert.equal(result.findings.find((entry) => entry.source === "Error Tone").verdict, "no-verdict");
  assert.equal(result.status, "incomplete");
});

// The launcher resolves these two commands independently. Keep each command's
// exact-string, no-substitute, completion, empty-skip, and pre-review obligations
// bound inside its own sentence.
const COMMAND_VARS = ["$MINOS_BUILD_CMD", "$MINOS_TEST_CMD"];
const COMMAND_CLAUSES = [
  ["exact command string", /is non-empty, run that exact command string/],
  ["no repository-native substitute", /never a repository-native guess or substitute/],
  ["run to completion before review", /to completion before any review/],
  ["empty means unconfigured, skip it", /empty, no [a-z]+ command is configured, so skip it/],
];

function commandSentence(step3, variable) {
  const start = step3.indexOf("When `" + variable + "`");
  if (start === -1) return "";
  let end = step3.length;
  for (const other of COMMAND_VARS) {
    if (other === variable) continue;
    const otherStart = step3.indexOf("When `" + other + "`");
    if (otherStart > start && otherStart < end) end = otherStart;
  }
  return step3.slice(start, end);
}

function lifecycleCommandBindingFailures(text) {
  const failures = [];
  const stepMatch = text.match(/\n3\. ([\s\S]*?)\n4\. /);
  if (!stepMatch) return ["step 3 not found bounded before step 4"];
  const step3 = stepMatch[1].replace(/\s+/g, " ");
  for (const variable of COMMAND_VARS) {
    const sentence = commandSentence(step3, variable);
    if (!sentence) {
      failures.push(`${variable}: no bounded obligation sentence in step 3`);
      continue;
    }
    for (const [name, pattern] of COMMAND_CLAUSES)
      if (!pattern.test(sentence)) failures.push(`${variable}: missing ${name}`);
  }
  const workflowIndex = text.indexOf("$MINOS_REVIEW_WORKFLOW");
  if (workflowIndex === -1) failures.push("review-workflow step not found");
  for (const variable of COMMAND_VARS) {
    const index = text.indexOf(variable);
    if (index === -1 || workflowIndex === -1 || index >= workflowIndex)
      failures.push(`${variable}: not before the review-workflow step`);
  }
  return failures;
}

test("the lifecycle binds each configured command to every pre-review obligation", () => {
  assert.deepEqual(lifecycleCommandBindingFailures(lifecycle), []);
});

test("relocating the test command after the workflow fails the lifecycle binding", () => {
  const adversarial = lifecycle
    .replace(/\$MINOS_TEST_CMD/g, "$MINOS_QA_CMD")
    + "\nThen run `$MINOS_TEST_CMD`.\n";
  assert.ok(lifecycleCommandBindingFailures(adversarial).includes("$MINOS_TEST_CMD: not before the review-workflow step"));
});

test("dropping the test command's no-substitute clauses fails the lifecycle binding", () => {
  const weakened = lifecycle.replace(
    /When `\$MINOS_TEST_CMD` is non-empty,[\s\S]*?inventing one\./,
    "When `$MINOS_TEST_CMD` is non-empty, run that exact command string."
  );
  assert.ok(lifecycleCommandBindingFailures(weakened).some((failure) => failure.startsWith("$MINOS_TEST_CMD: missing")));
});

test("the lifecycle uses deterministic brief input, same-run resume, and workflow-owned completeness", () => {
  assert.match(lifecycle, /review-inputs\.mjs/);
  assert.match(lifecycle, /resumeFromRunId/);
  assert.match(lifecycle, /workflowProgress/);
  assert.match(lifecycle, /status.*`complete`/s);
  assert.doesNotMatch(lifecycle, /selfReportedModel/);
});

test("project guidance assigns proposal and verification judgement to workflow agents, not the lead", () => {
  assert.match(agentsGuidance, /reviewers propose findings/);
  assert.match(agentsGuidance, /verifiers\s+judge them/);
  assert.match(agentsGuidance, /lead consumes the workflow verdict/);
});
