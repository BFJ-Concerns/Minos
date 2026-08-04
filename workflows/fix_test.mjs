import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, unlinkSync, writeFileSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import { sweepDigest } from "./completion-policy.mjs";
import { DECISION_KIND, isFixWavePlan, prepareFixWave } from "./fix-wave-plan.mjs";

const scriptPath = fileURLToPath(new URL("./fix.js", import.meta.url));
const inputScriptPath = fileURLToPath(new URL("./fix-inputs.mjs", import.meta.url));
const source = await readFile(scriptPath, "utf8");
const fixerBriefPath = fileURLToPath(new URL("./review-briefs/fixer.md", import.meta.url));
const fixerBriefContent = await readFile(fixerBriefPath, "utf8");
const body = source.replace(/^export const meta =/m, "const meta =");
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
const script = new AsyncFunction("agent", "parallel", "pipeline", "phase", "log", "args", body);

function finding(title, severity, path, line, id = `specialist:${title}`) {
  return { id, title, severity, confidence: 90, path, line, explanation: `${title} breaks the contract` };
}

function grouping(...groups) {
  return { kind: "minos-fix-grouping-v1", groups: groups.map((findings) => ({ findings })) };
}

function decision(classification, basis = "recorded grounds for this sweep's call") {
  return { kind: DECISION_KIND, classification, basis };
}

// The default decision follows the digest's own threshold indication — what
// a lead following the indication records — computed through the seam, never
// re-derived. Tests exercising judgement against the indication pass their
// own decision.
function indicatedDecision(findings, overrides) {
  const digest = sweepDigest({
    review: { status: "complete", confirmedFindings: findings },
    threshold: overrides.threshold ?? "High",
    maximumRounds: overrides.maximumRounds ?? null,
    runRecord: overrides.runRecord ?? { round: 0, confirmedUnfixed: [] },
  });
  return decision(digest.status === "complete" ? digest.thresholdIndication : "terminal");
}

