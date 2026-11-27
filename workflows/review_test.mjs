import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

import { invokeWorkflow, loadWorkflow } from "./workflow-invocation-fixture.mjs";
import { adjudicateEnvelope } from "./archive-fixture.mjs";
import { findingsFromVerifierPrompt, verdictFor } from "./verifier-prompt-fixture.mjs";

const inputScriptPath = fileURLToPath(new URL("./review-inputs.mjs", import.meta.url));
const lifecycle = await readFile(fileURLToPath(new URL("../lifecycle/lifecycle.md", import.meta.url)), "utf8");
const script = loadWorkflow("review.js");

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
    concernRouting,
    fileSafeguardRequests = "",
    depth = "",
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
        MINOS_CONCERN_ROUTING: concernRouting === undefined ? "" : JSON.stringify(concernRouting),
        MINOS_FILE_SAFEGUARD_REQUESTS: fileSafeguardRequests,
        MINOS_REVIEW_DEPTH: depth,
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

function unit(id = "logic", specialistType = "logic", scope = ["internal/x.go"]) {
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
    dependsOn: "",
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
  outsideConcernFindings = [],
) {
  return { applicability, findings, outsideConcernFindings, outOfScopeObservations };
}

function verifierResult(prompt, verdictOverrides = () => ({})) {
  return {
    verdicts: findingsFromVerifierPrompt(prompt).map((entry) => verdictFor(entry.id, verdictOverrides(entry))),
    newFindings: [],
    outOfScopeObservations: [],
  };
}

// Groups of one, so that no finding is merged unless a test says so.
function separateGroups(prompt) {
  return {
    groups: findingsFromVerifierPrompt(prompt).map((entry) => ({ findingIds: [entry.id], primary: entry.id, sharedCause: "" })),
  };
}

function responder({ exploration = explorationFixture(), specialist, merge, verify } = {}) {
  return (label, prompt, opts) => {
    if (label === "exploration") return typeof exploration === "function" ? exploration(label, prompt, opts) : exploration;
    if (label === "merge" || label.startsWith("merge-round-")) return merge ? merge(label, prompt, opts) : separateGroups(prompt);
    if (label.startsWith("verify-"))
      return verify ? verify(label, prompt, opts) : verifierResult(prompt);
    if (specialist) return specialist(label, prompt, opts);
    return specialistResult();
  };
}

function specialistCalls(calls) {
  return calls.filter((call) => call.opts.label?.startsWith("specialist-"));
}

