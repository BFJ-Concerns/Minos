import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

import { invokeWorkflow } from "./workflow-invocation-fixture.mjs";
import { adjudicateEnvelope } from "./archive-fixture.mjs";
import { findingsFromVerifierPrompt } from "./verifier-prompt-fixture.mjs";

const scriptPath = fileURLToPath(new URL("./review.js", import.meta.url));
const inputScriptPath = fileURLToPath(new URL("./review-inputs.mjs", import.meta.url));
const source = await readFile(scriptPath, "utf8");
const lifecycle = await readFile(fileURLToPath(new URL("../lifecycle/lifecycle.md", import.meta.url)), "utf8");
const body = source.replace(/^export const meta =/m, "const meta =");
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
const script = new AsyncFunction("agent", "parallel", "pipeline", "phase", "log", "args", body);

function runScript(args, respond) {
  return invokeWorkflow(script, args, respond);
}

function enumeratedArgs(
  target = "aaa111",
  head = "bbb222",
  {
    guidanceName = "README.md",
    guidanceContent = "MINOS_TEST_COMMISSION_INDIGO",
    secondaryGuidance,
    pullRequest,
    absentPullRequestRecord = false,
    provisioned = "claude codex",
    routing,
  } = {},
) {
  const root = mkdtempSync(join(tmpdir(), "minos-review-inputs-"));
  try {
    const workspace = join(root, "workspace");
    mkdirSync(workspace);
    const guidancePath = join(workspace, guidanceName);
    writeFileSync(guidancePath, guidanceContent);
    // The orientation setup-workspace writes: one entry per guidance document,
    // a configured secondary repository's document first when the test names one.
    const guidance = [];
    if (secondaryGuidance !== undefined) {
      const secondaryPath = join(root, "guidance", "owner--plans", "README.md");
      mkdirSync(join(root, "guidance", "owner--plans"), { recursive: true });
      writeFileSync(secondaryPath, secondaryGuidance);
      guidance.push({ source: { repository: "owner/plans", path: "README.md" }, location: secondaryPath, origin: "configured" });
    }
    guidance.push({ source: { path: guidanceName }, location: guidancePath, origin: secondaryGuidance === undefined ? "checked-in" : "configured" });
    const orientation = { repository: workspace, guidance, misconfigurations: [] };
    if (pullRequest !== undefined || absentPullRequestRecord) {
      const pullRequestPath = join(root, "pull-request.json");
      if (!absentPullRequestRecord)
        writeFileSync(pullRequestPath, typeof pullRequest === "string" ? pullRequest : JSON.stringify(pullRequest));
      orientation.pullRequest = pullRequestPath;
    }
    const orientationPath = join(root, "orientation.json");
    writeFileSync(orientationPath, JSON.stringify(orientation));
    const cliArgs = [inputScriptPath, target, head];
    return JSON.parse(execFileSync(process.execPath, cliArgs, {
      encoding: "utf8",
      env: {
        ...process.env,
        MINOS_ORIENTATION: orientationPath,
        MINOS_OWNER: "minos-e2e-owner",
        MINOS_REPO_NAME: "subject",
        MINOS_PR: "1",
        MINOS_PROVISIONED_ENGINES: provisioned,
        ...(routing === undefined ? { MINOS_ROUTING: "" } : { MINOS_ROUTING: JSON.stringify(routing) }),
      },
      stdio: ["ignore", "pipe", "pipe"],
    }));
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
}

const ARGS = enumeratedArgs();

test("a review input carries no grouped member or loop record", () => {
  assert.equal(ARGS.members, undefined);
  assert.equal(ARGS.priorFindings, undefined);
});

function unit(id = "logic", specialistType = "correctness", scope = ["internal/x.go"]) {
  return { id, concern: `${specialistType} concern ${id}`, scope, specialistType };
}

function explorationFixture({
  files = [{ path: "internal/x.go", added: 30, deleted: 30 }],
  plan = [unit()],
  applicability = { reason: "the change engages the planned review concerns" },
} = {}) {
  return { files, plan, applicability };
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
  const match = prompt.match(/Orientation packet: ([^\n]+)\n/);
  assert.ok(match, "specialist prompt carries a serialised orientation packet");
  return match[1];
}

test("the script emits an envelope with every routed leg and raw verifier output", async () => {
  const plan = [unit("correct", "correctness"), unit("secure", "security")];
  const { result, calls } = await runScript(ARGS, responder({ exploration: explorationFixture({ plan }) }));

  assert.deepEqual(Object.keys(result).sort(), [
    "briefs", "dispatches", "exploration", "misconfigurations", "outOfScopeObservations", "proposedFindings",
    "requiredModelEvidence", "reviewed", "reviewers", "stage",
  ]);
  assert.equal(result.stage, "present");
  assert.equal(result.proposedFindings.length, 2);
  assert.deepEqual(result.proposedFindings[0].rawVerifier, { verdict: "upheld", confidence: 91, reason: "reproduced" });
  assert.ok(result.proposedFindings.every((entry) => entry.proposingLabel && entry.verifyLabel));
  assert.deepEqual(
    result.requiredModelEvidence.map(({ role, pinnedModel }) => ({ role, pinnedModel })),
    [
      { role: "exploration", pinnedModel: "gpt-6-sol" },
      { role: "specialist", pinnedModel: "gpt-6-sol" },
      { role: "specialist", pinnedModel: "gpt-6-sol" },
      { role: "verifier", pinnedModel: "claude-opus-5-5" },
    ],
  );
  assert.ok(calls.every((call) => call.opts.engine === "codex" || call.opts.engine === "claude"));
  assert.ok(calls.every((call) => !("fallbackModel" in call.opts)));
});

test("malformed exploration scope entries fail closed before specialist dispatch", async (t) => {
  for (const malformedScope of [
    ["internal/group_execution.rs],"],
    ["specialistType"],
    ["correctness"],
  ]) {
    const exploration = explorationFixture({
      files: [{ path: "internal/group_execution.rs", added: 4, deleted: 1 }],
      plan: [unit("group-execution", "correctness", malformedScope)],
    });
    const { result, calls } = await runScript(ARGS, responder({ exploration }));

    assert.deepEqual(specialistCalls(calls), []);
    assert.deepEqual(result, {
      reviewed: { target: ARGS.target, head: ARGS.head, occasion: null },
      stage: "present",
      incomplete: ["exploration returned a malformed review plan"],
      requiredModelEvidence: [{
        label: "exploration",
        role: "exploration",
        pinnedModel: "gpt-6-sol",
      }],
      proposedFindings: [],
      outOfScopeObservations: [],
      briefs: [],
      misconfigurations: [],
      dispatches: [],
      reviewers: [{
        label: "exploration",
        role: "exploration",
        status: "no-result",
        reason: "exploration returned a malformed review plan",
      }],
    });
    const verdict = await adjudicateEnvelope(t, result);
    assert.equal(verdict.status, "incomplete");
    assert.equal(verdict.complete, false);
    assert.deepEqual(verdict.incomplete, [
      "workflow reported incomplete: exploration returned a malformed review plan",
    ]);
    assert.deepEqual(verdict.confirmedFindings, []);
  }
});

test("a well-formed exploration scope reaches specialists unchanged", async () => {
  const scope = ["internal/group_execution.rs", "internal/group_execution_test.rs"];
  const plan = [unit("group-execution", "correctness", scope)];
  const exploration = explorationFixture({
    files: scope.map((path) => ({ path, added: 4, deleted: 1 })),
    plan,
  });
  const { result, calls } = await runScript(ARGS, responder({ exploration }));
  const specialist = specialistCalls(calls)[0];

  assert.deepEqual(result.exploration.plan, plan);
  assert.deepEqual(result.dispatches[0].scope, scope);
  assert.deepEqual(JSON.parse(orientationPacketFromPrompt(specialist.prompt)).ownership[0].scope, scope);
  assert.match(specialist.prompt, /Assigned scope: internal\/group_execution\.rs, internal\/group_execution_test\.rs/);
});

test("an out-of-scope observation leaves the specialist without entering finding verification", async () => {
  const { result, calls } = await runScript(ARGS, responder({
    specialist: () => specialistResult([], undefined, [observation()]),
  }));

  assert.deepEqual(result.proposedFindings, []);
  assert.equal(calls.filter((call) => call.opts.label?.startsWith("verify-")).length, 0);
  assert.deepEqual(result.outOfScopeObservations, [{
    id: "specialist-1-correctness-codex:observation:1",
    source: "correctness concern logic",
    title: "pre-existing defect",
    path: "internal/legacy.go",
    line: 9,
    explanation: "Unverified observation: the unchanged branch admits an invalid state.",
    observingLabel: "specialist-1-correctness-codex",
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

test("all findings are proposed on Sol and verified cross-family on Claude", async () => {
  const plan = [unit("correct", "correctness"), unit("secure", "security")];
  const { calls } = await runScript(ARGS, responder({ exploration: explorationFixture({ plan }) }));
  const correctness = calls.find((call) => call.opts.label === "specialist-1-correctness-codex");
  const security = calls.find((call) => call.opts.label === "specialist-2-security-codex");
  const verifiers = calls.filter((call) => call.opts.label?.startsWith("verify-"));
  assert.deepEqual([correctness.opts.engine, correctness.opts.model], ["codex", "gpt-6-sol"]);
  assert.deepEqual([security.opts.engine, security.opts.model], ["codex", "gpt-6-sol"]);
  assert.ok(verifiers.length > 0);
  assert.ok(verifiers.every((call) => call.opts.engine === "claude" && call.opts.model === "claude-opus-5-5"));
  assert.ok(specialistCalls(calls).every((call) => call.opts.engine !== "claude"));
  assert.ok(calls.filter((call) => call.opts.label?.startsWith("verify-"))
    .every((call) => call.opts.engine !== "codex"));
});

test("a verifier observation reaches the result alongside its verdicts", async () => {
  const { result, calls } = await runScript(ARGS, responder({
    verify: (label, prompt) => ({
      ...verifierResult(prompt),
      outOfScopeObservations: [observation({ title: "residual defect the verdict cannot carry" })],
    }),
  }));
  assert.deepEqual(result.outOfScopeObservations, [{
    id: "verify-1-claude:observation:1",
    source: "verification",
    title: "residual defect the verdict cannot carry",
    path: "internal/legacy.go",
    line: 9,
    explanation: "Unverified observation: the unchanged branch admits an invalid state.",
    observingLabel: "verify-1-claude",
    verified: false,
  }]);
  const schema = calls.find((call) => call.opts.label === "verify-1-claude").opts.schema;
  assert.ok(!schema.required.includes("outOfScopeObservations"));
  assert.deepEqual(
    schema.properties.outOfScopeObservations.items.required,
    ["title", "path", "line", "explanation"],
  );
});

test("a verifier observation survives a verdict set discarded as malformed", async () => {
  const { result } = await runScript(ARGS, responder({
    verify: () => ({
      verdicts: [{ findingId: "not-a-proposed-finding", verdict: "upheld", confidence: 90, reason: "misaddressed" }],
      outOfScopeObservations: [observation()],
    }),
  }));
  assert.ok(result.proposedFindings.every((finding) => finding.rawVerifier === null));
  assert.equal(result.outOfScopeObservations.length, 1);
  assert.equal(result.outOfScopeObservations[0].source, "verification");
});

test("every leg runs on the routing the input carries, and labels follow the routed engine", async () => {
  const args = enumeratedArgs("aaa111", "bbb222", {
    routing: { verifier: { engine: "codex", model: "gpt-6-astra", effort: "low" }, exploration: { engine: "claude" } },
  });
  assert.deepEqual(args.routing.verifier, { engine: "codex", model: "gpt-6-astra", effort: "low" });
  assert.deepEqual(args.routing.exploration, { engine: "claude", model: "claude-opus-5-5", effort: "high" });
  const { result, calls } = await runScript(args, responder());
  const exploration = calls.find((call) => call.opts.label === "exploration");
  assert.deepEqual([exploration.opts.engine, exploration.opts.model, exploration.opts.effort], ["claude", "claude-opus-5-5", "high"]);
  const verifiers = calls.filter((call) => call.opts.label?.startsWith("verify-"));
  assert.ok(verifiers.length > 0);
  for (const verifier of verifiers) {
    assert.deepEqual([verifier.opts.engine, verifier.opts.model, verifier.opts.effort], ["codex", "gpt-6-astra", "low"]);
    assert.match(verifier.opts.label, /-codex$/);
  }
  assert.ok(specialistCalls(calls).every((call) => call.opts.label.endsWith("-codex")));
  assert.deepEqual(
    result.requiredModelEvidence.filter((leg) => leg.role === "verifier").map((leg) => leg.pinnedModel),
    verifiers.map(() => "gpt-6-astra"),
  );
});

test("a single-engine deployment routes every role to the one provisioned engine", async () => {
  const args = enumeratedArgs("aaa111", "bbb222", { provisioned: "claude" });
  const { calls } = await runScript(args, responder());
  assert.ok(calls.length >= 3);
  assert.ok(calls.every((call) => call.opts.engine === "claude" && call.opts.model === "claude-opus-5-5"));
});

test("the input builder refuses routing it cannot honour, and the workflow refuses input without routing", async () => {
  assert.throws(
    () => enumeratedArgs("aaa111", "bbb222", { provisioned: "claude", routing: { verifier: { engine: "codex" } } }),
    /routing\.verifier\.engine names codex, which this deployment has not provisioned/,
  );
  assert.throws(() => enumeratedArgs("aaa111", "bbb222", { provisioned: "" }), /MINOS_PROVISIONED_ENGINES is required/);
  const calls = [];
  const { routing, ...withoutRouting } = ARGS;
  await assert.rejects(
    runScript(withoutRouting, (label) => { calls.push(label); return null; }),
    /omitted the role routing/,
  );
  await assert.rejects(
    runScript({ ...ARGS, routing: { ...routing, proposer: { engine: "opencode", model: "x", effort: "low" } } }, (label) => { calls.push(label); return null; }),
    /omitted the role routing/,
  );
  assert.deepEqual(calls, []);
});

test("exploration and specialists run at high effort while verifiers stay at medium", async () => {
  const { calls } = await runScript(ARGS, responder());
  assert.equal(calls.find((call) => call.opts.label === "exploration").opts.effort, "high");
  assert.ok(specialistCalls(calls).every((call) => call.opts.effort === "high"));
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

test("the review plan dispatches exactly every requested specialist", async () => {
  const requested = Array.from({ length: 9 }, (_, index) => unit(`security-${index}`, "security", [`pkg/f${index}.go`]));
  const files = requested.map((entry) => ({ path: entry.scope[0], added: 1, deleted: 0 }));
  const { result, calls } = await runScript(ARGS, responder({
    exploration: explorationFixture({ files, plan: requested }),
    specialist: () => specialistResult([]),
  }));
  assert.equal(result.dispatches.length, 9);
  assert.ok(result.dispatches.every((entry) => entry.specialistType === "security"));
  assert.equal(specialistCalls(calls).length, 9);
  assert.deepEqual(specialistCalls(calls).map((call) => call.opts.label), result.dispatches.map((entry) => entry.label));
  assert.deepEqual(result.exploration.applicability, {
    status: "applicable",
    reason: "the change engages the planned review concerns",
  });
});

test("an empty plan completes through the adjudicator's no-findings path", async (t) => {
  const exploration = explorationFixture({
    plan: [],
    applicability: { reason: "only generated documentation changed" },
  });
  const { result, calls } = await runScript(ARGS, responder({ exploration }));

  assert.deepEqual(result.exploration, {
    ...exploration,
    applicability: { status: "inapplicable", reason: "only generated documentation changed" },
  });
  assert.deepEqual(result.dispatches, []);
  assert.deepEqual(result.reviewers, []);
  assert.deepEqual(result.proposedFindings, []);
  assert.deepEqual(result.requiredModelEvidence.map((leg) => leg.role), ["exploration"]);
  assert.equal(specialistCalls(calls).length, 0);
  assert.equal(calls.filter((call) => call.opts.label?.startsWith("verify-")).length, 0);
  const explorationSchema = calls.find((call) => call.opts.label === "exploration").opts.schema;
  assert.ok(explorationSchema.required.includes("applicability"));
  assert.deepEqual(explorationSchema.properties.applicability.required, ["reason"]);
  assert.equal(explorationSchema.properties.applicability.properties.status, undefined);
  const verdict = await adjudicateEnvelope(t, result);
  assert.equal(verdict.status, "complete", verdict.incomplete.join("\n"));
  assert.deepEqual(verdict.confirmedFindings, []);
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
  const verifier = calls.find((call) => call.opts.label === "verify-1-claude");
  assert.ok(verifier);
  assert.match(verifier.prompt, /"title":"four survives"/);
});

test("verifier dispatch pools findings globally and caps batches at six", async () => {
  const counts = [7, 1, 2, 3, 4, 5, 6];
  const types = ["correctness", "security", "testing", "design", "correctness", "security", "testing"];
  const plan = counts.map((_, index) => unit(`unit-${index + 1}`, types[index], [`pkg/f${index + 1}.go`]));
  const files = plan.map((entry) => ({ path: entry.scope[0], added: 1, deleted: 0 }));
  const { result, calls } = await runScript(ARGS, responder({
    exploration: explorationFixture({ files, plan }),
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
  assert.equal(verifierCalls.length, 5);
  assert.deepEqual(verifierCalls.map((call) => findingsFromVerifierPrompt(call.prompt).length), [6, 6, 6, 6, 4]);
  assert.deepEqual(
    result.requiredModelEvidence.filter((leg) => leg.role === "verifier").map((leg) => leg.findingIds.length),
    [6, 6, 6, 6, 4],
  );
  assert.deepEqual(verifierCalls.map((call) => call.opts.label), [
    "verify-1-claude", "verify-2-claude", "verify-3-claude", "verify-4-claude", "verify-5-claude",
  ]);
});

test("findings distributed across several units share one verifier leg", async () => {
  const plan = [unit("one", "correctness"), unit("two", "security"), unit("three", "testing")];
  const { result, calls } = await runScript(ARGS, responder({
    exploration: explorationFixture({ plan }),
    specialist: (label) => specialistResult([finding({ title: `finding from ${label}` })]),
  }));

  const verifierCalls = calls.filter((call) => call.opts.label?.startsWith("verify-"));
  assert.equal(verifierCalls.length, 1);
  assert.equal(verifierCalls[0].opts.label, "verify-1-claude");
  assert.equal(findingsFromVerifierPrompt(verifierCalls[0].prompt).length, 3);
  assert.deepEqual(new Set(result.proposedFindings.map((entry) => entry.proposingLabel)), new Set([
    "specialist-1-correctness-codex", "specialist-2-security-codex", "specialist-3-testing-codex",
  ]));
  assert.ok(result.proposedFindings.every((entry) => entry.verifyLabel === "verify-1-claude"));
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
      id: "specialist-1-correctness-codex:1",
      rawVerifier: { verdict: "upheld", confidence: 93, reason: "path remains reachable" },
    },
    {
      id: "specialist-1-correctness-codex:2",
      rawVerifier: { verdict: "refuted", confidence: 84, reason: "caller rejects the state" },
    },
    {
      id: "specialist-1-correctness-codex:3",
      rawVerifier: { verdict: "upheld", confidence: 93, reason: "path remains reachable" },
    },
  ]);
  const verdict = await adjudicateEnvelope(t, result);
  assert.equal(verdict.status, "complete", verdict.incomplete.join("\n"));
  assert.deepEqual(verdict.confirmedFindings.map((entry) => entry.id), [
    "specialist-1-correctness-codex:1",
    "specialist-1-correctness-codex:3",
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
  assert.match(verifierBrief, /return exactly one verdict keyed by\s+finding id for every/i);
  assert.match(verifierBrief, /Never build, test or run the reviewed\s+change, and never write an experiment of your own/);
  assert.doesNotMatch(verifierBrief, /confirming experiment/i);
  assert.match(verifierBrief, /Return the requested `verdicts` array:\s+exactly one structured verdict per assigned finding, each with its own\s+independent confidence integer from 0 to 100\./);
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

test("the recorded pull-request description reaches every judgement leg with the declared-scope rule", async () => {
  const marker = "MINOS_TEST_DECLARED_SCOPE_VERMILION_442";
  const args = enumeratedArgs("aaa111", "bbb222", {
    pullRequest: { number: 17, title: "Bridge a thread", body: `Not in scope: ${marker}.` },
  });
  assert.deepEqual(args.pullRequest, { title: "Bridge a thread", body: `Not in scope: ${marker}.` });
  const { calls } = await runScript(args, responder());
  const judgementCalls = calls.filter((call) =>
    call.opts.label === "exploration" || call.opts.label?.startsWith("specialist-") || call.opts.label?.startsWith("verify-"));
  assert.ok(judgementCalls.length >= 3);
  for (const call of judgementCalls) {
    assert.ok(call.prompt.includes(marker));
    assert.match(call.prompt, /declared gap is not a defect of this change/);
    assert.match(call.prompt, /never excuses incorrect behaviour/);
  }
});

test("a run without a recorded description carries no pull-request block", async () => {
  const { calls } = await runScript(enumeratedArgs(), responder());
  assert.ok(calls.every((call) => !call.prompt.includes("<pull-request-description")));
});

test("the review input fails closed on a recorded-but-unreadable pull-request record and omits an empty one", () => {
  assert.throws(
    () => enumeratedArgs("aaa111", "bbb222", { absentPullRequestRecord: true }),
    /missing or non-JSON pull-request record/,
  );
  assert.throws(
    () => enumeratedArgs("aaa111", "bbb222", { pullRequest: "not JSON" }),
    /missing or non-JSON pull-request record/,
  );
  assert.throws(
    () => enumeratedArgs("aaa111", "bbb222", { pullRequest: { title: 3, body: null } }),
    /needs string title and body fields/,
  );
  const empty = enumeratedArgs("aaa111", "bbb222", { pullRequest: { title: "", body: " \n" } });
  assert.equal(empty.pullRequest, undefined);
});

test("an oversized pull-request description is truncated rather than fatal", () => {
  const args = enumeratedArgs("aaa111", "bbb222", {
    pullRequest: { title: "Big", body: "x".repeat(70_000) },
  });
  assert.ok(args.pullRequest.body.length < 70_000);
  assert.match(args.pullRequest.body, /\[pull-request description truncated\]$/);
});

test("every configured guidance document is bound in order, naming its repository and origin", async () => {
  const args = enumeratedArgs("aaa111", "bbb222", {
    guidanceName: "AGENTS.md",
    guidanceContent: "MINOS_REVIEWED_GUIDANCE_OCHRE_719",
    secondaryGuidance: "MINOS_SECONDARY_COMMISSION_OCHRE_719",
  });
  assert.deepEqual(args.guidance.map(({ repository, path, origin }) => ({ repository, path, origin })), [
    { repository: "owner/plans", path: "README.md", origin: "configured" },
    { repository: null, path: "AGENTS.md", origin: "configured" },
  ]);
  const { calls } = await runScript(args, responder());
  const prompt = calls.find((call) => call.opts.label === "exploration").prompt;
  const secondaryAt = prompt.indexOf('<project-guidance origin="configured" repository="owner/plans" path="README.md">\nMINOS_SECONDARY_COMMISSION_OCHRE_719\n</project-guidance>');
  const reviewedAt = prompt.indexOf('<project-guidance origin="configured" path="AGENTS.md">\nMINOS_REVIEWED_GUIDANCE_OCHRE_719\n</project-guidance>');
  assert.ok(secondaryAt !== -1 && reviewedAt !== -1 && secondaryAt < reviewedAt, "both documents are bound, in configured order");
  assert.doesNotMatch(prompt, /grounding=/, "guidance blocks omit grounding attributes");
});

test("the deterministic review input rejects empty guidance", () => {
  for (const guidanceContent of ["", " \n\t"])
    assert.throws(() => enumeratedArgs("aaa111", "bbb222", { guidanceContent }), /guidance .* is empty/);
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
    pinnedModel: "gpt-6-sol",
  }]);
  assert.equal(result.reviewers[0].status, "no-result");
});

test("the lifecycle writes every marker by role in its configured form", () => {
  assert.match(lifecycle, /`"\$MINOS_BIN" forge marker HEAD TARGET clean add`/);
  assert.match(lifecycle, /`"\$MINOS_BIN" forge marker HEAD TARGET attention add`/);
  assert.match(lifecycle, /`"\$MINOS_BIN" forge marker FRESH_HEAD FRESH_TARGET in-flight remove`/);
  assert.match(lifecycle, /`"\$MINOS_BIN" forge marker HEAD TARGET in-flight remove`/);
  assert.doesNotMatch(lifecycle, /forge reaction/);
});

test("the lifecycle prescribes the review-only workflow discipline", () => {
  assert.match(lifecycle, /Every workflow stage is launched by one script, never by a command you\s+compose: `"\$\{MINOS_SETUP_WORKSPACE%\/\*\}\/dispatch-stage" NAME WORKFLOW`/);
  assert.match(lifecycle, /the timing wrapper outermost[\s\S]*the completion-flag wrapper next[\s\S]*the result-publication wrapper innermost/);
  assert.match(lifecycle, /\*\*A Claude Code session\.\*\* Launch each stage through Bash with\s+`run_in_background` — do not append shell `&`/);
  assert.match(lifecycle, /\*\*A Codex session\.\*\*[\s\S]*run the launch command in the\s+foreground and wait for it to exit[\s\S]*keep waiting on that same process[\s\S]*Never launch the\s+stage a second time[\s\S]*Do not end\s+your turn while a stage is running/);
  assert.match(lifecycle, /The flag watcher is the primary wake on completion\.[\s\S]*dispatch-stage" await NAME/);
  assert.match(lifecycle, /The recurring `CronCreate` timer is hang detection only, never the\s+expected wake\.[\s\S]*cancel the timer with `CronDelete`/);
  assert.match(lifecycle, /Do not call `ScheduleWakeup` in this lifecycle\.[\s\S]*`CronCreate` timer above is the working fallback/);
  assert.match(lifecycle, /memory-pressure[\s\S]*Check for it only at the\s+named boundaries below[\s\S]*do not interrupt a workflow or leave a forge write half-finished/);
  assert.match(lifecycle, /append one line to `\$MINOS_FAILURE_LOG`[\s\S]*every failed\*\* non-clean exit[\s\S]*lead-complete/);
  assert.match(lifecycle, /Whenever you stop after a clean, converged pass[\s\S]*`printf 'clean\\n' > "\$MINOS_RUN_DIR\/lead-complete"`/);
  assert.match(lifecycle, /Minos authors no commits on the pull-request branch, so every head movement is the\s+author's[\s\S]*stop without publishing a review or\s+setting a status/);
  assert.match(lifecycle, /Classification is your judgement,\s+informed by the threshold rather than mechanically bound to it[\s\S]*Any gating\s+finding makes the verdict `request-changes`; none makes it `clean`/);
  assert.match(lifecycle, /`outOfScopeObservations`[\s\S]*unverified observations, not findings[\s\S]*never enter a\s+review, the filing destination, a classification digest, or a run\s+outcome/);
  assert.match(lifecycle, /compose-review-publication\.mjs[\s\S]*publication-plan\.json[\s\S]*Post the reviews in that order, one scripted review per entry/);
  assert.match(lifecycle, /compose-review-publication\.mjs" \\\s+"\$MINOS_RUN_DIR\/publication" "\$MINOS_ORIENTATION" "\$MINOS_REVIEW_THRESHOLD"/);
  assert.match(lifecycle, /A request-changes plan contains one review with all confirmed findings[\s\S]*A clean plan has no posts/);
  assert.match(lifecycle, /A finding judged non-gating is advisory[\s\S]*deliver it to the\s+repository's configured filing destination when the run is clean/);
  assert.match(lifecycle, /file-triage\.mjs" \\\s+"\$MINOS_RUN_DIR\/publication\/triage-entries\.json" \\\s+"\$MINOS_ORIENTATION" "\$MINOS_CREDENTIAL_FILE"/);
  assert.match(lifecycle, /`unfiled` means delivery\s+failed or the destination kind is unavailable[\s\S]*Filing failure never generates a pull-request comment,\s+never falls back to another surface, and changes no verdict/);
});

test("the lifecycle prescribes the verdict-classification seam", () => {
  assert.match(lifecycle, /verdict-classification\.mjs" \\\s+--digest VERDICT_FILE "\$MINOS_REVIEW_THRESHOLD" \\\s+> "\$MINOS_RUN_DIR\/verdict-digest\.json"/);
  assert.match(lifecycle, /`verdict-digest-brief\.json` for the brief verdict/);
  assert.match(lifecycle, /digest lists every confirmed finding with its severity, confidence\s+values, and threshold position, and a `thresholdIndication` — what the\s+configured threshold alone would say/);
  assert.match(lifecycle, /informed by the threshold rather than mechanically bound to it \(the\s+commission's balance rule\)/);
  assert.match(lifecycle, /only two classes may be judged out —\s+a gap the pull-request description explicitly declares as known or out\s+of scope, where the project guidance does not contradict the\s+declaration \(`declared-out-of-scope`\), and a verifier-confirmed request\s+for a new safeguard[\s\S]*?no defect\s+in the change's own logic \(`speculative-hardening`\)/);
  assert.match(lifecycle, /The one upward exception\s+is judging a below-threshold finding's severity an undergrade that\s+genuinely belongs at or above the threshold, named as such/);
  assert.match(lifecycle, /`\$MINOS_RUN_DIR\/verdict-decision\.json` \(`verdict-decision-brief\.json`\s+for the brief verdict\)[\s\S]*?"kind": "minos-verdict-decision-v1"[\s\S]*?"basis":[\s\S]*?"gating": false, "class": "declared-out-of-scope"[\s\S]*?"gating": true, "undergrade":/);
  assert.match(lifecycle, /Validate each decision before any forge write[\s\S]*?verdict-classification\.mjs" \\\s+--validate VERDICT_FILE DECISION_FILE "\$MINOS_REVIEW_THRESHOLD"/);
  assert.match(lifecycle, /fails validation — a missing basis, an unnamed\s+declassification, an unnamed undergrade, a finding with no\s+disposition — is corrected and re-validated rather than worked around;\s+nothing has touched the forge yet/);
  assert.match(lifecycle, /The run's overall verdict is\s+`request-changes` when either validated decision is; `clean` only when\s+every validated decision is clean/);
});

test("worker dispatch options strip capabilities and identify fan-out settlements", async (t) => {
  const respond = responder();
  const { result, calls } = await runScript(ARGS, respond);
  assert.equal(calls.length, 3);
  for (const { opts } of calls) {
    assert.deepEqual(opts.strip, ["skills", "agents"], `${opts.label} strips worker capabilities`);
    if (!["Explore"].includes(opts.phase))
      assert.equal(opts.identity, true, `${opts.label} identifies its settlement`);
  }
  const verdict = await adjudicateEnvelope(t, result);
  assert.equal(verdict.complete, true, verdict.incomplete.join("\n"));
  assert.equal(verdict.confirmedFindings.length, 1);
});

test("identified specialist failure reaches the verdict by label", async (t) => {
  const normal = responder();
  let failedLabel;
  const respond = (label, prompt, opts) => {
    if (opts.phase === "Specialise") {
      failedLabel = label;
      return { identityEnvelope: { label, phase: opts.phase, output: null, failure: { message: "worker transport lost" } } };
    }
    return normal(label, prompt, opts);
  };
  const { result } = await runScript(ARGS, respond);
  const reason = `agent ${failedLabel} failed: worker transport lost`;
  assert.ok(result.incomplete?.includes(reason), JSON.stringify(result.incomplete));
  assert.equal(result.reviewers.find((entry) => entry.label === failedLabel).status, "no-result");
  // Complete archive records isolate the producer's incompleteness channel.
  const verdict = await adjudicateEnvelope(t, result);
  assert.equal(verdict.complete, false);
  assert.ok(verdict.incomplete.includes(`workflow reported incomplete: ${reason}`));
  assert.deepEqual(verdict.confirmedFindings, []);
});

test("identified verifier failure reaches the verdict by label", async (t) => {
  const normal = responder();
  let failedLabel;
  const respond = (label, prompt, opts) => {
    if (label.startsWith("verify-")) {
      failedLabel = label;
      return { identityEnvelope: { label, phase: opts.phase, output: null, failure: { message: "worker transport lost" } } };
    }
    return normal(label, prompt, opts);
  };
  const { result } = await runScript(ARGS, respond);
  const reason = `agent ${failedLabel} failed: worker transport lost`;
  assert.ok(result.incomplete?.includes(reason), JSON.stringify(result.incomplete));

  // Complete archive records isolate the producer's incompleteness channel.
  const verdict = await adjudicateEnvelope(t, result);
  assert.equal(verdict.complete, false);
  assert.ok(verdict.incomplete.includes(`workflow reported incomplete: ${reason}`));
  assert.deepEqual(verdict.confirmedFindings, []);
});

test("identified null answer keeps reviewer settlement truthful", async () => {
  const normal = responder();
  let answeredLabel;
  const respond = (label, prompt, opts) => {
    if (opts.phase === "Specialise") {
      answeredLabel = label;
      return { identityEnvelope: { label, phase: opts.phase, output: null, failure: null } };
    }
    return normal(label, prompt, opts);
  };
  const { result } = await runScript(ARGS, respond);
  assert.equal(result.reviewers.find((entry) => entry.label === answeredLabel).status, "done");
  assert.ok(result.incomplete.includes(`agent ${answeredLabel} answered without usable output`));
  assert.ok(!result.incomplete.some((reason) => reason.includes("failed:")));
});