function args(findings, overrides = {}) {
  return {
    review: {
      status: "complete",
      reviewed: { target: "target111", head: "head222" },
      confirmedFindings: findings,
    },
    threshold: "High",
    maximumRounds: null,
    runRecord: { round: 0, confirmedUnfixed: [] },
    decision: indicatedDecision(findings, overrides),
    workspace: "/run/workspace",
    verification: { build: "make build", tests: "make test" },
    guidance: { grounding: "annexe", path: "/run/subject-Annexe/README.md", content: "COMMISSION_VIOLET_719" },
    fixerBrief: { path: "workflows/review-briefs/fixer.md", readPath: fixerBriefPath, content: fixerBriefContent },
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

test("fix-facing source surfaces contain no deterministic cluster or file boundary", async () => {
  const paths = [
    scriptPath,
    fileURLToPath(new URL("./fix-wave-plan.mjs", import.meta.url)),
    inputScriptPath,
    fixerBriefPath,
  ];
  const surfaces = (await Promise.all(paths.map((path) => readFile(path, "utf8")))).join("\n");
  assert.doesNotMatch(
    surfaces,
    /MINOS_FIX_CLUSTER_CAP|clusterCap|cluster-cap|clustersFor|Assigned files|Stay within|directly related context/i,
  );
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
  assert.equal(calls.length, 3);
  assert.equal(result.dispatches.length, 3);
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
  assert.equal(result.decision.classification, "terminal");
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

test("default dispatch keeps every finding whole and carries purpose scope without a file boundary", async () => {
  const findings = [
    finding("a1", "High", "a.go", 1),
    finding("a2", "Low", "a.go", 2),
    finding("b1", "Low", "b.go", 1),
    finding("c1", "Low", "c.go", 1),
    finding("d1", "Low", "d.go", 1),
  ];
  const { result, calls } = await run(args(findings));
  assert.equal(result.dispatches.length, findings.length);
  assert.ok(result.dispatches.every((entry) => entry.findingKeys.length === 1));
  assert.ok(result.dispatches.every((entry) => !Object.hasOwn(entry, "files")));
  assert.deepEqual(calls.map((call) => assignedFindings(call.prompt)[0].title), findings.map((item) => item.title));
  for (const call of calls) {
    assert.match(call.prompt, /Repair these findings and only these findings/);
    assert.match(call.prompt, /purpose, not territory/);
    assert.doesNotMatch(call.prompt, /Assigned files|Stay within/i);
  }
});

test("duplicate finding keys fail closed before dispatch", async () => {
  const first = finding("race", "Critical", "a.go", 7, "specialist:first");
  first.explanation = "critical explanation";
  const second = finding(" Race ", "Low", "a.go", 7, "specialist:second");
  second.explanation = "low explanation";
  const { result, calls } = await run(args([first, second]));
  assert.equal(result.status, "incomplete");
  assert.equal(result.reason, 'fix preparation has duplicate finding key ["a.go",7,"race"]');
  assert.deepEqual(result.dispatches, []);
  assert.deepEqual(calls, []);
});

test("lead grouping flows through validation to findings-scoped dispatch intact", async () => {
  const findings = [
    finding("first", "High", "a.go", 1),
    finding("second", "Low", "b.go", 2),
    finding("third", "Low", "c.go", 3),
  ];
  const input = args(findings, { grouping: grouping([findings[0].id, findings[2].id], [findings[1].id]) });
  const plan = prepareFixWave(input);
  const defaultPlan = prepareFixWave(args(findings));
  assert.deepEqual(plan.grouping, { source: "lead" });
  assert.deepEqual(plan.dispatches.map((entry) => entry.findingKeys), [
    [plan.findings[0].key, plan.findings[2].key],
    [plan.findings[1].key],
  ]);
  assert.equal(Object.hasOwn(plan, "clusters"), false);
  assert.ok(plan.dispatches.every((entry) => !Object.hasOwn(entry, "files")));
  assert.notEqual(plan.fingerprint, defaultPlan.fingerprint);

  const { result, calls } = await run(plan);
  assert.equal(result.dispatches.length, 2);
  assert.deepEqual(calls.map((call) => assignedFindings(call.prompt).map((item) => item.id)), [
    [findings[0].id, findings[2].id],
    [findings[1].id],
  ]);
});

test("invalid lead grouping fails closed before dispatch", async (t) => {
  const findings = [finding("first", "High", "a.go", 1), finding("second", "Low", "b.go", 2)];
  for (const testCase of [
    { name: "malformed", grouping: null, reason: /fix grouping must be an object/ },
    { name: "omitted", grouping: grouping([findings[0].id]), reason: /omits confirmed finding specialist:second/ },
    { name: "unknown", grouping: grouping([findings[0].id, "specialist:unknown"], [findings[1].id]), reason: /names unknown finding specialist:unknown/ },
    { name: "duplicate", grouping: grouping([findings[0].id], [findings[0].id, findings[1].id]), reason: /assigns finding specialist:first to more than one dispatch/ },
  ]) {
    await t.test(testCase.name, async () => {
      const { result, calls } = await run(args(findings, { grouping: testCase.grouping }));
      assert.equal(result.status, "incomplete");
      assert.match(result.reason, testCase.reason);
      assert.deepEqual(calls, []);
    });
  }
});

test("lead grouping rejects duplicate candidate ids before dispatch", async () => {
  const duplicateID = "repository-review-design-md-claude:1";
  const findings = [
    finding("first", "Critical", "a.go", 1, duplicateID),
    finding("second", "High", "b.go", 2, duplicateID),
  ];
  const { result, calls } = await run(args(findings, { grouping: grouping([duplicateID]) }));
  assert.equal(result.status, "incomplete");
  assert.equal(result.reason, "fix grouping has duplicate candidate finding id repository-review-design-md-claude:1");
  assert.deepEqual(result.dispatches, []);
  assert.deepEqual(calls, []);
});

test("terminal classification ignores grouping because it dispatches nothing", async () => {
  const { result, calls } = await run(args([finding("wording", "Low", "a.go", 4)], { grouping: null }));
  assert.equal(result.classification, "terminal");
  assert.deepEqual(calls, []);
});

test("a wave records one lead push for all distinct agent commits", async () => {
  const findings = [finding("first", "High", "a.go", 1), finding("second", "Low", "b.go", 2)];
  const { result } = await run(args(findings));
  assert.deepEqual(result.integration.commits, ["fix-dispatch-1-commit", "fix-dispatch-2-commit"]);
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
    if (expected.whitespaceOnly) assert.doesNotMatch(prompt, /Run this configured (?:build|test) command/i);
  }
});

test("a recorded Rust target fallback reaches composed fix prompts only while present", async () => {
  const root = mkdtempSync(join(tmpdir(), "minos-fix-rust-target-fallback-"));
  const workspace = join(root, "workspace");
  const target = join(workspace, "target");
  const guidancePath = join(root, "README.md");
  const orientationPath = join(root, "orientation.json");
  const reviewPath = join(root, "review.json");
  const fallbackRecord = join(root, "rust-target-fallback");
  mkdirSync(target, { recursive: true });
  writeFileSync(guidancePath, "COMMISSION_VIOLET_719");
  writeFileSync(orientationPath, JSON.stringify({ repository: workspace, grounding: "annexe", guidance: guidancePath }));
  writeFileSync(reviewPath, JSON.stringify({
    status: "complete",
    reviewed: { target: "target111", head: "head222" },
    confirmedFindings: [finding("Rust repair", "High", "src/lib.rs", 7)],
  }));
  writeFileSync(fallbackRecord, `${target}\n`);
  const environment = {
    ...process.env,
    MINOS_RUN_DIR: root,
    MINOS_ORIENTATION: orientationPath,
    MINOS_WORKSPACE: workspace,
  };

  const recordedPlan = JSON.parse(execFileSync(process.execPath, [inputScriptPath, reviewPath, "--single-wave"], {
    encoding: "utf8",
    env: environment,
  }));
  assert.equal(recordedPlan.input.warmTargetSource, target);
  const recorded = await run(recordedPlan);
  assert.match(recorded.calls[0].prompt, /seed this isolated worktree's Rust target/);
  assert.ok(recorded.calls[0].prompt.includes(JSON.stringify(target)));

  unlinkSync(fallbackRecord);
  const ordinaryPlan = JSON.parse(execFileSync(process.execPath, [inputScriptPath, reviewPath, "--single-wave"], {
    encoding: "utf8",
    env: environment,
  }));
  assert.equal(ordinaryPlan.input.warmTargetSource, undefined);
  const ordinary = await run(ordinaryPlan);
  assert.doesNotMatch(ordinary.calls[0].prompt, /seed this isolated worktree's Rust target/);
  assert.ok(!ordinary.calls[0].prompt.includes(target));
});

test("fix write-ups keep the finding path and line", async () => {
  const original = finding("transition", "High", "internal/state.go", 41);
  const { result } = await run(args([original]));
  assert.deepEqual(result.fixReview.comments, [{
    path: "internal/state.go",
    body: "Repaired transition.",
    line: 41,
  }]);
  assert.deepEqual(result.runRecord.confirmedFixed, [{
    key: result.runRecord.confirmedFixed[0].key,
    finding: {
      ...original,
      key: result.runRecord.confirmedFixed[0].key,
      identity: result.runRecord.confirmedFixed[0].finding.identity,
    },
    writeUp: "Repaired transition.",
  }]);
});

test("a lead grouping cannot redispatch a twice-failed finding across a round boundary", async () => {
  const original = finding("transition", "High", "internal/state.go", 41);
  const first = await run(args([original], { grouping: grouping([original.id]) }), (label, prompt) => ({
    commit: "",
    fixes: assignedFindings(prompt).map((item) => ({ findingKey: item.key, status: "failed", writeUp: `${label} could not repair it` })),
  }));
  assert.equal(first.calls.length, 2);
  assert.equal(first.result.confirmedUnfixed.length, 1);
  assert.equal(first.result.confirmedUnfixed[0].attempts, 2);

  const repairable = finding("other transition", "High", "internal/other.go", 12);
  const next = await run(args([original, repairable], {
    runRecord: first.result.runRecord,
    grouping: grouping([original.id], [repairable.id]),
  }));
  assert.equal(next.result.classification, "working");
  assert.equal(next.result.round, 2);
  assert.notEqual(next.result.fingerprint, first.result.fingerprint);
  assert.equal(next.result.dispatches.length, 1);
  assert.equal(next.calls.length, 1);
  assert.deepEqual(assignedFindings(next.calls[0].prompt).map((item) => item.id), [repairable.id]);
  assert.equal(next.result.runRecord.confirmedUnfixed[0].attempts, 2);

  const terminal = await run(args([original], { runRecord: next.result.runRecord }));
  assert.equal(terminal.result.classification, "terminal");
  assert.equal(terminal.result.requestChanges.length, 1);
  assert.equal(terminal.result.requestChangesReview.verdict, "request-changes");
  assert.deepEqual(terminal.result.requestChangesReview.comments[0], {
    path: "internal/state.go",
    body: "**transition**\n\ntransition breaks the contract\n\nSeverity: High. Confidence: 90.",
    line: 41,
  });
  assert.equal(terminal.calls.length, 0);
});

test("confirmed-unfixed identity drift stays suppressed across round-boundary planning", async () => {
  const original = finding("transition corrupts state", "High", "internal/state.go", 41);
  original.explanation = "The transition stores the new state before validation completes.";
  const first = await run(args([original]), (_label, prompt) => ({
    commit: "",
    fixes: assignedFindings(prompt).map((item) => ({
      findingKey: item.key,
      status: "failed",
      writeUp: "could not repair it",
    })),
  }));
  assert.equal(first.result.confirmedUnfixed[0].attempts, 2);

  const distinct = finding("transition drops audit event", "High", "internal/state.go", 48);
  distinct.explanation = "The transition returns before appending its audit event.";
  for (const drifted of [
    { ...original, line: 48 },
    { ...original, title: "Validation happens after the state write" },
  ]) {
    const next = await run(args([drifted, distinct], { runRecord: first.result.runRecord }));
    assert.equal(next.result.classification, "working");
    assert.equal(next.calls.length, 1, "the confirmed-unfixed finding was re-dispatched");
    assert.deepEqual(
      assignedFindings(next.calls[0].prompt).map((item) => item.title),
      [distinct.title],
      "same-path findings with different defect explanations must remain distinct",
    );
  }
});

test("rewording a confirmed-unfixed explanation remains a new dispatch", async () => {
  const original = finding("transition corrupts state", "High", "internal/state.go", 41);
  original.explanation = "The transition stores the new state before validation completes.";
  const first = await run(args([original]), (_label, prompt) => ({
    commit: "",
    fixes: assignedFindings(prompt).map((item) => ({
      findingKey: item.key,
      status: "failed",
      writeUp: "could not repair it",
    })),
  }));
  assert.equal(first.result.confirmedUnfixed[0].attempts, 2);

  const reworded = {
    ...original,
    line: 48,
    explanation: "Validation completes only after the transition has stored the new state.",
  };
  const next = await run(args([reworded], { runRecord: first.result.runRecord }));
  assert.equal(next.result.classification, "working");
  assert.equal(next.calls.length, 1);
  assert.deepEqual(
    assignedFindings(next.calls[0].prompt).map((item) => item.explanation),
    [reworded.explanation],
  );
});

test("the configured maximum rounds stops another fix dispatch", async () => {
  const original = finding("transition", "High", "internal/state.go", 41);
  const { result, calls } = await run(args([original], {
    maximumRounds: 1,
    runRecord: { round: 1, confirmedUnfixed: [] },
  }));
  assert.equal(result.classification, "terminal");
  assert.equal(result.requestChanges.length, 1);
  assert.equal(calls.length, 0);
});

test("a working decision past the maximum rounds fails closed before any dispatch", async () => {
  const original = finding("transition", "High", "internal/state.go", 41);
  const { result, calls } = await run(args([original], {
    maximumRounds: 1,
    runRecord: { round: 1, confirmedUnfixed: [] },
    decision: decision("working", "another wave would repair the finding"),
  }));
  assert.equal(result.status, "incomplete");
  assert.match(result.reason, /maximum rounds has been reached/);
  assert.deepEqual(result.dispatches, []);
  assert.equal(calls.length, 0);
});

test("judgement may end the loop against the threshold indication", async () => {
  const findings = [finding("restated ground", "High", "a.go", 4)];
  const { result, calls } = await run(args(findings, {
    decision: decision("terminal", "the finding restates already-adjudicated ground"),
  }));
  assert.equal(result.classification, "terminal");
  assert.equal(result.decision.basis, "the finding restates already-adjudicated ground");
  assert.equal(result.requestChanges.length, 1);
  assert.equal(result.requestChanges[0].attempts, 0);
  assert.equal(result.requestChangesReview.verdict, "request-changes");
  assert.equal(calls.length, 0);
});

test("a decision without a stated basis fails closed before any forge write", async () => {
  const findings = [finding("transition", "High", "a.go", 4)];
  const { result, calls } = await run(args(findings, {
    decision: { kind: DECISION_KIND, classification: "working", basis: "   " },
  }));
  assert.equal(result.status, "incomplete");
  assert.match(result.reason, /non-empty basis/);
  assert.equal(calls.length, 0);
});

test("a missing decision fails closed before any forge write", async () => {
  const findings = [finding("transition", "High", "a.go", 4)];
  const { result, calls } = await run(args(findings, { decision: undefined }));
  assert.equal(result.status, "incomplete");
  assert.match(result.reason, /sweep decision must be an object/);
  assert.equal(calls.length, 0);
});

test("single-wave mode dispatches every brief finding once and requests build and tests without re-review", async () => {
  const findings = [
    finding("material", "High", "a.go", 1),
    finding("minor", "Low", "b.go", 2),
  ];
  const { result, calls } = await run(args(findings, { singleWave: true }));
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
      assert.deepEqual(result.integration.commits, ["brief-fix-dispatch-1-verified-commit"]);
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

test("the deterministic fix input carries loop knobs, grouping judgement, and the run record", () => {
  const root = mkdtempSync(join(tmpdir(), "minos-fix-inputs-"));
  const guidancePath = join(root, "README.md");
  const orientationPath = join(root, "orientation.json");
  const reviewPath = join(root, "review.json");
  const recordPath = join(root, "record.json");
  const groupingPath = join(root, "grouping.json");
  writeFileSync(guidancePath, "COMMISSION_VIOLET_719");
  writeFileSync(orientationPath, JSON.stringify({ repository: root, grounding: "annexe", guidance: guidancePath }));
  writeFileSync(reviewPath, JSON.stringify({
    status: "complete",
    reviewed: { target: "target111", head: "head222" },
    confirmedFindings: [],
  }));
  writeFileSync(recordPath, JSON.stringify({ round: 3, confirmedUnfixed: [] }));
  const leadGrouping = grouping(["specialist:one"]);
  writeFileSync(groupingPath, JSON.stringify(leadGrouping));
  const decisionPath = join(root, "sweep-decision.json");
  const leadDecision = decision("terminal", "nothing dispatchable remains");
  writeFileSync(decisionPath, JSON.stringify(leadDecision));
  const input = JSON.parse(execFileSync(
    process.execPath,
    [inputScriptPath, "--grouping", groupingPath, "--decision", decisionPath, reviewPath, recordPath],
    {
      encoding: "utf8",
      env: {
        ...process.env,
        MINOS_ORIENTATION: orientationPath,
        MINOS_WORKSPACE: root,
        MINOS_REVIEW_THRESHOLD: "Medium",
        MINOS_MAX_ROUNDS: "8",
        MINOS_BUILD_CMD: "make build",
        MINOS_TEST_CMD: "make test",
      },
    },
  ));
  assert.equal(input.threshold, "Medium");
  assert.equal(input.maximumRounds, 8);
  assert.equal(input.runRecord.round, 3);
  assert.deepEqual(input.grouping, leadGrouping);
  assert.deepEqual(input.decision, leadDecision);
  assert.deepEqual(input.verification, { build: "make build", tests: "make test" });
  assert.equal(input.guidance.content, "COMMISSION_VIOLET_719");
});

test("a seeded continuation record reaches the digest and the first fix-wave ledger", () => {
  const root = mkdtempSync(join(tmpdir(), "minos-fix-inputs-continuation-"));
  const guidancePath = join(root, "README.md");
  const orientationPath = join(root, "orientation.json");
  const reviewPath = join(root, "review.json");
  const recordPath = join(root, "loop-record.json");
  const decisionPath = join(root, "sweep-decision.json");
  const predecessorFinding = finding("transition", "High", "internal/state.go", 41);
  const newFinding = finding("other transition", "High", "internal/other.go", 12);
  writeFileSync(guidancePath, "COMMISSION_VIOLET_719");
  writeFileSync(orientationPath, JSON.stringify({ repository: root, grounding: "annexe", guidance: guidancePath }));
  writeFileSync(reviewPath, JSON.stringify({
    status: "complete",
    reviewed: { target: "target111", head: "head222" },
    confirmedFindings: [predecessorFinding, newFinding],
  }));
  writeFileSync(recordPath, JSON.stringify({
    round: 3,
    confirmedUnfixed: [{
      key: JSON.stringify([predecessorFinding.path, predecessorFinding.line, predecessorFinding.title]),
      finding: predecessorFinding,
      attempts: 2,
      reason: "two attempts failed",
    }],
  }));
  writeFileSync(decisionPath, JSON.stringify(decision("working", "one new High finding is dispatchable")));

  const digest = JSON.parse(execFileSync(process.execPath, [inputScriptPath, reviewPath, recordPath, "--digest"], {
    encoding: "utf8",
    env: { ...process.env, MINOS_ORIENTATION: orientationPath, MINOS_WORKSPACE: root },
  }));
  assert.equal(digest.round, 4);
  assert.equal(digest.thresholdIndication, "working");
  assert.deepEqual(digest.aboveThresholdKeys, [
    JSON.stringify([newFinding.path, newFinding.line, newFinding.title]),
  ]);
  assert.deepEqual(digest.priorConfirmedUnfixed, [{
    key: JSON.stringify([predecessorFinding.path, predecessorFinding.line, predecessorFinding.title]),
    attempts: 2,
  }]);

  const input = JSON.parse(execFileSync(
    process.execPath,
    [inputScriptPath, reviewPath, recordPath, "--decision", decisionPath],
    { encoding: "utf8", env: { ...process.env, MINOS_ORIENTATION: orientationPath, MINOS_WORKSPACE: root } },
  ));
  const plan = prepareFixWave(input);
  assert.equal(plan.round, 4);
  assert.equal(plan.priorConfirmedUnfixed.length, 1);
  assert.equal(plan.priorConfirmedUnfixed[0].attempts, 2);
  assert.deepEqual(plan.dispatches.flatMap((dispatch) => dispatch.findingKeys), [
    JSON.stringify([newFinding.path, newFinding.line, newFinding.title]),
  ]);
});

test("a malformed grouping file becomes an incomplete single-wave plan", () => {
  const root = mkdtempSync(join(tmpdir(), "minos-fix-inputs-grouping-"));
  const guidancePath = join(root, "README.md");
  const orientationPath = join(root, "orientation.json");
  const reviewPath = join(root, "review.json");
  const groupingPath = join(root, "grouping.json");
  writeFileSync(guidancePath, "COMMISSION_VIOLET_719");
  writeFileSync(orientationPath, JSON.stringify({ repository: root, grounding: "annexe", guidance: guidancePath }));
  writeFileSync(reviewPath, JSON.stringify({
    status: "complete",
    reviewed: { target: "target111", head: "head222" },
    confirmedFindings: [finding("first", "High", "a.go", 1)],
  }));
  writeFileSync(groupingPath, "not JSON");
  const plan = JSON.parse(execFileSync(
    process.execPath,
    [inputScriptPath, reviewPath, "--grouping", groupingPath, "--single-wave"],
    { encoding: "utf8", env: { ...process.env, MINOS_ORIENTATION: orientationPath, MINOS_WORKSPACE: root } },
  ));
  assert.equal(plan.status, "incomplete");
  assert.match(plan.reason, /fix grouping must be an object/);
  assert.deepEqual(plan.dispatches, []);
});

test("fix input option and unreadable grouping errors use the usage exit", () => {
  const root = mkdtempSync(join(tmpdir(), "minos-fix-inputs-errors-"));
  const missingGroupingPath = join(root, "missing-grouping.json");
  for (const testCase of [
    {
      name: "unknown option",
      args: [inputScriptPath, "review.json", "--unknown"],
      message: "unknown fix input option --unknown",
    },
    {
      name: "unreadable grouping",
      args: [inputScriptPath, "review.json", "--grouping", missingGroupingPath],
      message: `fix grouping file is unreadable: ${missingGroupingPath}`,
    },
    {
      name: "unreadable decision",
      args: [inputScriptPath, "review.json", "--decision", missingGroupingPath],
      message: `sweep decision file is unreadable: ${missingGroupingPath}`,
    },
    {
      name: "digest stands alone",
      args: [inputScriptPath, "review.json", "--digest", "--single-wave"],
      message: "fix input option --digest stands alone",
    },
    {
      name: "surplus positional",
      args: [inputScriptPath, "review.json", "record.json", "stray.json", "--single-wave"],
      message: "unexpected fix input argument stray.json",
    },
  ]) {
    const result = spawnSync(process.execPath, testCase.args, { encoding: "utf8" });
    assert.equal(result.status, 2, testCase.name);
    assert.equal(result.stdout, "", testCase.name);
    assert.ok(result.stderr.includes(testCase.message), testCase.name);
    assert.match(result.stderr, /usage: node workflows\/fix-inputs\.mjs/, testCase.name);
    assert.doesNotMatch(result.stderr, /\n\s+at /, testCase.name);
  }
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