test("the script emits an envelope with every routed leg and raw verifier output", async () => {
  const plan = [unit("correct", "logic"), unit("secure", "security")];
  const { result, calls } = await runScript(ARGS, responder({ exploration: explorationFixture({ plan }) }));

  assert.deepEqual(Object.keys(result).sort(), [
    "briefs", "dispatches", "exploration", "misconfigurations", "outOfScopeObservations", "proposedFindings",
    "requiredModelEvidence", "reviewed", "reviewers", "stage", "uncheckedFindings",
  ]);
  assert.equal(result.stage, "present");
  assert.equal(result.proposedFindings.length, 2);
  const { findingId, ...rawVerifier } = verdictFor(result.proposedFindings[0].id);
  assert.deepEqual(result.proposedFindings[0].rawVerifier, rawVerifier);
  assert.deepEqual(result.uncheckedFindings, []);
  assert.ok(result.proposedFindings.every((entry) => entry.proposingLabel && entry.verifyLabel));
  assert.deepEqual(
    result.requiredModelEvidence.map(({ role, pinnedModel }) => ({ role, pinnedModel })),
    [
      { role: "exploration", pinnedModel: "gpt-6.1-sol" },
      { role: "specialist", pinnedModel: "gpt-6.2-astra" },
      { role: "specialist", pinnedModel: "gpt-6.2-astra" },
      { role: "merge", pinnedModel: "claude-opus-5-5" },
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
    ["logic"],
  ]) {
    const exploration = explorationFixture({
      files: [{ path: "internal/group_execution.rs", added: 4, deleted: 1 }],
      plan: [unit("group-execution", "logic", malformedScope)],
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
        pinnedModel: "gpt-6.1-sol",
      }],
      proposedFindings: [],
      uncheckedFindings: [],
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
  const plan = [unit("group-execution", "logic", scope)];
  const exploration = explorationFixture({
    files: scope.map((path) => ({ path, added: 4, deleted: 1 })),
    plan,
  });
  const { result, calls } = await runScript(ARGS, responder({ exploration }));
  const specialist = specialistCalls(calls)[0];

  assert.deepEqual(result.exploration.plan, plan);
  assert.deepEqual(result.dispatches[0].scope, scope);
  assert.ok(specialist.prompt.includes("logic concern group-execution\n\n- `internal/group_execution.rs`\n- `internal/group_execution_test.rs`"));
});

test("an out-of-scope observation leaves the specialist without entering finding verification", async () => {
  const { result, calls } = await runScript(ARGS, responder({
    specialist: () => specialistResult([], undefined, [observation()]),
  }));

  assert.deepEqual(result.proposedFindings, []);
  assert.equal(calls.filter((call) => call.opts.label?.startsWith("verify-")).length, 0);
  assert.deepEqual(result.outOfScopeObservations, [{
    id: "specialist-1-logic-codex:observation:1",
    source: "logic concern logic",
    title: "pre-existing defect",
    path: "internal/legacy.go",
    line: 9,
    explanation: "Unverified observation: the unchanged branch admits an invalid state.",
    observingLabel: "specialist-1-logic-codex",
    verified: false,
  }]);
  const schema = specialistCalls(calls)[0].opts.schema;
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
});

test("all findings are proposed on Astra and verified cross-family on Claude", async () => {
  const plan = [unit("correct", "logic"), unit("secure", "security")];
  const { calls } = await runScript(ARGS, responder({ exploration: explorationFixture({ plan }) }));
  const correctness = calls.find((call) => call.opts.label === "specialist-1-logic-codex");
  const security = calls.find((call) => call.opts.label === "specialist-2-security-codex");
  const verifiers = calls.filter((call) => call.opts.label?.startsWith("verify-"));
  assert.deepEqual([correctness.opts.engine, correctness.opts.model], ["codex", "gpt-6.2-astra"]);
  assert.deepEqual([security.opts.engine, security.opts.model], ["codex", "gpt-6.2-astra"]);
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
  assert.deepEqual(
    schema.properties.outOfScopeObservations.items.required,
    ["title", "path", "line", "explanation"],
  );
});

test("a verifier observation survives a verdict set discarded as malformed", async () => {
  const { result } = await runScript(ARGS, responder({
    verify: () => ({
      verdicts: [verdictFor("not-a-proposed-finding")],
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

test("a routed concern's specialists propose on its own routing and the other concerns on the proposer's", async () => {
  const args = enumeratedArgs("aaa111", "bbb222", {
    concernRouting: { security: { engine: "claude", effort: "high" }, tests: { effort: "low" } },
  });
  assert.deepEqual(args.concernRouting.security, { engine: "claude", model: "claude-opus-5-5", effort: "high" });
  assert.deepEqual(args.concernRouting.tests, { engine: "codex", model: "gpt-6.2-astra", effort: "low" });
  assert.deepEqual(args.concernRouting.logic, args.routing.proposer);
  const plan = [unit("correct", "logic"), unit("secure", "security"), unit("tested", "tests")];
  const { result, calls } = await runScript(args, responder({ exploration: explorationFixture({ plan }) }));
  assert.deepEqual(
    specialistCalls(calls).map((call) => [call.opts.label, call.opts.engine, call.opts.model, call.opts.effort]),
    [
      ["specialist-1-logic-codex", "codex", "gpt-6.2-astra", "medium"],
      ["specialist-2-security-claude", "claude", "claude-opus-5-5", "high"],
      ["specialist-3-tests-codex", "codex", "gpt-6.2-astra", "low"],
    ],
  );
  assert.deepEqual(
    result.requiredModelEvidence.filter((leg) => leg.role === "specialist").map((leg) => leg.pinnedModel),
    ["gpt-6.2-astra", "claude-opus-5-5", "gpt-6.2-astra"],
  );
  assert.deepEqual(result.dispatches.map((dispatch) => dispatch.family), ["codex", "claude", "codex"]);
});

test("a finding from a concern routed to the verifier's engine is verified by the other family", async () => {
  const args = enumeratedArgs("aaa111", "bbb222", { concernRouting: { security: { engine: "claude" } } });
  const plan = [unit("correct", "logic"), unit("secure", "security")];
  const { result, calls } = await runScript(args, responder({ exploration: explorationFixture({ plan }) }));
  const verified = Object.fromEntries(calls
    .filter((call) => call.opts.label?.startsWith("verify-"))
    .map((call) => [call.opts.label, [call.opts.engine, call.opts.model, findingsFromVerifierPrompt(call.prompt).map((entry) => entry.id)]]));
  assert.deepEqual(verified, {
    "verify-1-claude": ["claude", "claude-opus-5-5", ["specialist-1-logic-codex:1"]],
    "verify-1-codex": ["codex", "gpt-6.2-astra", ["specialist-2-security-claude:1"]],
  });
});

test("both routes' verdicts reach the result when findings from two engines are verified apart", async (t) => {
  const args = enumeratedArgs("aaa111", "bbb222", { concernRouting: { security: { engine: "claude" } } });
  const plan = [unit("correct", "logic"), unit("secure", "security")];
  const { result } = await runScript(args, responder({
    exploration: explorationFixture({ plan }),
    verify: (label, prompt) => verifierResult(prompt, () => ({ reason: `checked by ${label}` })),
  }));
  assert.deepEqual(
    result.proposedFindings.map((entry) => [entry.id, entry.verifyLabel, entry.rawVerifier?.reason]).sort(),
    [
      ["specialist-1-logic-codex:1", "verify-1-claude", "checked by verify-1-claude"],
      ["specialist-2-security-claude:1", "verify-1-codex", "checked by verify-1-codex"],
    ],
  );
  const verdict = await adjudicateEnvelope(t, result);
  assert.equal(verdict.status, "complete", verdict.incomplete.join("\n"));
  assert.equal(verdict.confirmedFindings.length, 2);
});

test("a merged group is verified by the family that did not propose its primary, wherever the primary is listed", async () => {
  // Both pairings: a Codex primary listed second, and a Claude primary
  // listed first, which only a verifier chosen from the primary's own
  // proposer, and not from the default proposer's engine, sends to Codex.
  for (const [primary, verifiedOn] of [[1, ["verify-1-claude", "claude"]], [0, ["verify-1-codex", "codex"]]]) {
    const args = enumeratedArgs("aaa111", "bbb222", { concernRouting: { security: { engine: "claude" } } });
    const plan = [unit("secure", "security"), unit("correct", "logic")];
    const { calls } = await runScript(args, responder({
      exploration: explorationFixture({ plan }),
      merge: (_label, prompt) => {
        const ids = findingsFromVerifierPrompt(prompt).map((entry) => entry.id);
        assert.deepEqual(ids, ["specialist-1-security-claude:1", "specialist-2-logic-codex:1"]);
        return { groups: [{ findingIds: ids, primary: ids[primary], sharedCause: "One guard covers both." }] };
      },
    }));
    const verified = calls
      .filter((call) => call.opts.label?.startsWith("verify-"))
      .map((call) => [call.opts.label, call.opts.engine, findingsFromVerifierPrompt(call.prompt).map((entry) => entry.id)]);
    const primaryId = ["specialist-1-security-claude:1", "specialist-2-logic-codex:1"][primary];
    assert.deepEqual(verified, [[...verifiedOn, [primaryId]]]);
  }
});

test("the input builder hands the specialists and verifiers the safeguard-request passages the repository's setting chooses", async () => {
  const passage = (value) => readFileSync(new URL(`./review-briefs/safeguard-requests/${value}.md`, import.meta.url), "utf8").trim();
  const verifierText = (on) => readFileSync(new URL(`./review-briefs/verifier-safeguard-requests/${on}.md`, import.meta.url), "utf8").trim();
  for (const [setting, chosen, other] of [["", "false", "true"], ["false", "false", "true"], ["true", "true", "false"]]) {
    const args = enumeratedArgs("aaa111", "bbb222", { fileSafeguardRequests: setting });
    for (const [concern, text] of Object.entries(args.prompts.specialist)) {
      assert.ok(text.includes(passage(chosen)), `${concern} under ${JSON.stringify(setting)} carries ${chosen}.md`);
      assert.ok(!text.includes(passage(other)), `${concern} under ${JSON.stringify(setting)} leaves out ${other}.md`);
    }
    const { calls } = await runScript(args, responder({ exploration: explorationFixture({ plan: [unit("correct", "logic")] }) }));
    const specialist = calls.find((call) => call.opts.label?.startsWith("specialist-"));
    assert.ok(specialist.prompt.includes(passage(chosen)), `the dispatched specialist reads ${chosen}.md`);
    const verifierPassage = verifierText(true);
    const verifier = calls.find((call) => call.opts.label?.startsWith("verify-"));
    assert.equal(verifier.prompt.includes(verifierPassage), chosen === "true",
      `the dispatched verifier under ${JSON.stringify(setting)} carries the request section only when filing is on`);
  }
  assert.throws(() => enumeratedArgs("aaa111", "bbb222", { fileSafeguardRequests: "yes" }),
    /MINOS_FILE_SAFEGUARD_REQUESTS must be one of false, true/);
});

test("the input builder hands the exploration leg and the specialists the depth passages the repository's setting chooses", async () => {
  const passage = (slot, value) => readFileSync(new URL(`./review-briefs/${slot}/${value}.md`, import.meta.url), "utf8").trim();
  for (const [setting, depth] of [["", "standard"], ["light", "light"], ["thorough", "thorough"]]) {
    const args = enumeratedArgs("aaa111", "bbb222", { depth: setting });
    const { calls } = await runScript(args, responder({ exploration: explorationFixture({ plan: [unit("correct", "logic")] }) }));
    const exploration = calls.find((call) => call.opts.label?.startsWith("explor"));
    const specialist = calls.find((call) => call.opts.label?.startsWith("specialist-"));
    assert.ok(exploration.prompt.includes(passage("exploration-depth", depth)), `the dispatched exploration leg under ${JSON.stringify(setting)} reads ${depth}.md`);
    assert.ok(specialist.prompt.includes(passage("specialist-depth", depth)), `the dispatched specialist under ${JSON.stringify(setting)} reads ${depth}.md`);
    for (const other of ["light", "thorough"].filter((value) => value !== depth)) {
      assert.ok(!exploration.prompt.includes(passage("exploration-depth", other)), `the exploration leg under ${depth} leaves out ${other}.md`);
      assert.ok(!specialist.prompt.includes(passage("specialist-depth", other)), `the specialist under ${depth} leaves out ${other}.md`);
    }
  }
  assert.throws(() => enumeratedArgs("aaa111", "bbb222", { depth: "deep" }),
    /MINOS_REVIEW_DEPTH must be one of light, standard, thorough/);
});

test("the input builder refuses concern routing it cannot honour, and the workflow refuses input without it", async () => {
  assert.throws(
    () => enumeratedArgs("aaa111", "bbb222", { concernRouting: { style: { effort: "low" } } }),
    /routing\.concerns\.style is not a specialist concern/,
  );
  assert.throws(
    () => enumeratedArgs("aaa111", "bbb222", { provisioned: "codex", concernRouting: { security: { engine: "claude" } } }),
    /routing\.concerns\.security\.engine names claude, which this deployment has not provisioned/,
  );
  assert.throws(
    () => enumeratedArgs("aaa111", "bbb222", { concernRouting: { security: { model: "claude-opus-5-5" } } }),
    /routing\.concerns\.security\.model needs routing\.concerns\.security\.engine/,
  );
  const calls = [];
  const { concernRouting, ...withoutConcernRouting } = ARGS;
  await assert.rejects(
    runScript(withoutConcernRouting, (label) => { calls.push(label); return null; }),
    /omitted or malformed the concern routing/,
  );
  const { security, ...missingOne } = concernRouting;
  await assert.rejects(
    runScript({ ...ARGS, concernRouting: missingOne }, (label) => { calls.push(label); return null; }),
    /omitted or malformed the concern routing/,
  );
  assert.deepEqual(calls, []);
});

test("a single-engine deployment verifies a verifier's own findings on the one engine it has", async () => {
  const args = enumeratedArgs("aaa111", "bbb222", { provisioned: "claude" });
  let raised = false;
  const { calls } = await runScript(args, responder({
    verify: (label, prompt) => ({
      ...verifierResult(prompt),
      newFindings: raised ? [] : [finding({ title: "raised once", line: 70 }), (raised = true)].slice(0, 1),
    }),
  }));
  assert.deepEqual(calls.filter((call) => call.opts.label?.startsWith("verify-")).map((call) => call.opts.label),
    ["verify-1-claude", "verify-round-2-1-claude"]);
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

test("exploration runs at high effort while specialists and verifiers run at medium", async () => {
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

test("specialist inapplicability is a skip-like disposition whose own findings never reach a verifier", async (t) => {
  const { result, calls } = await runScript(ARGS, responder({
    specialist: () => specialistResult(
      [finding({ title: "must not escape" })],
      { status: "inapplicable", reason: "the assigned scope contains no relevant behaviour" },
    ),
  }));
  assert.equal(result.proposedFindings.length, 0);
  assert.equal(calls.filter((call) => call.opts.label?.startsWith("verify-")).length, 0);
  assert.deepEqual(result.briefs, [{
    brief: "workflows/review-briefs/concerns/logic.md",
    title: "logic concern logic",
    status: "skipped",
    skipKind: "inapplicable",
    reason: "the assigned scope contains no relevant behaviour",
  }]);
  const verdict = await adjudicateEnvelope(t, result);
  assert.equal(verdict.status, "complete");
  assert.deepEqual(verdict.summarySections, [
    { kind: "concern", name: "logic", skipped: "the assigned scope contains no relevant behaviour" },
  ]);
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
  assert.match(verifier.prompt, /"title": "four survives"/);
});

test("verifier dispatch pools findings globally and caps batches at six", async () => {
  const counts = [7, 1, 2, 3, 4, 5, 6];
  const types = ["logic", "security", "tests", "fit", "logic", "security", "tests"];
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
  const plan = [unit("one", "logic"), unit("two", "security"), unit("three", "tests")];
  const { result, calls } = await runScript(ARGS, responder({
    exploration: explorationFixture({ plan }),
    specialist: (label) => specialistResult([finding({ title: `finding from ${label}` })]),
  }));

  const verifierCalls = calls.filter((call) => call.opts.label?.startsWith("verify-"));
  assert.equal(verifierCalls.length, 1);
  assert.equal(verifierCalls[0].opts.label, "verify-1-claude");
  assert.equal(findingsFromVerifierPrompt(verifierCalls[0].prompt).length, 3);
  assert.deepEqual(new Set(result.proposedFindings.map((entry) => entry.proposingLabel)), new Set([
    "specialist-1-logic-codex", "specialist-2-security-codex", "specialist-3-tests-codex",
  ]));
  assert.ok(result.proposedFindings.every((entry) => entry.verifyLabel === "verify-1-claude"));
});

for (const responseCase of [
  {
    name: "missing ids",
    response(findings) {
      return { verdicts: [findings[0], findings[2]].map((entry) => ({
        ...verdictFor(entry.id, { reason: "checked" }),
      })) };
    },
    present: [true, false, true],
  },
  {
    name: "duplicate ids",
    response(findings) {
      return { verdicts: [findings[0], findings[0], findings[1], findings[2]].map((entry) => ({
        ...verdictFor(entry.id, { reason: "checked" }),
      })) };
    },
    present: [false, true, true],
  },
  {
    name: "unknown ids",
    response(findings) {
      return { verdicts: [...findings, { id: "unknown-finding" }].map((entry) => ({
        ...verdictFor(entry.id, { reason: "checked" }),
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
      finding({ title: "confirmed one" }),
      finding({ title: "refuted", line: 4 }),
      finding({ title: "confirmed two", line: 5 }),
    ]),
    verify: (_label, prompt) => verifierResult(prompt, (entry) => entry.title === "refuted"
      ? { verdict: "refuted", confidence: 84, severity: "None", reason: "caller rejects the state" }
      : { confidence: 93, reason: "path remains reachable" }),
  }));

  assert.deepEqual(result.proposedFindings.map(({ id, rawVerifier }) => ({ id, verdict: rawVerifier.verdict, reason: rawVerifier.reason })), [
    { id: "specialist-1-logic-codex:1", verdict: "confirmed", reason: "path remains reachable" },
    { id: "specialist-1-logic-codex:2", verdict: "refuted", reason: "caller rejects the state" },
    { id: "specialist-1-logic-codex:3", verdict: "confirmed", reason: "path remains reachable" },
  ]);
  const verdict = await adjudicateEnvelope(t, result);
  assert.equal(verdict.status, "complete", verdict.incomplete.join("\n"));
  assert.deepEqual(verdict.confirmedFindings.map((entry) => entry.id), [
    "specialist-1-logic-codex:1",
    "specialist-1-logic-codex:3",
  ]);
  assert.deepEqual(
    verdict.modelEvidence.find((entry) => entry.role === "verifier").findingIds,
    result.proposedFindings.map((entry) => entry.id),
  );
});

test("the deterministic input binds shipped role prose and project guidance into every judgement leg", async () => {
  const marker = "MINOS_ESTATE_COMMISSION_CINNABAR_719";
  const args = enumeratedArgs("aaa111", "bbb222", { guidanceContent: marker });
  const { calls } = await runScript(args, responder());
  const judgementCalls = calls.filter((call) =>
    call.opts.label === "exploration" || call.opts.label?.startsWith("specialist-") || call.opts.label?.startsWith("verify-"));
  assert.ok(judgementCalls.every((call) => call.prompt.includes(marker)));
  const specialist = judgementCalls.find((call) => call.opts.label?.startsWith("specialist-")).prompt;
  assert.match(specialist, /^# Specialist review\n/);
  assert.match(specialist, /## Your concern: logic\n\nWhether the change does the right thing when everything goes to plan\./);
  assert.match(specialist, /- `security`: /);
  assert.doesNotMatch(specialist, /- `logic`: /, "a specialist is not listed among the others");
  assert.match(judgementCalls.find((call) => call.opts.label?.startsWith("verify-")).prompt, /^# Finding verifier\n/);
  assert.match(judgementCalls.find((call) => call.opts.label === "exploration").prompt, /^# Review exploration\n[\s\S]*- `fit`: [\s\S]*- `tests`: /);
  assert.ok(calls.every((call) => !/\{\{[a-z-]+\}\}/.test(call.prompt)), "every slot is filled");
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
    assert.ok(call.prompt.includes(`<pull-request-description title="Bridge a thread">\nNot in scope: ${marker}.\n</pull-request-description>`));
    assert.match(call.prompt, /A gap the author declares out of scope is not a problem/);
    assert.match(call.prompt, /never excuses wrong behaviour/);
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
    pinnedModel: "gpt-6.1-sol",
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
  assert.match(lifecycle, /`outOfScopeObservations`[\s\S]*unverified observations, not findings[\s\S]*`unsettledFindings`[\s\S]*Neither kind is a confirmed finding\. They never enter a\s+review, a classification digest, or a run outcome; the composer in step\s+6 sets them aside for the filing destination, marked as unverified/);
  assert.match(lifecycle, /compose-review-publication\.mjs[\s\S]*publication-plan\.json[\s\S]*Post the reviews in that order, one scripted review per entry/);
  assert.match(lifecycle, /compose-review-publication\.mjs" \\\s+"\$MINOS_RUN_DIR\/publication" "\$MINOS_ORIENTATION" "\$MINOS_REVIEW_THRESHOLD"/);
  assert.match(lifecycle, /A request-changes plan contains one review with the blocking findings\s+from both review stages and nothing else[\s\S]*A clean plan has no posts/);
  assert.match(lifecycle, /A finding that neither gates nor takes a class is advisory\. It goes to\s+the repository's configured filing destination on either outcome and\s+is never part of the review, which carries only what blocks the pull\s+request\. Where that destination is a comment on the pull request, the\s+comment is filing/);
  assert.match(lifecycle, /A finding that fits a class takes it at any severity[\s\S]*?A classed finding is never\s+part of the review step 6 posts, and with one exception it is filed\s+nowhere either[\s\S]*?`\$MINOS_FILE_SAFEGUARD_REQUESTS` is `true`: there the composer in step\s+6 sets each `speculative-hardening` finding aside for the filing\s+destination as a safeguard request[\s\S]*?A classed finding stays in the run record/);
  assert.match(lifecycle, /file-triage\.mjs" \\\s+"\$MINOS_RUN_DIR\/publication\/triage-entries\.json" \\\s+"\$MINOS_ORIENTATION" \\\s+>/);
  assert.match(lifecycle, /`unfiled` means delivery\s+failed or the destination kind is unavailable[\s\S]*Filing failure never generates a pull-request comment,\s+never falls back to another surface, and changes no verdict/);
});

test("the lifecycle sizes the review to what the author changed since the head Minos last finished on", () => {
  assert.match(lifecycle, /then decide how much review this head needs, before any workflow runs/);
  assert.match(lifecycle, /"\$MINOS_BIN" forge earlier "\$MINOS_HEAD_SHA" "\$MINOS_TARGET_SHA"[\s\S]*`changed_paths`[\s\S]*The comparison is of file content, not of\s+commits/);
  assert.match(lifecycle, /`\$MINOS_PUSH_INSPECTION_OUTCOMES`[\s\S]*An outcome the list\s+does not name is closed to this run[\s\S]*Choose one that the list names:\s+- \*\*No review\.\*\*/);
  assert.match(lifecycle, /\*\*No review\.\*\*[\s\S]*an empty `changed_paths`/);
  assert.match(lifecycle, /\*\*No review\.\*\*[\s\S]*unavailable\s+when a `\.review\/` brief engages the range[\s\S]*review-scope-inputs\.mjs" BASE "\$MINOS_HEAD_SHA"`[\s\S]*`briefsEngage`/);
  assert.match(lifecycle, /\*\*Increment\.\*\*[\s\S]*earlier head is an ancestor[\s\S]*`EARLIER\.\.\.HEAD` alone/);
  assert.match(lifecycle, /\*\*Full\.\*\* Everything else/);
  assert.match(lifecycle, /Every `minos forge` command keeps the\s+run's real head and target, whatever was reviewed/);
  assert.match(lifecycle, /review-inputs\.mjs" \\\s+REVIEW_BASE "\$MINOS_HEAD_SHA"/);
  assert.match(lifecycle, /review-brief-inputs\.mjs" \\\s+REVIEW_BASE "\$MINOS_HEAD_SHA"/);
  assert.doesNotMatch(lifecycle, /review-scope\.js/);
  assert.match(lifecycle, /a blocking\s+finding from an earlier Minos review still counts until it is settled/);
});

test("the lifecycle keeps an answered finding from being raised again", () => {
  assert.match(lifecycle, /`"\$MINOS_BIN" forge threads` prints every inline\s+thread Minos opened on this pull request/);
  assert.match(lifecycle, /Ask the forge on every run,\s+whatever step 2's `earlier` printed/);
  assert.match(lifecycle, /"\$MINOS_BIN" forge snapshot` prints every review with its `body`:\s+Minos's are the ones whose `user` is the snapshot's\s+`authenticated_user`/);
  assert.match(lifecycle, /\(`already-raised`\)[\s\S]*restates an earlier finding, in a thread or in a review\s+body alike, and the code it cites has not changed[\s\S]*a finding at or above\s+the threshold never takes it on that ground/);
  assert.match(lifecycle, /\(`speculative-hardening`\)[\s\S]*The setting decides where such a finding goes and nothing\s+about its class/);
  assert.match(lifecycle, /One is settled when the pull request's author answered it, or when you\s+read the code it cites at this head and the defect it describes is no\s+longer there/);
  assert.match(lifecycle, /A finding in a review\s+body has no thread to resolve or reply in, so the author answers it in\s+a comment on the pull request that rejects it[\s\S]*?`"\$MINOS_BIN" forge comments` prints the pull request's comments/);
  assert.match(lifecycle, /"gating": false, "class": "already-raised"/);
});

test("the lifecycle prescribes the verdict-classification seam", () => {
  assert.match(lifecycle, /verdict-classification\.mjs" \\\s+--digest VERDICT_FILE "\$MINOS_REVIEW_THRESHOLD" \\\s+> "\$MINOS_RUN_DIR\/verdict-digest\.json"/);
  assert.match(lifecycle, /`verdict-digest-brief\.json` for the brief verdict/);
  assert.match(lifecycle, /digest lists every confirmed finding with its severity, confidence\s+values, and threshold position, and a `thresholdIndication` — what the\s+configured threshold alone would say/);
  assert.match(lifecycle, /informed by the threshold rather than mechanically bound to it: the\s+threshold gives each finding its default, and you depart from a\s+default only on a basis this step names\./);
  assert.match(lifecycle, /only three classes may be judged out\.\s+Two object to the kind of finding:\s+a gap the pull-request description explicitly declares as known or out\s+of scope, where the project guidance does not contradict the\s+declaration \(`declared-out-of-scope`\), and a request for a new\s+safeguard[\s\S]*?where the change's own logic does\s+nothing wrong \(`speculative-hardening`\)/);
  assert.match(lifecycle, /The one upward exception\s+is judging a below-threshold finding's severity an undergrade that\s+genuinely belongs at or above the threshold, named as such/);
  assert.match(lifecycle, /`\$MINOS_RUN_DIR\/verdict-decision\.json` \(`verdict-decision-brief\.json`\s+for the brief verdict\)[\s\S]*?"kind": "minos-verdict-decision-v1"[\s\S]*?"basis":[\s\S]*?"gating": false, "class": "declared-out-of-scope"[\s\S]*?"gating": true, "undergrade":/);
  assert.match(lifecycle, /Validate each decision before any forge write[\s\S]*?verdict-classification\.mjs" \\\s+--validate VERDICT_FILE DECISION_FILE "\$MINOS_REVIEW_THRESHOLD"/);
  assert.match(lifecycle, /fails validation — a missing basis, an unnamed\s+declassification, an unnamed undergrade, a finding with no\s+disposition, a `class` on a gating finding, a blank `defect` label,\s+one whose findings, those already raised aside, gate or are classed\s+differently, or findings of one defect at one site that are already\s+raised in one disposition and new in another — is corrected and\s+re-validated rather than worked around; nothing has touched the forge\s+yet/);
  assert.match(lifecycle, /A gating disposition carries no `class` at all/);
  assert.match(lifecycle, /a brief finding at the same line and under the same title\s+as a main-review finding even where you gave them no label, because\s+the composer merges the two as one finding/);
  assert.match(lifecycle, /Those findings aside,\s+it refuses, with nothing written, a label whose findings gate\s+differently across the two decisions, and a non-gating defect the two\s+decisions class differently/);
  assert.match(lifecycle, /Give every one of those findings\s+the one `defect` label\. Class the finding at the earlier site\s+`already-raised`\.[\s\S]*?The findings at the new sites take their own call/);
  assert.match(lifecycle, /findings of one defect at one site take one answer on it\. Two findings\s+at one line under one title, and a label's findings at one line\s+whatever their titles, all take `already-raised` or none of them\s+does/);
  assert.match(lifecycle, /A site's one answer on\s+`already-raised` holds across the two decisions as well, for those\s+twins and for a label's findings at one line where the decisions each\s+hold one/);
  assert.match(lifecycle, /The run's overall verdict is\s+`request-changes` when either validated decision is; `clean` only when\s+every validated decision is clean/);
  assert.match(lifecycle, /take the same `defect`\s+label on their dispositions[\s\S]*?they gate\s+alike — so a below-threshold restatement of a gating defect gates too,\s+and carries an `undergrade`[\s\S]*?publishes a labelled defect once, at its highest-severity\s+site[\s\S]*?Distinct\s+defects at one\s+line take no label/);
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

test("a specialist is shown the other planned units of its own concern, and an only unit none", async () => {
  const plan = [
    unit("logic-api", "logic", ["internal/api.go"]),
    unit("logic-store", "logic", ["internal/store.go"]),
    unit("failure", "failure", ["internal/api.go"]),
  ];
  const { calls } = await runScript(ARGS, responder({
    exploration: explorationFixture({
      files: [{ path: "internal/api.go", added: 400, deleted: 0 }, { path: "internal/store.go", added: 400, deleted: 0 }],
      plan,
    }),
  }));
  const promptOf = (label) => calls.find((call) => call.opts.label === label).prompt;
  const focus = (entry) => `${entry.concern}\n\n- \`${entry.scope[0]}\``;
  const first = promptOf("specialist-1-logic-codex");
  const second = promptOf("specialist-2-logic-codex");
  const only = promptOf("specialist-3-failure-codex");
  assert.equal(first.split(focus(plan[1])).length - 1, 1, "the first logic unit sees the second's share");
  assert.equal(first.split(focus(plan[0])).length - 1, 1, "and its own only as its planned focus");
  assert.equal(second.split(focus(plan[0])).length - 1, 1, "the second logic unit sees the first's share");
  assert.ok(!only.includes(plan[0].concern) && !only.includes(plan[1].concern), "an only unit is shown no other unit");
});

test("the lifecycle finds its workflow's status snapshot by the names the workflows declare", async () => {
  assert.match(lifecycle, /`\$MINOS_RUN_DIR\/ensemble\.<run-id>\.local\.json`/);
  assert.doesNotMatch(lifecycle, /`\$MINOS_RUN_DIR\/ensemble\.local\.json`/);
  for (const [file, stage] of [["./review.js", "the main review"], ["./review-briefs.js", "the brief stage"]]) {
    const source = await readFile(fileURLToPath(new URL(file, import.meta.url)), "utf8");
    const name = source.match(/^\s*name: "([^"]+)"/m)?.[1];
    assert.ok(name, `${file} declares a workflow name`);
    assert.match(lifecycle, new RegExp(`\`${name}\` for ${stage}`));
  }
  assert.match(lifecycle, /the later\s+`startedAt` is the attempt you are waiting on/);
});

test("the lifecycle approves a clean outcome through the guarded command and settles comments by time and author", () => {
  assert.match(lifecycle, /A clean plan has no posts: write no review,/);
  assert.doesNotMatch(lifecycle, /publish no approval review/);
  assert.match(lifecycle, /run\s+`"\$MINOS_BIN" forge approve HEAD TARGET` first, on every clean\s+outcome, a run that reviewed nothing included/);
  assert.match(lifecycle, /An\s+approval the forge will not take, for any reason but a moved head,[\s\S]*?forge status HEAD TARGET incomplete`/);
  assert.match(lifecycle, /The\s+command already tries again an approval whose write came back\s+uncertain or retryable, so a last answer that is not `applied` is an\s+approval the forge will not take/);
  assert.match(lifecycle, /refuses as well findings of one defect at one site, twins under one\s+title or a label's findings at one line, that are already raised in\s+one decision and new in the other, even where the other gates them/);
  assert.match(lifecycle, /only when its `user` is the\s+snapshot's `author` and its `created_at` is later than the\s+`submitted_at` the snapshot gives for the Minos review whose body\s+holds the finding/);
});

test("findings one fix would resolve are merged and verified once, with the group's other write-ups beside the primary", async (t) => {
  const plan = [unit("logic", "logic"), unit("failure", "failure")];
  const { result, calls } = await runScript(ARGS, responder({
    exploration: explorationFixture({ plan }),
    specialist: (label) => specialistResult([finding({
      title: `${label} write-up`,
      severity: label.includes("logic") ? "High" : "Low",
      confidence: 70,
      dependsOn: label.includes("logic") ? "" : "The queue redelivers after thirty seconds.",
    })]),
    merge: (_label, prompt) => {
      const ids = findingsFromVerifierPrompt(prompt).map((entry) => entry.id);
      return { groups: [{ findingIds: ids, primary: ids[0], sharedCause: "Guard the transition once." }] };
    },
  }));

  const merge = calls.find((call) => call.opts.label === "merge");
  assert.deepEqual(
    findingsFromVerifierPrompt(merge.prompt).map(({ id, reportedBy, outsideConcern, severity, confidence }) =>
      ({ id, reportedBy, outsideConcern, severity, confidence })),
    [
      { id: "specialist-1-logic-codex:1", reportedBy: "logic", outsideConcern: false, severity: undefined, confidence: undefined },
      { id: "specialist-2-failure-codex:1", reportedBy: "failure", outsideConcern: false, severity: undefined, confidence: undefined },
    ],
  );
  const verifiers = calls.filter((call) => call.opts.label?.startsWith("verify-"));
  assert.equal(verifiers.length, 1);
  assert.deepEqual(findingsFromVerifierPrompt(verifiers[0].prompt), [{
    id: "specialist-1-logic-codex:1",
    title: "specialist-1-logic-codex write-up",
    path: "internal/x.go",
    line: 3,
    explanation: "the new branch admits an invalid state",
    dependsOn: "The queue redelivers after thirty seconds.",
    alsoReportedAs: [{ title: "specialist-2-failure-codex write-up", explanation: "the new branch admits an invalid state" }],
  }]);
  assert.equal(result.proposedFindings.length, 1);
  assert.deepEqual(result.proposedFindings[0].mergedFrom.map(({ id }) => id), ["specialist-2-failure-codex:1"]);
  const verdict = await adjudicateEnvelope(t, result);
  assert.equal(verdict.status, "complete", verdict.incomplete.join("\n"));
  assert.equal(verdict.confirmedFindings.length, 1);
  assert.deepEqual(verdict.summarySections, [
    { kind: "concern", name: "logic", findings: 1 },
    { kind: "concern", name: "failure", findings: 1 },
  ]);
});

test("a grouping that loses or repeats a finding stops the review before verification", async (t) => {
  const plan = [unit("logic", "logic"), unit("failure", "failure")];
  for (const groups of [
    (ids) => [{ findingIds: [ids[0]], primary: ids[0], sharedCause: "" }],
    (ids) => [{ findingIds: ids, primary: ids[0], sharedCause: "" }, { findingIds: [ids[1]], primary: ids[1], sharedCause: "" }],
    (ids) => [{ findingIds: ids, primary: "elsewhere", sharedCause: "" }],
    (ids) => [{ findingIds: [...ids, ids[1]], primary: ids[0], sharedCause: "" }],
  ]) {
    const { result, calls } = await runScript(ARGS, responder({
      exploration: explorationFixture({ plan }),
      merge: (_label, prompt) => ({ groups: groups(findingsFromVerifierPrompt(prompt).map((entry) => entry.id)) }),
    }));
    assert.equal(calls.filter((call) => call.opts.label?.startsWith("verify-")).length, 0);
    assert.deepEqual(result.proposedFindings, []);
    assert.ok(result.incomplete.some((reason) => reason.startsWith("agent merge returned a grouping")));
    const verdict = await adjudicateEnvelope(t, result);
    assert.equal(verdict.status, "incomplete");
  }
});

test("a problem a specialist meets outside its concern is verified like any finding, even from an inapplicable specialist", async () => {
  const { result, calls } = await runScript(ARGS, responder({
    specialist: () => specialistResult(
      [finding({ title: "dropped with the inapplicable concern" })],
      { status: "inapplicable", reason: "no ordinary-run logic changed" },
      [],
      [finding({ title: "unchecked error from the new write", line: 12 })],
    ),
  }));
  assert.deepEqual(result.proposedFindings.map(({ title, outsideConcern }) => ({ title, outsideConcern })), [
    { title: "unchecked error from the new write", outsideConcern: true },
  ]);
  assert.equal(calls.filter((call) => call.opts.label?.startsWith("verify-")).length, 1);
  assert.equal(result.briefs[0].skipKind, "inapplicable");
});

test("a finding a verifier raises is verified by a later leg, and one the last round raises leaves the run incomplete", async (t) => {
  let raisedCount = 0;
  const { result, calls } = await runScript(ARGS, responder({
    verify: (label, prompt) => ({
      ...verifierResult(prompt),
      newFindings: [finding({ title: `raised by ${label}`, line: 40 + (raisedCount += 1) })],
    }),
  }));
  const verifierLabels = calls.filter((call) => call.opts.label?.startsWith("verify-")).map((call) => call.opts.label);
  assert.deepEqual(verifierLabels, ["verify-1-claude", "verify-round-2-1-codex", "verify-round-3-1-claude"]);
  assert.deepEqual(
    calls.filter((call) => call.opts.label?.startsWith("verify-")).map((call) => [call.opts.engine, call.opts.model]),
    [["claude", "claude-opus-5-5"], ["codex", "gpt-6.2-astra"], ["claude", "claude-opus-5-5"]],
    "each round is verified by the family that did not raise its findings",
  );
  assert.deepEqual(result.proposedFindings.map(({ id, proposingLabel, verifyLabel }) => ({ id, proposingLabel, verifyLabel })), [
    { id: "specialist-1-logic-codex:1", proposingLabel: "specialist-1-logic-codex", verifyLabel: "verify-1-claude" },
    { id: "verify-1-claude:raised:1", proposingLabel: "verify-1-claude", verifyLabel: "verify-round-2-1-codex" },
    { id: "verify-round-2-1-codex:raised:1", proposingLabel: "verify-round-2-1-codex", verifyLabel: "verify-round-3-1-claude" },
  ]);
  assert.ok(result.proposedFindings.every((entry) => entry.proposingLabel.split("-").at(-1) !== entry.verifyLabel.split("-").at(-1)));
  assert.deepEqual(result.uncheckedFindings.map(({ id, source }) => ({ id, source })), [
    { id: "verify-round-3-1-claude:raised:1", source: "verification" },
  ]);
  assert.ok(result.incomplete.includes("verification stopped after 3 rounds with 1 finding(s) raised and not verified"));
  const verdict = await adjudicateEnvelope(t, result);
  assert.equal(verdict.status, "incomplete");
  assert.ok(verdict.incomplete.includes(
    `finding "raised by verify-round-3-1-claude" has no complete verdict (raised in the last round of verification and never verified)`,
  ));
  assert.deepEqual(verdict.confirmedFindings, []);
  assert.deepEqual(verdict.unsettledFindings, []);
});

test("findings verifiers raise in one round are merged before the next round verifies them", async (t) => {
  const plan = [unit("logic", "logic"), unit("failure", "failure")];
  const merges = [];
  const { result, calls } = await runScript(ARGS, responder({
    exploration: explorationFixture({ plan }),
    specialist: (label) => specialistResult(Array.from({ length: label.includes("logic") ? 4 : 3 }, (_, index) =>
      finding({ title: `${label} ${index}`, line: index + 1 }))),
    merge: (label, prompt) => {
      merges.push(label);
      const ids = findingsFromVerifierPrompt(prompt).map((entry) => entry.id);
      return label === "merge"
        ? separateGroups(prompt)
        : { groups: [{ findingIds: ids, primary: ids[0], sharedCause: "One fix." }] };
    },
    verify: (label, prompt) => ({
      ...verifierResult(prompt),
      newFindings: label.startsWith("verify-round-") ? [] : [finding({ title: "same defect both batches traced", line: 90 })],
    }),
  }));
  assert.deepEqual(merges, ["merge", "merge-round-2"]);
  const merged = calls.find((call) => call.opts.label === "merge-round-2");
  assert.deepEqual(findingsFromVerifierPrompt(merged.prompt).map(({ id, reportedBy }) => ({ id, reportedBy })), [
    { id: "verify-1-claude:raised:1", reportedBy: "verification" },
    { id: "verify-2-claude:raised:1", reportedBy: "verification" },
  ]);
  const secondRound = calls.filter((call) => call.opts.label?.startsWith("verify-round-2-"));
  assert.equal(secondRound.length, 1);
  assert.equal(findingsFromVerifierPrompt(secondRound[0].prompt).length, 1);
  const raised = result.proposedFindings.filter((entry) => entry.source === "verification");
  assert.equal(raised.length, 1);
  assert.deepEqual(raised[0].mergedFrom.map(({ id }) => id), ["verify-2-claude:raised:1"]);
  assert.ok(result.requiredModelEvidence.some((leg) => leg.label === "merge-round-2" && leg.role === "merge"));
  const verdict = await adjudicateEnvelope(t, result);
  assert.equal(verdict.status, "complete", verdict.incomplete.join("\n"));
  assert.equal(verdict.confirmedFindings.length, 8);
});

test("an unsettled verdict reaches the verdict as an open question, never as a confirmed finding", async (t) => {
  const { result } = await runScript(ARGS, responder({
    verify: (_label, prompt) => verifierResult(prompt, () => ({
      verdict: "unsettled",
      severity: "Medium",
      confidence: 50,
      outsideFact: "",
      wouldSettle: "Run the change against a stalled peer.",
    })),
  }));
  const verdict = await adjudicateEnvelope(t, result);
  assert.equal(verdict.status, "complete", verdict.incomplete.join("\n"));
  assert.deepEqual(verdict.confirmedFindings, []);
  assert.deepEqual(verdict.summarySections, [{ kind: "concern", name: "logic", findings: 0 }]);
  assert.deepEqual(verdict.unsettledFindings.map(({ verdict: disposition, wouldSettle }) => ({ disposition, wouldSettle })), [
    { disposition: "unsettled", wouldSettle: "Run the change against a stalled peer." },
  ]);
});

test("slot syntax inside a pull-request description is carried as written, never filled", async () => {
  const args = enumeratedArgs("aaa111", "bbb222", {
    pullRequest: { title: "Template {{head}}", body: "Docs mention {{findings}} and {{unknown-slot}}." },
  });
  const { calls } = await runScript(args, responder());
  for (const call of calls.filter((entry) => entry.opts.label !== "merge")) {
    assert.ok(call.prompt.includes('<pull-request-description title="Template {{head}}">\nDocs mention {{findings}} and {{unknown-slot}}.\n</pull-request-description>'));
  }
});

test("a prompt carrying a slot its stage never fills stops the workflow before any dispatch", async () => {
  const calls = [];
  for (const prompts of [
    { ...ARGS.prompts, verifier: `${ARGS.prompts.verifier}\n{{concern-name}}` },
    { ...ARGS.prompts, specialist: { ...ARGS.prompts.specialist, logic: undefined } },
    { ...ARGS.prompts, merge: "" },
  ]) {
    await assert.rejects(
      runScript({ ...ARGS, prompts }, (label) => { calls.push(label); return null; }),
      /malformed the review prompts/,
    );
  }
  assert.deepEqual(calls, []);
});

test("the exploration plan may name only a shipped concern", async () => {
  assert.deepEqual(ARGS.concerns.map(({ name }) => name), ["failure", "fit", "logic", "security", "tests"]);
  assert.deepEqual(ARGS.contracts.explorationSchema.properties.plan.items.properties.specialistType.enum, ["failure", "fit", "logic", "security", "tests"]);
  const { result, calls } = await runScript(ARGS, responder({
    exploration: explorationFixture({ plan: [unit("old", "correctness")] }),
  }));
  assert.equal(specialistCalls(calls).length, 0);
  assert.deepEqual(result.incomplete, ["exploration returned a malformed review plan"]);
});
