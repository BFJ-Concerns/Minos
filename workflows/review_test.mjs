import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

import { adjudicate } from "./run-record-adjudicator.mjs";
import { DECISION_KIND, prepareFixWave } from "./fix-wave-plan.mjs";

const scriptPath = fileURLToPath(new URL("./review.js", import.meta.url));
const inputScriptPath = fileURLToPath(new URL("./review-inputs.mjs", import.meta.url));
const fixScriptPath = fileURLToPath(new URL("./fix.js", import.meta.url));
const source = await readFile(scriptPath, "utf8");
const fixSource = await readFile(fixScriptPath, "utf8");
const lifecycle = await readFile(fileURLToPath(new URL("../lifecycle/lifecycle.md", import.meta.url)), "utf8");
const body = source.replace(/^export const meta =/m, "const meta =");
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
const script = new AsyncFunction("agent", "parallel", "pipeline", "phase", "log", "args", body);
const fixScript = new AsyncFunction(
  "agent", "parallel", "pipeline", "phase", "log", "args",
  fixSource.replace(/^export const meta =/m, "const meta ="),
);

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
  {
    grounding = "annexe",
    guidanceName = "README.md",
    guidanceContent = "MINOS_TEST_COMMISSION_INDIGO",
    loopRecord,
    absentLoopRecord = false,
  } = {},
) {
  const root = mkdtempSync(join(tmpdir(), "minos-review-inputs-"));
  const workspace = join(root, "workspace");
  mkdirSync(workspace);
  const guidancePath = join(workspace, guidanceName);
  writeFileSync(guidancePath, guidanceContent);
  const orientationPath = join(root, "orientation.json");
  writeFileSync(orientationPath, JSON.stringify({ repository: workspace, grounding, guidance: guidancePath }));
  const cliArgs = [inputScriptPath, target, head];
  if (loopRecord !== undefined || absentLoopRecord) {
    const recordPath = join(root, "loop-record.json");
    if (!absentLoopRecord)
      writeFileSync(recordPath, typeof loopRecord === "string" ? loopRecord : JSON.stringify(loopRecord));
    cliArgs.push("--loop-record", recordPath);
  }
  return JSON.parse(execFileSync(process.execPath, cliArgs, {
    encoding: "utf8",
    env: { ...process.env, MINOS_ORIENTATION: orientationPath },
    stdio: ["ignore", "pipe", "pipe"],
  }));
}

async function runFixPlan(plan, respond) {
  const parallel = async (thunks) => Promise.all(thunks.map((thunk) => thunk().catch(() => null)));
  return fixScript(respond, parallel, async (items) => items, () => {}, () => {}, plan);
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

function observation(overrides = {}) {
  return {
    title: "pre-existing defect",
    path: "internal/legacy.go",
    line: 9,
    explanation: "Unverified observation: the unchanged branch admits an invalid state.",
    ...overrides,
  };
}

function specialistResult(
  findings = [finding()],
  applicability = { status: "applicable", reason: "the assigned change exercises the concern" },
  outOfScopeObservations = [],
) {
  return { applicability, findings, outOfScopeObservations };
}

function findingsFromVerifierPrompt(prompt) {
  const marker = "Findings: ";
  const offset = prompt.lastIndexOf(marker);
  assert.notEqual(offset, -1, "verifier prompt carries a findings array");
  return JSON.parse(prompt.slice(offset + marker.length));
}

function verifierResult(prompt, verdictFor = () => ({ verdict: "upheld", confidence: 91, reason: "reproduced" })) {
  return {
    verdicts: findingsFromVerifierPrompt(prompt).map((entry) => ({
      findingId: entry.id,
      ...verdictFor(entry),
    })),
  };
}

function responder({ exploration = explorationFixture(), specialist, verify } = {}) {
  return (label, prompt, opts) => {
    if (label === "exploration") return typeof exploration === "function" ? exploration(label, prompt, opts) : exploration;
    if (label.startsWith("verify-"))
      return verify ? verify(label, prompt, opts) : verifierResult(prompt);
    if (specialist) return specialist(label, prompt, opts);
    return specialistResult();
  };
}

function specialistCalls(calls) {
  return calls.filter((call) => call.opts.label?.startsWith("specialist-"));
}

function orientationPacketFromPrompt(prompt) {
  const match = prompt.match(/Orientation packet: (.+)\nAssigned concern:/);
  assert.ok(match, "specialist prompt carries a serialised orientation packet");
  return match[1];
}

async function adjudicateEnvelope(t, envelope) {
  const recordDir = mkdtempSync(join(tmpdir(), "minos-review-batch-record-"));
  t.after(() => rmSync(recordDir, { recursive: true, force: true }));
  const archiveDir = join(recordDir, "runs", "cwd", "synthetic", "run");
  mkdirSync(join(archiveDir, "agents"), { recursive: true });
  writeFileSync(join(archiveDir, "manifest.json"), JSON.stringify({ status: "complete" }));
  envelope.requiredModelEvidence.forEach((leg, index) => {
    const agentDir = join(archiveDir, "agents", String(index + 1).padStart(6, "0"));
    mkdirSync(agentDir);
    writeFileSync(join(agentDir, "agent.json"), JSON.stringify({
      label: leg.label,
      status: "complete",
      resolved_model: leg.pinnedModel,
    }));
  });
  return adjudicate({ envelope, recordDir });
}

test("the script emits an envelope with every routed leg and raw verifier output", async () => {
  const plan = [unit("correct", "correctness"), unit("secure", "security")];
  const { result, calls } = await runScript(ARGS, responder({ exploration: explorationFixture({ plan }) }));

  assert.deepEqual(Object.keys(result).sort(), [
    "briefs", "dispatches", "misconfigurations", "outOfScopeObservations", "proposedFindings",
    "requiredModelEvidence", "reviewed", "reviewers", "stage",
  ]);
  assert.equal(result.stage, "present");
  assert.equal(result.proposedFindings.length, 2);
  assert.deepEqual(result.proposedFindings[0].rawVerifier, { verdict: "upheld", confidence: 91, reason: "reproduced" });
  assert.ok(result.proposedFindings.every((entry) => entry.proposingLabel && entry.verifyLabel));
  assert.deepEqual(
    result.requiredModelEvidence.map(({ role, pinnedModel }) => ({ role, pinnedModel })),
    [
      { role: "exploration", pinnedModel: "gpt-5.6-terra" },
      { role: "specialist", pinnedModel: "gpt-5.6-terra" },
      { role: "specialist", pinnedModel: "gpt-5.6-terra" },
      { role: "verifier", pinnedModel: "claude-opus-5" },
      { role: "verifier", pinnedModel: "claude-opus-5" },
    ],
  );
  assert.ok(calls.every((call) => call.opts.engine === "codex" || call.opts.engine === "claude"));
  assert.ok(calls.every((call) => !("fallbackModel" in call.opts)));
});

test("an out-of-scope observation leaves the specialist without entering finding verification", async () => {
  const { result, calls } = await runScript(ARGS, responder({
    specialist: () => specialistResult([], undefined, [observation()]),
  }));

  assert.deepEqual(result.proposedFindings, []);
  assert.equal(calls.filter((call) => call.opts.label?.startsWith("verify-")).length, 0);
  assert.deepEqual(result.outOfScopeObservations, [{
    id: "specialist-1-correctness-gpt:observation:1",
    source: "correctness concern logic",
    title: "pre-existing defect",
    path: "internal/legacy.go",
    line: 9,
    explanation: "Unverified observation: the unchanged branch admits an invalid state.",
    observingLabel: "specialist-1-correctness-gpt",
    verified: false,
  }]);
  const schema = specialistCalls(calls)[0].opts.schema;
  assert.ok(!schema.required.includes("outOfScopeObservations"));
  assert.ok(schema.properties.outOfScopeObservations);
  assert.deepEqual(
    schema.properties.outOfScopeObservations.items.required,
    ["title", "path", "line", "explanation"],
  );
});

test("a specialist may omit an empty observation array", async () => {
  const { result, calls } = await runScript(ARGS, responder({
    specialist: () => ({
      applicability: { status: "applicable", reason: "the concern applies" },
      findings: [],
    }),
  }));

  assert.deepEqual(result.outOfScopeObservations, []);
  assert.deepEqual(result.proposedFindings, []);
  assert.equal(calls.filter((call) => call.opts.label?.startsWith("verify-")).length, 0);
  const schema = specialistCalls(calls)[0].opts.schema;
  assert.ok(!schema.required.includes("outOfScopeObservations"));
});

test("all findings are proposed on Terra and verified cross-family on Claude", async () => {
  const plan = [unit("correct", "correctness"), unit("secure", "security")];
  const { calls } = await runScript(ARGS, responder({ exploration: explorationFixture({ plan }) }));
  const correctness = calls.find((call) => call.opts.label === "specialist-1-correctness-gpt");
  const security = calls.find((call) => call.opts.label === "specialist-2-security-gpt");
  const correctnessVerifier = calls.find((call) => call.opts.label === "verify-1-1-claude");
  const securityVerifier = calls.find((call) => call.opts.label === "verify-2-1-claude");
  assert.deepEqual([correctness.opts.engine, correctness.opts.model], ["codex", "gpt-5.6-terra"]);
  assert.deepEqual([security.opts.engine, security.opts.model], ["codex", "gpt-5.6-terra"]);
  assert.deepEqual([correctnessVerifier.opts.engine, correctnessVerifier.opts.model], ["claude", "claude-opus-5"]);
  assert.deepEqual([securityVerifier.opts.engine, securityVerifier.opts.model], ["claude", "claude-opus-5"]);
  assert.ok(specialistCalls(calls).every((call) => call.opts.engine !== "claude"));
  assert.ok(calls.filter((call) => call.opts.label?.startsWith("verify-"))
    .every((call) => call.opts.engine !== "codex"));
});

test("specialists and verifiers use medium effort while exploration keeps its pin", async () => {
  const { calls } = await runScript(ARGS, responder());
  assert.equal(calls.find((call) => call.opts.label === "exploration").opts.effort, "high");
  assert.ok(specialistCalls(calls).every((call) => call.opts.effort === "medium"));
  assert.ok(calls.filter((call) => call.opts.label?.startsWith("verify-"))
    .every((call) => call.opts.effort === "medium"));
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

test("every specialist finding reaches its opposite-family verifier batch", async () => {
  const { result, calls } = await runScript(ARGS, responder({
    specialist: () => specialistResult([
      finding({ title: "one" }),
      finding({ title: "two", line: 4 }),
      finding({ title: "three survives", severity: "Medium", line: 5 }),
      finding({ title: "four survives", severity: "Low", line: 6 }),
    ]),
  }));
  assert.deepEqual(result.proposedFindings.map((entry) => entry.title), ["one", "two", "three survives", "four survives"]);
  const verifier = calls.find((call) => call.opts.label === "verify-1-1-claude");
  assert.ok(verifier);
  assert.match(verifier.prompt, /"title":"four survives"/);
});

test("verifier dispatch groups seven specialists independently and caps batches at six", async () => {
  const counts = [7, 1, 2, 3, 4, 5, 6];
  const types = ["correctness", "security", "testing", "design", "correctness", "security", "testing"];
  const plan = counts.map((_, index) => unit(`unit-${index + 1}`, types[index], [`pkg/f${index + 1}.go`]));
  const { result, calls } = await runScript(ARGS, responder({
    exploration: explorationFixture({ plan }),
    specialist: (label) => {
      const unitIndex = Number(label.split("-")[1]) - 1;
      return specialistResult(Array.from({ length: counts[unitIndex] }, (_, findingIndex) => finding({
        title: `unit ${unitIndex + 1} finding ${findingIndex + 1}`,
        line: findingIndex + 1,
      })));
    },
  }));

  const verifierCalls = calls.filter((call) => call.opts.label?.startsWith("verify-"));
  assert.equal(result.proposedFindings.length, counts.reduce((total, count) => total + count, 0));
  assert.equal(verifierCalls.length, 8);
  assert.deepEqual(verifierCalls.map((call) => findingsFromVerifierPrompt(call.prompt).length), [6, 1, 1, 2, 3, 4, 5, 6]);
  assert.deepEqual(
    result.requiredModelEvidence.filter((leg) => leg.role === "verifier").map((leg) => leg.findingIds.length),
    [6, 1, 1, 2, 3, 4, 5, 6],
  );
  assert.deepEqual(verifierCalls.map((call) => call.opts.label), [
    "verify-1-1-claude", "verify-1-2-claude", "verify-2-1-claude", "verify-3-1-claude",
    "verify-4-1-claude", "verify-5-1-claude", "verify-6-1-claude", "verify-7-1-claude",
  ]);
});

for (const responseCase of [
  {
    name: "missing ids",
    response(findings) {
      return { verdicts: [findings[0], findings[2]].map((entry) => ({
        findingId: entry.id, verdict: "upheld", confidence: 91, reason: "checked",
      })) };
    },
    present: [true, false, true],
  },
  {
    name: "duplicate ids",
    response(findings) {
      return { verdicts: [findings[0], findings[0], findings[1], findings[2]].map((entry) => ({
        findingId: entry.id, verdict: "upheld", confidence: 91, reason: "checked",
      })) };
    },
    present: [false, true, true],
  },
  {
    name: "unknown ids",
    response(findings) {
      return { verdicts: [...findings, { id: "unknown-finding" }].map((entry) => ({
        findingId: entry.id, verdict: "upheld", confidence: 91, reason: "checked",
      })) };
    },
    present: [false, false, false],
  },
]) {
  test(`a batched verifier response fails closed on ${responseCase.name}`, async () => {
    const { result } = await runScript(ARGS, responder({
      specialist: () => specialistResult([
        finding({ title: "one" }),
        finding({ title: "two", line: 4 }),
        finding({ title: "three", line: 5 }),
      ]),
      verify: (_label, prompt) => responseCase.response(findingsFromVerifierPrompt(prompt)),
    }));
    assert.deepEqual(result.proposedFindings.map((entry) => Boolean(entry.rawVerifier)), responseCase.present);
  });
}

test("a full synthetic batched run preserves per-finding adjudication", async (t) => {
  const { result } = await runScript(ARGS, responder({
    specialist: () => specialistResult([
      finding({ title: "upheld one" }),
      finding({ title: "refuted", line: 4 }),
      finding({ title: "upheld two", line: 5 }),
    ]),
    verify: (_label, prompt) => verifierResult(prompt, (entry) => entry.title === "refuted"
      ? { verdict: "refuted", confidence: 84, reason: "caller rejects the state" }
      : { verdict: "upheld", confidence: 93, reason: "path remains reachable" }),
  }));

  assert.deepEqual(result.proposedFindings.map(({ id, rawVerifier }) => ({ id, rawVerifier })), [
    {
      id: "specialist-1-correctness-gpt:1",
      rawVerifier: { verdict: "upheld", confidence: 93, reason: "path remains reachable" },
    },
    {
      id: "specialist-1-correctness-gpt:2",
      rawVerifier: { verdict: "refuted", confidence: 84, reason: "caller rejects the state" },
    },
    {
      id: "specialist-1-correctness-gpt:3",
      rawVerifier: { verdict: "upheld", confidence: 93, reason: "path remains reachable" },
    },
  ]);
  const verdict = await adjudicateEnvelope(t, result);
  assert.equal(verdict.status, "complete", verdict.incomplete.join("\n"));
  assert.deepEqual(verdict.confirmedFindings.map((entry) => entry.id), [
    "specialist-1-correctness-gpt:1",
    "specialist-1-correctness-gpt:3",
  ]);
  assert.deepEqual(
    verdict.modelEvidence.find((entry) => entry.role === "verifier").findingIds,
    result.proposedFindings.map((entry) => entry.id),
  );
});

test("every specialist prompt carries the compact exploration orientation packet", async () => {
  const exploration = explorationFixture({
    files: [
      { path: "cmd/minos/main.go", added: 12, deleted: 3 },
      { path: "internal/review.go", added: 7, deleted: 11 },
    ],
    plan: [
      unit("entrypoint", "correctness", ["cmd/minos/main.go"]),
      unit("review", "design", ["internal/review.go"]),
    ],
  });
  const { calls } = await runScript(enumeratedArgs("target-orientation", "head-orientation"), responder({ exploration }));
  for (const call of specialistCalls(calls)) {
    assert.match(call.prompt, /"commitRange":"target-orientation\.\.\.head-orientation"/);
    assert.match(call.prompt, /"path":"cmd\/minos\/main\.go","added":12,"deleted":3/);
    assert.match(call.prompt, /"path":"internal\/review\.go","added":7,"deleted":11/);
    assert.match(call.prompt, /"unit":"entrypoint"/);
    assert.match(call.prompt, /"unit":"review"/);
  }
});

test("large exploration results keep every orientation packet within 32 KiB", async () => {
  const files = Array.from({ length: 166 }, (_, index) => ({
    path: `packages/${String(index).padStart(3, "0")}-${"changed-component-".repeat(9)}.js`,
    added: index + 1,
    deleted: index,
  }));
  const plan = Array.from({ length: 20 }, (_, index) => unit(
    `unit-${index + 1}`,
    ["correctness", "security", "testing", "design"][index % 4],
    files.filter((_, fileIndex) => fileIndex % 20 === index).map((file) => file.path),
  ));
  const { calls } = await runScript(ARGS, responder({
    exploration: explorationFixture({ files, plan }),
    specialist: () => specialistResult([]),
  }));

  for (const call of specialistCalls(calls)) {
    const packet = orientationPacketFromPrompt(call.prompt);
    assert.ok(Buffer.byteLength(packet, "utf8") <= 32 * 1024, `packet is ${Buffer.byteLength(packet, "utf8")} bytes`);
    assert.match(packet, /\+\d+ more files/);
    assert.match(packet, /"scopeCount":\d+/);
    assert.doesNotMatch(packet, /"scope":\[/);
  }
});

test("specialist and verifier briefs pin the review-stage discipline", async () => {
  const specialistBriefs = ["correctness", "security", "testing", "design", "repository"];
  for (const brief of specialistBriefs) {
    const content = await readFile(fileURLToPath(new URL(`./review-briefs/${brief}.md`, import.meta.url)), "utf8");
    assert.match(content, /do not investigate outside your assigned boundary/i);
    assert.match(content, /Do not build side experiments during proposal/);
    if (brief !== "repository")
      assert.match(content, /Trust the packet for orientation; read the diff for your scope directly\./);
  }
  const verifierBrief = await readFile(fileURLToPath(new URL("./review-briefs/verifier.md", import.meta.url)), "utf8");
  assert.match(verifierBrief, /return exactly one verdict keyed by\s+finding id for every member/i);
  assert.match(verifierBrief, /run the focused\s+confirming experiment described by the specialist/i);
  assert.match(verifierBrief, /Return one structured verdict with an\s+independent confidence integer from 0 to 100\./);
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

test("a two-round journey carries fixed and unfixed findings into later specialist judgement", async () => {
  const fixedSentinel = finding({
    id: "round-one-fixed",
    title: "ROUND_ONE_FIXED_SENTINEL",
    path: "internal/fixed.go",
    line: 11,
    explanation: "ROUND_ONE_FIXED_EXPLANATION_SENTINEL",
  });
  const unfixedSentinel = finding({
    id: "round-one-unfixed",
    title: "ROUND_ONE_UNFIXED_SENTINEL",
    path: "internal/unfixed.go",
    line: 22,
    explanation: "ROUND_ONE_UNFIXED_EXPLANATION_SENTINEL",
  });
  const input = {
    review: {
      status: "complete",
      reviewed: { target: "target-round-one", head: "head-round-one" },
      confirmedFindings: [fixedSentinel, unfixedSentinel],
    },
    threshold: "High",
    maximumRounds: null,
    runRecord: { round: 0, confirmedFixed: [], confirmedUnfixed: [] },
    decision: {
      kind: DECISION_KIND,
      classification: "working",
      basis: "both confirmed findings warrant one repair wave",
    },
    workspace: "/run/workspace",
    verification: { build: "", tests: "" },
    guidance: ARGS.guidance,
    fixerBrief: ARGS.instructionBriefs.find((entry) => entry.path.endsWith("correctness.md")),
  };
  const plan = prepareFixWave(input);
  const roundOne = await runFixPlan(plan, async (_prompt, options) => {
    const dispatch = plan.dispatches.find((entry) => entry.label === options.label);
    const key = dispatch.findingKeys[0];
    const isFixed = plan.findings.find((entry) => entry.key === key).title === fixedSentinel.title;
    return {
      commit: isFixed ? "fixed-sentinel-commit" : "",
      fixes: [{ findingKey: key, status: isFixed ? "fixed" : "failed", writeUp: isFixed ? "fixed sentinel repair" : "still unfixed" }],
    };
  });
  assert.equal(roundOne.runRecord.confirmedFixed[0].finding.title, fixedSentinel.title);
  assert.equal(roundOne.runRecord.confirmedUnfixed[0].finding.title, unfixedSentinel.title);

  const roundTwoArgs = enumeratedArgs("target-round-two", "head-round-two", {
    loopRecord: roundOne.runRecord,
  });
  const newSentinel = "ROUND_TWO_GENUINELY_NEW_SENTINEL";
  async function assertHistoryReachesSpecialists(args) {
    const { result, calls } = await runScript(args, responder({
      specialist: (_label, prompt) => specialistResult(
        prompt.includes("a genuinely new finding remains reportable")
          ? [finding({ title: newSentinel, path: "internal/new.go", line: 33 })]
          : [],
      ),
    }));
    const prompts = specialistCalls(calls).map((call) => call.prompt);
    assert.ok(prompts.length > 0);
    assert.ok(prompts.every((prompt) => prompt.includes(fixedSentinel.title)), "fixed sentinel reaches every specialist");
    assert.ok(prompts.every((prompt) => prompt.includes(unfixedSentinel.title)), "unfixed sentinel reaches every specialist");
    assert.ok(result.proposedFindings.some((entry) => entry.title === newSentinel), "new sentinel remains reportable");
  }
  await assertHistoryReachesSpecialists(roundTwoArgs);
  await assert.rejects(
    assertHistoryReachesSpecialists({
      ...roundTwoArgs,
      priorFindings: { confirmedFixed: [], confirmedUnfixed: [] },
    }),
    /fixed sentinel reaches every specialist/,
  );
});

test("review inputs provide stable empty context and fail closed on malformed records", async () => {
  assert.deepEqual(enumeratedArgs().priorFindings, { confirmedFixed: [], confirmedUnfixed: [] });
  assert.deepEqual(
    enumeratedArgs("target", "head", { absentLoopRecord: true }).priorFindings,
    { confirmedFixed: [], confirmedUnfixed: [] },
  );
  const malformed = enumeratedArgs("target", "head", { loopRecord: "not JSON" });
  await assert.rejects(runScript(malformed, responder()), /needs priorFindings/);

  const unreadable = mkdtempSync(join(tmpdir(), "minos-unreadable-loop-record-"));
  const result = spawnSync(process.execPath, [inputScriptPath, "target", "head", "--loop-record", unreadable], {
    encoding: "utf8",
    env: { ...process.env, MINOS_ORIENTATION: "unused" },
  });
  assert.equal(result.status, 2);
  assert.equal(result.stdout, "");
  assert.match(result.stderr, /review loop record is unreadable/);
  assert.match(result.stderr, /usage: node workflows\/review-inputs\.mjs/);
  assert.doesNotMatch(result.stderr, /^\s+at /m);
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
    pinnedModel: "gpt-5.6-terra",
  }]);
  assert.equal(result.reviewers[0].status, "no-result");
});

test("the lifecycle uses one adjudicated review call and publication-owned fix waves", () => {
  assert.match(lifecycle, /invoke the adjudication wrapper once[\s\S]*review\.js[\s\S]*--json-args/);
  assert.match(lifecycle, /review-inputs\.mjs[\s\S]*--loop-record "\$MINOS_LOOP_RECORD"/);
  assert.match(
    lifecycle,
    /`outOfScopeObservations`[\s\S]*unverified observations, not findings[\s\S]*do not publish them through this lifecycle/,
  );
  assert.match(lifecycle, /--digest[\s\S]*sweep-digest\.json/);
  assert.match(
    lifecycle,
    /Classifying the sweep is your judgement[\s\S]*informed by the threshold rather than mechanically bound to it/,
  );
  assert.match(lifecycle, /minos-sweep-decision-v1[\s\S]*--decision[\s\S]*fix-args\.json/);
  assert.match(lifecycle, /publish-before-fix[\s\S]*fix-args\.json[\s\S]*HEAD TARGET/);
  assert.match(lifecycle, /starts the effectful `fix\.js`[\s\S]*only after[\s\S]*`outcome: "applied"`/);
  assert.match(lifecycle, /Do not post the terminal sweep's sub-threshold[\s\S]*findings/);
  assert.doesNotMatch(lifecycle, /workflowProgress|resumeFromRunId|--workflow-script|briefReview/);
  assert.match(
    lifecycle,
    /rootcause\.js[\s\S]*root-cause skill on `codex` \/ `gpt-5\.6-sol`[\s\S]*isolation: "worktree"[\s\S]*commit a repair[\s\S]*never push/,
  );
  assert.match(lifecycle, /helper returns a non-empty commit[\s\S]*integrate-wave/);
});

test("configured command failures stop before initial or repeated review", () => {
  const initialGate = lifecycle.slice(lifecycle.indexOf("3. Read"), lifecycle.indexOf("4. Run"));
  assert.match(
    initialGate,
    /\$\{MINOS_REVIEW_WORKFLOW%\/\*\}\/completion-policy\.mjs[\s\S]*JSON `status` field[\s\S]*non-zero exit status[\s\S]*`\$MINOS_FAILURE_LOG`[\s\S]*status HEAD TARGET incomplete[\s\S]*reaction-remove HEAD TARGET eyes[\s\S]*non-clean[\s\S]*stop before starting any review/,
  );
  assert.match(initialGate, /empty, no test command is configured[\s\S]*skip it/);

  const repeatedGate = lifecycle.slice(lifecycle.indexOf("After the push"), lifecycle.indexOf("6. A `terminal`"));
  assert.match(
    repeatedGate,
    /\$\{MINOS_REVIEW_WORKFLOW%\/\*\}\/completion-policy\.mjs[\s\S]*JSON `status` field[\s\S]*empty strings are skipped[\s\S]*non-zero exit status[\s\S]*`\$MINOS_FAILURE_LOG`[\s\S]*incomplete[\s\S]*reaction-remove[\s\S]*non-clean[\s\S]*stop before another review round or merge action[\s\S]*Only after both gates pass or skip[\s\S]*adjudication-wrapper/,
  );
});

test("only an unchanged or verified tests-only finishing result may reach the merge path", () => {
  const finishing = lifecycle.slice(lifecycle.indexOf("8. Enter finishing"), lifecycle.indexOf("9. If"));
  assert.match(
    finishing,
    /red-check path[\s\S]*label-only path/,
  );
  assert.match(
    finishing,
    /each fresh snapshot[\s\S]*`dependencies_available`[\s\S]*`open_dependencies`[\s\S]*set `incomplete`/,
  );
  assert.match(
    finishing,
    /forge check-logs HEAD TARGET[\s\S]*check-logs\.json[\s\S]*Give the helper that file/,
  );
  assert.match(
    finishing,
    /classify-finishing-change\.mjs[\s\S]*`tests-only` classification[\s\S]*continue to step 9/,
  );
  assert.match(
    finishing,
    /`review-required` classification[\s\S]*set `incomplete`[\s\S]*fresh whole review/,
  );
  assert.match(
    finishing,
    /`writeUp`[\s\S]*rootcause-result\.json/,
  );
  assert.match(
    finishing,
    /helper's `writeUp`[\s\S]*post[\s\S]*one `comment` review/,
  );
  assert.match(
    finishing,
    /helper returns no commit and no pushed head appears[\s\S]*made no[\s\S]*mutation[\s\S]*red-check path[\s\S]*set[\s\S]*`incomplete`[\s\S]*Only on the label-only[\s\S]*step 9 receive the unchanged verified head[\s\S]*and target/,
  );
});
