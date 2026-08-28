import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

import { adjudicate } from "./run-record-adjudicator.mjs";
import { DECISION_KIND, prepareFixWave } from "./fix-wave-plan.mjs";

const scriptPath = fileURLToPath(new URL("./review.js", import.meta.url));
const inputScriptPath = fileURLToPath(new URL("./review-inputs.mjs", import.meta.url));
const publishMembersScriptPath = fileURLToPath(new URL("./publish-member-reviews.mjs", import.meta.url));
const fixScriptPath = fileURLToPath(new URL("./fix.js", import.meta.url));
const source = await readFile(scriptPath, "utf8");
const fixSource = await readFile(fixScriptPath, "utf8");
const lifecycle = await readFile(fileURLToPath(new URL("../lifecycle/lifecycle.md", import.meta.url)), "utf8");
const maintenance = await readFile(fileURLToPath(new URL("../lifecycle/maintenance.md", import.meta.url)), "utf8");
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
    pullRequest,
    absentPullRequestRecord = false,
    loopRecord,
    absentLoopRecord = false,
    members,
    minosBin,
  } = {},
) {
  const root = mkdtempSync(join(tmpdir(), "minos-review-inputs-"));
  const workspace = join(root, "workspace");
  mkdirSync(workspace);
  const guidancePath = join(workspace, guidanceName);
  writeFileSync(guidancePath, guidanceContent);
  const orientation = { repository: workspace, grounding, guidance: guidancePath };
  if (pullRequest !== undefined || absentPullRequestRecord) {
    const pullRequestPath = join(root, "pull-request.json");
    if (!absentPullRequestRecord)
      writeFileSync(pullRequestPath, typeof pullRequest === "string" ? pullRequest : JSON.stringify(pullRequest));
    orientation.pullRequest = pullRequestPath;
  }
  const orientationPath = join(root, "orientation.json");
  writeFileSync(orientationPath, JSON.stringify(orientation));
  if (members !== undefined) {
    execFileSync("git", ["-C", workspace, "init", "--quiet"]);
    execFileSync("git", ["-C", workspace, "-c", "user.name=Minos", "-c", "user.email=minos@example.invalid", "commit", "--allow-empty", "--quiet", "-m", "fixture"]);
  }
  const cliArgs = [inputScriptPath, target, head];
  if (loopRecord !== undefined || absentLoopRecord) {
    const recordPath = join(root, "loop-record.json");
    if (!absentLoopRecord)
      writeFileSync(recordPath, typeof loopRecord === "string" ? loopRecord : JSON.stringify(loopRecord));
    cliArgs.push("--loop-record", recordPath);
  }
  if (members !== undefined) {
    const membersPath = join(root, "members.json");
    writeFileSync(membersPath, typeof members === "string" ? members : JSON.stringify(members));
    cliArgs.push("--members", membersPath);
  }
  return JSON.parse(execFileSync(process.execPath, cliArgs, {
    encoding: "utf8",
    env: {
      ...process.env,
      MINOS_ORIENTATION: orientationPath,
      MINOS_OWNER: "minos-e2e-owner",
      MINOS_REPO_NAME: "subject",
      MINOS_PR: "1",
      ...(minosBin ? { MINOS_BIN: minosBin } : {}),
    },
    stdio: ["ignore", "pipe", "pipe"],
  }));
}

async function runFixPlan(plan, respond) {
  const parallel = async (thunks) => Promise.all(thunks.map((thunk) => thunk().catch(() => null)));
  return fixScript(respond, parallel, async (items) => items, () => {}, () => {}, plan);
}

const ARGS = enumeratedArgs();

test("a solo input derives its member from the run without a grouped members record", () => {
  assert.deepEqual(ARGS.members, [{
    id: "primary",
    owner: "minos-e2e-owner",
    repo: "subject",
    number: 1,
    target: "aaa111",
    head: "bbb222",
  }]);
});

function unit(id = "logic", specialistType = "correctness", scope = ["internal/x.go"], member = "primary") {
  return { id, member, concern: `${specialistType} concern ${id}`, scope, specialistType };
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
    member: "primary",
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
  const match = prompt.match(/Orientation packet: ([^\n]+)\n/);
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
    "briefs", "dispatches", "exploration", "members", "misconfigurations", "outOfScopeObservations", "proposedFindings",
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
    ],
  );
  assert.ok(calls.every((call) => call.opts.engine === "codex" || call.opts.engine === "claude"));
  assert.ok(calls.every((call) => !("fallbackModel" in call.opts)));
});

test("a two-member input keeps each finding attributed through verification and member publication", async (t) => {
  const members = { members: [
    {
      id: "primary", owner: "minos-e2e-owner", repo: "subject", number: 1,
      target: "primary-target", head: "primary-head", diff: "PRIMARY_MEMBER_DIFF",
      title: "Primary title", body: "PRIMARY_MEMBER_DESCRIPTION",
    },
    {
      id: "sibling", owner: "minos-e2e-owner", repo: "sibling", number: 2,
      target: "sibling-target", head: "sibling-head", diff: "SIBLING_MEMBER_DIFF",
      title: "Sibling title", body: "SIBLING_MEMBER_DESCRIPTION",
    },
  ] };
  const args = { ...enumeratedArgs("target", "head"), members: members.members };
  const plan = [unit("primary", "correctness", ["internal/primary.go"], "primary"), unit("sibling", "security", ["internal/sibling.go"], "sibling")];
  const { result, calls } = await runScript(args, responder({
    exploration: explorationFixture({
      files: [
        { path: "internal/primary.go", added: 1, deleted: 0 },
        { path: "internal/sibling.go", added: 1, deleted: 0 },
      ],
      plan,
    }),
    specialist: (label) => specialistResult([finding({
      member: label.includes("security") ? "sibling" : "primary",
      title: label.includes("security") ? "sibling finding" : "primary finding",
    })], undefined, [observation({ title: `${label} observation` })]),
    verify: (label, prompt) => ({
      ...verifierResult(prompt),
      outOfScopeObservations: [observation({ member: "sibling", title: `${label} observation` })],
    }),
  }));
  assert.deepEqual(result.members, members.members);
  assert.deepEqual(
    result.proposedFindings.map(({ member, title }) => ({ member, title })).sort((left, right) => left.member.localeCompare(right.member)),
    [
      { member: "primary", title: "primary finding" },
      { member: "sibling", title: "sibling finding" },
    ],
  );
  assert.match(calls.find((call) => call.opts.label === "exploration").prompt, /SIBLING_MEMBER_DIFF/);

  const primarySpecialist = calls.find((call) => call.opts.label === "specialist-1-correctness-gpt");
  const siblingSpecialist = calls.find((call) => call.opts.label === "specialist-2-security-gpt");
  assert.match(primarySpecialist.prompt, /primary-target\.\.\.primary-head/);
  assert.match(primarySpecialist.prompt, /PRIMARY_MEMBER_DIFF/);
  assert.match(primarySpecialist.prompt, /PRIMARY_MEMBER_DESCRIPTION/);
  assert.doesNotMatch(primarySpecialist.prompt, /SIBLING_MEMBER_(?:DIFF|DESCRIPTION)/);
  assert.match(siblingSpecialist.prompt, /sibling-target\.\.\.sibling-head/);
  assert.match(siblingSpecialist.prompt, /SIBLING_MEMBER_DIFF/);
  assert.match(siblingSpecialist.prompt, /SIBLING_MEMBER_DESCRIPTION/);
  assert.doesNotMatch(siblingSpecialist.prompt, /PRIMARY_MEMBER_(?:DIFF|DESCRIPTION)/);

  const verifier = calls.find((call) => call.opts.label === "verify-1-claude");
  assert.match(verifier.prompt, /primary-target.*primary-head/);
  assert.match(verifier.prompt, /PRIMARY_MEMBER_DIFF/);
  assert.match(verifier.prompt, /PRIMARY_MEMBER_DESCRIPTION/);
  assert.match(verifier.prompt, /sibling-target.*sibling-head/);
  assert.match(verifier.prompt, /SIBLING_MEMBER_DIFF/);
  assert.match(verifier.prompt, /SIBLING_MEMBER_DESCRIPTION/);

  assert.deepEqual(
    result.outOfScopeObservations.map(({ member, title }) => ({ member, title })),
    [
      { member: "primary", title: "specialist-1-correctness-gpt observation" },
      { member: "sibling", title: "specialist-2-security-gpt observation" },
      { member: "sibling", title: "verify-1-claude observation" },
    ],
  );

  const verdict = await adjudicateEnvelope(t, result);
  assert.equal(verdict.status, "complete", verdict.incomplete.join("\n"));
  assert.deepEqual(verdict.memberReviews.map(({ member, status }) => ({ member, status })), [
    { member: "primary", status: "attention" },
    { member: "sibling", status: "attention" },
  ]);
  assert.match(verdict.memberReviews[0].comments[0].body, /primary finding/);
  assert.doesNotMatch(verdict.memberReviews[0].comments[0].body, /sibling finding/);
  assert.match(verdict.memberReviews[1].comments[0].body, /sibling finding/);
  assert.doesNotMatch(verdict.memberReviews[1].comments[0].body, /primary finding/);
});

test("the members CLI snapshots each member and carries its fresh diff", () => {
  const root = mkdtempSync(join(tmpdir(), "minos-member-inputs-"));
  const callLog = join(root, "calls.log");
  const minosBin = join(root, "minos-fixture");
  writeFileSync(minosBin, `#!/bin/sh\nprintf '%s\\n' "$*" >> '${callLog}'\nprintf '{"head_sha":"HEAD","target_sha":"HEAD"}\\n'\n`);
  chmodSync(minosBin, 0o755);
  const input = enumeratedArgs("target", "head", {
    minosBin,
    members: { members: [
      { id: "primary", owner: "minos-e2e-owner", repo: "subject", number: 1 },
      { id: "sibling", owner: "minos-e2e-owner", repo: "sibling", number: 2 },
    ] },
  });
  assert.deepEqual(input.members.map(({ id, head, target, diff }) => ({ id, head, target, diff })), [
    { id: "primary", head: "HEAD", target: "HEAD", diff: "" },
    { id: "sibling", head: "HEAD", target: "HEAD", diff: "" },
  ]);
  assert.deepEqual(readFileSync(callLog, "utf8").trim().split("\n"), [
    "forge --member minos-e2e-owner subject 1 snapshot",
    "forge --member minos-e2e-owner sibling 2 snapshot",
  ]);
});

test("the members CLI carries the diff returned by each fresh snapshot", () => {
  const root = mkdtempSync(join(tmpdir(), "minos-member-input-diff-"));
  const workspace = join(root, "workspace");
  mkdirSync(workspace);
  execFileSync("git", ["-C", workspace, "init", "--quiet"]);
  execFileSync("git", ["-C", workspace, "config", "user.name", "Minos"]);
  execFileSync("git", ["-C", workspace, "config", "user.email", "minos@example.invalid"]);
  writeFileSync(join(workspace, "member.txt"), "before\n");
  execFileSync("git", ["-C", workspace, "add", "member.txt"]);
  execFileSync("git", ["-C", workspace, "commit", "--quiet", "-m", "base"]);
  const target = execFileSync("git", ["-C", workspace, "rev-parse", "HEAD"], { encoding: "utf8" }).trim();
  writeFileSync(join(workspace, "member.txt"), "after\n");
  execFileSync("git", ["-C", workspace, "commit", "-am", "member change", "--quiet"]);
  const head = execFileSync("git", ["-C", workspace, "rev-parse", "HEAD"], { encoding: "utf8" }).trim();
  const guidancePath = join(root, "guidance.md");
  const orientationPath = join(root, "orientation.json");
  const membersPath = join(root, "members.json");
  const minosBin = join(root, "minos-fixture");
  writeFileSync(guidancePath, "fixture guidance\n");
  writeFileSync(orientationPath, JSON.stringify({ repository: workspace, guidance: guidancePath }));
  writeFileSync(membersPath, JSON.stringify({ members: [
    { id: "primary", owner: "minos-e2e-owner", repo: "subject", number: 1 },
  ] }));
  writeFileSync(minosBin, `#!/bin/sh\nprintf '{"head_sha":"${head}","target_sha":"${target}"}\\n'\n`);
  chmodSync(minosBin, 0o755);
  const input = JSON.parse(execFileSync(process.execPath, [inputScriptPath, target, head, "--members", membersPath], {
    encoding: "utf8",
    env: { ...process.env, MINOS_ORIENTATION: orientationPath, MINOS_BIN: minosBin },
  }));
  assert.match(input.members[0].diff, /-before/);
  assert.match(input.members[0].diff, /\+after/);
});

test("terminal member publication writes each member's own request-changes review and status", () => {
  const root = mkdtempSync(join(tmpdir(), "minos-member-publication-"));
  const callLog = join(root, "calls.log");
  const minosBin = join(root, "minos-fixture");
  writeFileSync(minosBin, `#!/bin/sh\nprintf '%s\\n' "$*" >> '${callLog}'\nif [ "$6" = snapshot ]; then printf '{"head_sha":"head-%s","target_sha":"target-%s"}\\n' "$5" "$5"; elif [ "$6" = review ]; then printf 'content %s ' "$5" >> '${callLog}'; cat "\${10}" >> '${callLog}'; printf ' ' >> '${callLog}'; cat "\${11}" >> '${callLog}'; printf '\\n' >> '${callLog}'; printf '{"outcome":"applied"}\\n'; elif [ "$6" = status ]; then printf '{"outcome":"applied"}\\n'; fi\n`);
  chmodSync(minosBin, 0o755);
  const verdictPath = join(root, "verdict.json");
  writeFileSync(verdictPath, JSON.stringify({
    status: "complete",
    members: [
      { id: "primary", owner: "minos-e2e-owner", repo: "subject", number: 1, head: "reviewed-head-1", target: "reviewed-target-1" },
      { id: "sibling", owner: "minos-e2e-owner", repo: "sibling", number: 2, head: "reviewed-head-2", target: "reviewed-target-2" },
    ],
    memberReviews: [
      { member: "primary", verdict: "comment", body: "Primary body.", comments: [{ body: "primary finding" }], status: "attention" },
      { member: "sibling", verdict: "comment", body: "Sibling body.", comments: [{ body: "sibling finding" }], status: "attention" },
    ],
  }));
  execFileSync(process.execPath, [publishMembersScriptPath, verdictPath, "--terminal"], {
    env: { ...process.env, MINOS_BIN: minosBin },
    stdio: "pipe",
  });
  const calls = readFileSync(callLog, "utf8").trim().split("\n");
  assert.deepEqual(calls.filter((call) => !call.startsWith("content ")).map((call) => call.split(" ").slice(0, 6).join(" ")), [
    "forge --member minos-e2e-owner subject 1 review",
    "forge --member minos-e2e-owner subject 1 status",
    "forge --member minos-e2e-owner sibling 2 review",
    "forge --member minos-e2e-owner sibling 2 status",
  ]);
  assert.ok(calls.some((call) => call.includes("review reviewed-head-1 reviewed-target-1 request-changes")));
  assert.ok(calls.some((call) => call.includes("status reviewed-head-1 reviewed-target-1 attention")));
  assert.ok(calls.some((call) => call.includes("review reviewed-head-2 reviewed-target-2 request-changes")));
  assert.ok(calls.some((call) => call.includes("status reviewed-head-2 reviewed-target-2 attention")));
  assert.ok(calls.includes('content 1 Primary body. [{"body":"primary finding"}]'));
  assert.ok(calls.includes('content 2 Sibling body. [{"body":"sibling finding"}]'));
});

test("terminal member publication writes every clean review before finishing status", () => {
  const root = mkdtempSync(join(tmpdir(), "minos-clean-member-publication-"));
  const callLog = join(root, "calls.log");
  const minosBin = join(root, "minos-fixture");
  writeFileSync(minosBin, `#!/bin/sh\nprintf '%s\\n' "$*" >> '${callLog}'\nif [ "$6" = snapshot ]; then printf '{"head_sha":"head-%s","target_sha":"target-%s"}\\n' "$5" "$5"; elif [ "$6" = review ]; then printf 'content %s ' "$5" >> '${callLog}'; cat "\${10}" >> '${callLog}'; printf ' ' >> '${callLog}'; cat "\${11}" >> '${callLog}'; printf '\\n' >> '${callLog}'; printf '{"outcome":"applied"}\\n'; elif [ "$6" = status ]; then printf '{"outcome":"applied"}\\n'; fi\n`);
  chmodSync(minosBin, 0o755);
  const verdictPath = join(root, "verdict.json");
  writeFileSync(verdictPath, JSON.stringify({
    status: "complete",
    members: [
      { id: "primary", owner: "minos-e2e-owner", repo: "subject", number: 1, head: "reviewed-head-1", target: "reviewed-target-1" },
      { id: "sibling", owner: "minos-e2e-owner", repo: "sibling", number: 2, head: "reviewed-head-2", target: "reviewed-target-2" },
    ],
    memberReviews: [
      { member: "primary", verdict: "comment", body: "No confirmed findings in the reviewed code.", comments: [], status: "clean" },
      { member: "sibling", verdict: "comment", body: "No confirmed findings in the reviewed code.", comments: [], status: "clean" },
    ],
  }));
  execFileSync(process.execPath, [publishMembersScriptPath, verdictPath, "--terminal"], {
    env: { ...process.env, MINOS_BIN: minosBin },
    stdio: "pipe",
  });
  const calls = readFileSync(callLog, "utf8").trim().split("\n");
  assert.deepEqual(calls.filter((call) => !call.startsWith("content ")).map((call) => call.split(" ").slice(0, 6).join(" ")), [
    "forge --member minos-e2e-owner subject 1 review",
    "forge --member minos-e2e-owner sibling 2 review",
  ]);
  assert.ok(calls.some((call) => call.includes("review reviewed-head-1 reviewed-target-1 comment")));
  assert.ok(calls.some((call) => call.includes("review reviewed-head-2 reviewed-target-2 comment")));
  assert.equal(calls.some((call) => call.includes(" status ")), false, "clean member completion waits for finishing");
  assert.ok(calls.includes("content 1 No confirmed findings in the reviewed code. []"));
  assert.ok(calls.includes("content 2 No confirmed findings in the reviewed code. []"));
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
        pinnedModel: "gpt-5.6-terra",
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
    assert.equal(verdict.reviewBody, null);
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
    id: "specialist-1-correctness-gpt:observation:1",
    source: "correctness concern logic",
    member: "primary",
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
  const verifiers = calls.filter((call) => call.opts.label?.startsWith("verify-"));
  assert.deepEqual([correctness.opts.engine, correctness.opts.model], ["codex", "gpt-5.6-terra"]);
  assert.deepEqual([security.opts.engine, security.opts.model], ["codex", "gpt-5.6-terra"]);
  assert.ok(verifiers.length > 0);
  assert.ok(verifiers.every((call) => call.opts.engine === "claude" && call.opts.model === "claude-opus-5"));
  assert.ok(specialistCalls(calls).every((call) => call.opts.engine !== "claude"));
  assert.ok(calls.filter((call) => call.opts.label?.startsWith("verify-"))
    .every((call) => call.opts.engine !== "codex"));
});

test("a verifier observation reaches the result alongside its verdicts", async () => {
  const { result, calls } = await runScript(ARGS, responder({
    verify: (label, prompt) => ({
      ...verifierResult(prompt),
      outOfScopeObservations: [observation({ member: "primary", title: "residual defect the verdict cannot carry" })],
    }),
  }));
  assert.deepEqual(result.outOfScopeObservations, [{
    id: "verify-1-claude:observation:1",
    source: "verification",
    member: "primary",
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
    ["member", "title", "path", "line", "explanation"],
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
  assert.deepEqual(verdict.memberReviews, [{
    member: "primary",
    verdict: "comment",
    body: "No confirmed findings in the reviewed code.",
    comments: [],
    status: "clean",
  }]);
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
    "specialist-1-correctness-gpt", "specialist-2-security-gpt", "specialist-3-testing-gpt",
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

test("review inputs refuse a member entry without forge coordinates", () => {
  const root = mkdtempSync(join(tmpdir(), "minos-invalid-member-input-"));
  const membersPath = join(root, "members.json");
  writeFileSync(membersPath, JSON.stringify({ members: [{ id: "primary", owner: "minos-e2e-owner" }] }));
  const result = spawnSync(process.execPath, [inputScriptPath, "target", "head", "--members", membersPath], {
    encoding: "utf8",
    env: { ...process.env, MINOS_ORIENTATION: "unused" },
  });
  assert.equal(result.status, 2);
  assert.equal(result.stdout, "");
  assert.match(result.stderr, /review members need non-empty ids and forge coordinates/);
  assert.match(result.stderr, /usage: node workflows\/review-inputs\.mjs/);
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

test("grouped runs keep the forge record per member and split on a blocking outcome", () => {
  assert.match(lifecycle, /the forge record is per\s+member/);
  assert.match(lifecycle, /every finding is published only on the member it\s+concerns/);
  assert.match(lifecycle, /membership is fixed\s+from this point/);
  assert.match(lifecycle, /no line count, file count, or member count decides it/);
  assert.match(lifecycle, /forge claim-member OWNER REPO NUMBER/);
  assert.match(lifecycle, /memberHeads[\s\S]*forge --member` snapshot[\s\S]*every non-primary member/);
  assert.match(lifecycle, /list exactly matches its guarded membership and every head is unchanged/);
  assert.match(lifecycle, /--members "\$MINOS_RUN_DIR\/members\.json"/);
  assert.match(
    lifecycle,
    /REVIEW_MEMBERS_ARGS=\(\)[\s\S]*if \[ -f "\$MINOS_RUN_DIR\/members\.json" \]; then[\s\S]*REVIEW_MEMBERS_ARGS=\(--members "\$MINOS_RUN_DIR\/members\.json"\)/,
  );
  assert.match(
    lifecycle,
    /publication-before-fix operation[\s\S]*redirects its\s+one review write through `publish-member-reviews\.mjs`[\s\S]*former\s+primary-only review call/,
  );
  assert.match(lifecycle, /A solo member takes\s+this same publication path/);
  assert.match(lifecycle, /does not write a terminal-shaped `Minos`[\s\S]*while the loop is still working/);
  assert.match(lifecycle, /blocking member's `attention` status/);
  assert.match(lifecycle, /`requestChangesReview` is present on a legacy verdict without `members`/);
  assert.match(lifecycle, /member-shaped verdict[\s\S]*do not post a second[\s\S]*primary-only request-changes review/);
  assert.match(
    lifecycle,
    /`integration\.memberFixReviews`[\s\S]*one entry for each member in[\s\S]*`integration\.memberCommits`[\s\S]*absent, duplicate, or additional member is[\s\S]*a fault[\s\S]*forge --member OWNER REPO NUMBER comment MEMBER_HEAD[\s\S]*MEMBER_TARGET FIX_REVIEW_FILE[\s\S]*Never post the aggregate `fixReview` on a[\s\S]*grouped run/,
  );
  assert.match(
    lifecycle,
    /a blocking outcome splits the group[\s\S]*siblings in any order, a stacked chain base-first[\s\S]*affects no other member/,
  );
  assert.match(
    lifecycle,
    /approve-chain-wait BODY_FILE[\s\S]*cause\s+`chain-wait`[\s\S]*target SHA this verdict was rendered\s+against[\s\S]*status HEAD TARGET held[\s\S]*Held at: finishing/,
  );
});

test("the maintenance playbook is fixed, screened, and runs no review workflow", () => {
  assert.match(maintenance, /No\s+review workflow runs at any point in this lifecycle\./);
  assert.match(maintenance, /never a\s+work order/);
  assert.match(maintenance, /at least seven days old[\s\S]*adopted without\s+ceremony/);
  assert.match(
    maintenance,
    /flagged young release is never adopted\*\*: pin it back to the aged\s+release/,
  );
  assert.match(maintenance, /never reach the branch unscreened/);
  assert.match(maintenance, /one patch level[\s\S]*declares no version of its own, skip this step/);
  assert.match(maintenance, /no 👍 is ever added/);
  assert.match(maintenance, /integrate-wave/);
  assert.match(maintenance, /request-changes-checks\s+BODY_FILE COMMENTS_FILE/);
});

test("the lifecycle uses one adjudicated review call and publication-owned fix waves", () => {
  assert.match(lifecycle, /refused setup if the admitted head moved through a foreign commit before setup/);
  assert.match(
    lifecycle,
    /mainline pull request[\s\S]*pushed a completed target reconciliation merge[\s\S]*made that merge `\$MINOS_HEAD_SHA`/,
  );
  assert.match(
    lifecycle,
    /fork or AGit[\s\S]*reconciliation stays local[\s\S]*`\$MINOS_HEAD_SHA` remains the[\s\S]*admitted head/,
  );
  assert.doesNotMatch(lifecycle, /unpushable|separate publication worktree|`publication` field/);
  assert.match(
    lifecycle,
    /\*\*Carried result:\*\*[\s\S]*carried-review-result\.json` exists[\s\S]*complete predecessor verdict[\s\S]*Consume the one-time carry with this command:[\s\S]*mv "\$MINOS_RUN_DIR\/carried-review-result\.json"[\s\S]*Do not invoke the review workflow for this first review/,
  );
  assert.match(
    lifecycle,
    /\*\*Stale carry:\*\*[\s\S]*complete\s+predecessor verdict for an earlier head[\s\S]*merge-base --is-ancestor[\s\S]*any foreign commit in the\s+range ends the reuse/,
  );
  assert.match(
    lifecycle,
    /serves repair only, never publication or a\s+terminal stand[\s\S]*--carried-from REVIEWED_HEAD REVIEWED_TARGET[\s\S]*fresh verdict —\s+never the stale one/,
  );
  assert.match(
    lifecycle,
    /classification is `terminal`, the\s+stale verdict cannot stand[\s\S]*run the review workflow fresh/,
  );
  assert.match(
    lifecycle,
    /repair push it cannot match\s+exactly[\s\S]*mv "\$MINOS_RUN_DIR\/carried-review-result\.json"\s+"\$MINOS_RUN_DIR\/stale-review-result\.json"/,
  );
  assert.match(
    lifecycle,
    /\*\*No carried result:\*\*[\s\S]*invoke the adjudication[\s\S]*review\.js[\s\S]*> "\$MINOS_RUN_DIR\/review-result\.json"/,
  );
  assert.match(lifecycle, /invoke the adjudication\s+wrapper once and save its verdict:/);
  assert.match(
    lifecycle,
    /An ordinary `review-result\.json` is never a carry signal[\s\S]*After a fix wave,[\s\S]*invoke the workflow again for the fresh head/,
  );
  assert.doesNotMatch(lifecycle, /If `\$MINOS_RUN_DIR\/review-result\.json` (?:already )?exists/);
  assert.match(lifecycle, /invoke the adjudication[\s\S]*review\.js[\s\S]*--json-args/i);
  assert.match(lifecycle, /review-inputs\.mjs[\s\S]*--loop-record "\$MINOS_LOOP_RECORD"/);
  assert.match(
    lifecycle,
    /`outOfScopeObservations`[\s\S]*?unverified observations, not findings[\s\S]*?terminal filing[\s\S]*?out-of-scope-observation[\s\S]*?never enter a review, fix input, sweep\s+decision, or run outcome/,
  );
  assert.match(
    lifecycle,
    /fix result may also carry `outOfScopeObservations`[\s\S]*?Keep them with the saved result[\s\S]*?terminal filing publishes only[\s\S]*?current complete adjudicated review or brief verdict[\s\S]*?not observations from a fix result/,
  );
  assert.match(lifecycle, /--digest[\s\S]*sweep-digest\.json/);
  assert.match(
    lifecycle,
    /Classifying the sweep is your judgement[\s\S]*informed by the threshold rather than mechanically bound to it/,
  );
  assert.match(
    lifecycle,
    /new findings all sit below the threshold is\s+`terminal` by default[\s\S]*only exception is severity disagreement/,
  );
  assert.match(lifecycle, /minos-sweep-decision-v1[\s\S]*--decision[\s\S]*fix-args\.json/);
  assert.match(lifecycle, /publish-before-fix[\s\S]*fix-args\.json[\s\S]*HEAD TARGET/);
  assert.match(lifecycle, /starts the effectful `fix\.js`[\s\S]*only after[\s\S]*`outcome: "applied"`/);
  assert.match(lifecycle, /Do not post the terminal sweep's sub-threshold[\s\S]*findings/);
  assert.match(
    lifecycle,
    /`overflow` array unchanged[\s\S]*`outOfScopeObservations`[\s\S]*`misconfigurations`[\s\S]*review-brief-misconfiguration[\s\S]*do not add them to[\s\S]*sweep digest[\s\S]*requestChangesReview[\s\S]*fix input[\s\S]*PUBLICATION_FILE[\s\S]*Out-of-scope observation[\s\S]*Review brief misconfiguration[\s\S]*failure never fails the run/,
  );
  assert.match(
    lifecycle,
    /complete brief verdict's `outOfScopeObservations` and `misconfigurations`[\s\S]*member-aware\s+`publish-overflow\.mjs` block from step 6[\s\S]*including `--members[\s\S]*whenever that record exists[\s\S]*same\s+per-member handling[\s\S]*Do not mix either[\s\S]*channel into the brief review, `briefFixRequired`, or the single-wave fix\s+input[\s\S]*failure degrades presentation and[\s\S]*never changes the brief-stage outcome/,
  );
  assert.doesNotMatch(lifecycle, /workflowProgress|resumeFromRunId|--workflow-script|briefReview/);
  assert.match(
    lifecycle,
    /shipped `rootcause\.js`[\s\S]*`codex` \/ `gpt-5\.6-sol`[\s\S]*isolation: "worktree"[\s\S]*configured Minos identity[\s\S]*never\s+push/,
  );
  assert.doesNotMatch(lifecycle, /(?:write|construct)(?: the)? `rootcause\.js`/i);
  assert.match(lifecycle, /helper returns a licensed non-empty commit[\s\S]*integrate-wave/);
});

test("configured command failures enter the repair discipline, never a review on a red head", () => {
  const initialGate = lifecycle.slice(lifecycle.indexOf("3. Read"), lifecycle.indexOf("4. Run"));
  assert.match(
    initialGate,
    /\$\{MINOS_REVIEW_WORKFLOW%\/\*\}\/completion-policy\.mjs[\s\S]*JSON `status` field[\s\S]*gate repair discipline[\s\S]*No review starts while the gate is red/,
  );
  assert.match(
    initialGate,
    /ENSEMBLE_STATUS_DIR="\$MINOS_RUN_DIR"[\s\S]*ENSEMBLE_RUN_RECORD=on[\s\S]*rootcause\.js/,
  );
  assert.match(initialGate, /when it is empty, no test command is\s+configured, so skip it/);
  assert.match(
    initialGate,
    /build-command-output\.log[\s\S]*test-command-output\.log[\s\S]*shipped `rootcause\.js`[\s\S]*matching absolute `build-command-output\.log` or\s+`test-command-output\.log`[\s\S]*--command FAILING_COMMAND --exit-status EXIT_STATUS[\s\S]*--evidence CAPTURED_OUTPUT_FILE[\s\S]*gate-repair-result\.json[\s\S]*background task[\s\S]*integrate-wave[\s\S]*run the exact configured\s+commands again[\s\S]*complete combined output[\s\S]*preserving[\s\S]*exit status[\s\S]*overwriting its output file on every\s+execution/,
  );
  assert.doesNotMatch(initialGate, / &$/m);
  assert.match(
    initialGate,
    /fork pull request[\s\S]*skip the\s+repair dispatch/,
  );
  assert.match(
    initialGate,
    /bounded by progress, not a count[\s\S]*the red result\s+that repair was dispatched against[\s\S]*never the one that first entered the discipline[\s\S]*earns another repair\s+dispatch[\s\S]*same command failing the same way is a stall[\s\S]*your judgement[\s\S]*never a string comparison of gate\s+output[\s\S]*append its basis as one\s+sentence to `\$MINOS_RUN_DIR\/gate-repair-ladder\.log`[\s\S]*A counted ceiling sits behind the\s+progress rule[\s\S]*\$MINOS_MAX_GATE_REPAIRS[\s\S]*Record the running count[\s\S]*reaches the\s+ceiling[\s\S]*end the run as\s+\*\*attention\*\*[\s\S]*repair budget ran\s+out/,
  );
  assert.match(
    initialGate,
    /top of every gate-repair-ladder iteration/,
  );
  assert.match(initialGate, /before preparing or[\s\S]*dispatching the next repair/);
  assert.match(initialGate, /memory-pressure[\s\S]*pressure-boundary handoff/);
  assert.match(initialGate, /gateRepairLadder[\s\S]*authoritative\s+progress record/);
  assert.match(initialGate, /do not poll for pressure while[\s\S]*workflow or forge write[\s\S]*is in flight/);
  const pressureCheckpoint = initialGate.indexOf("At the top of every gate-repair-ladder iteration");
  const repairDispatch = initialGate.indexOf("rootcause-inputs.mjs");
  assert.ok(pressureCheckpoint >= 0, "the pressure checkpoint anchor must be present");
  assert.ok(repairDispatch >= 0, "the repair dispatch anchor must be present");
  assert.ok(
    pressureCheckpoint < repairDispatch,
    "the pressure checkpoint must precede the repair dispatch",
  );
  assert.match(
    initialGate,
    /When the ladder stalls[\s\S]*ends as \*\*attention\*\*,\s+never incomplete[\s\S]*`\$MINOS_FAILURE_LOG`[\s\S]*request-changes-checks[\s\S]*no claimed diagnosis beyond what\s+those attempts proved[\s\S]*status HEAD TARGET attention[\s\S]*reaction-remove HEAD TARGET eyes[\s\S]*non-clean terminal marker[\s\S]*records that failed required checks caused the verdict[\s\S]*target its\s+gate actually ran against[\s\S]*never a newer target you judged equivalent[\s\S]*target movement spends it/,
  );
  assert.match(
    initialGate,
    /Where this discipline applies, `incomplete` remains the\s+outcome only for genuine inability\s+to assess/,
  );
  assert.match(
    initialGate,
    /reports that bound structurally in its `cause` verdict —\s+`side`, `determinism` and `locus`[\s\S]*branches on those fields rather than on the\s+diagnosis prose/,
  );
  assert.match(
    initialGate,
    /cause proven\s+`intermittent` with locus `test-expectation` is repaired on the\s+pull-request branch and rides it to merge[\s\S]*deliberately narrow[\s\S]*`deterministic` target-side cause stays out of\s+scope[\s\S]*reverts the target's work at merge[\s\S]*`product` locus stays out\s+because the intermittent test is the only witness to a real race and\s+silencing it ships the bug/,
  );
  assert.match(
    initialGate,
    /One empty-commit result is not a stall: a `cause` verdict whose `side`\s+is `target`[\s\S]*findings-caused terminal review would block re-attempts\s+until the head moves[\s\S]*check-caused form is spent by target movement on\s+a non-fork[\s\S]*End the run as held instead[\s\S]*forge status HEAD\s+TARGET held[\s\S]*bound to the current target/,
  );
  assert.match(
    initialGate,
    /flake fix rides the pull request to merge[\s\S]*ends held only\s+when the repair could not be delivered[\s\S]*solely in files\s+that exist on the target side[\s\S]*`side` is `unproven` is not a\s+target-side answer at all: it is a stall/,
  );
  assert.match(
    initialGate,
    /opening with the line `Held at:\s+review` — the durable record of the stretch this hold interrupted/,
  );
  assert.match(
    initialGate,
    /With annexe grounding,\s+also file the proven diagnosis into the reviewed project's annexe[\s\S]*publish-overflow\.mjs[\s\S]*With repository grounding the held comment already sits on\s+the project's own surface[\s\S]*presentation-class: its failure degrades and never fails the run/,
  );
  assert.match(
    initialGate,
    /forge\s+target-broken TARGET REASON[\s\S]*every sibling reconciling\s+with that same commit will reach this same conclusion[\s\S]*binds\s+to the target commit exactly as the held status does[\s\S]*a target that\s+moves carries no marker[\s\S]*Write it only from this paragraph[\s\S]*never from the stall or\s+infrastructure endings[\s\S]*presentation-class/,
  );
  assert.match(
    initialGate,
    /`side` of `infrastructure` at this gate[\s\S]*excludes the\s+reconciled tree[\s\S]*answered locally[\s\S]*run the exact configured commands again[\s\S]*green re-run continues exactly as a repaired gate would[\s\S]*same-failure judgement finds unchanged spends the exclusion[\s\S]*genuinely different failure is\s+progress[\s\S]*fresh-run push remedy belongs to finishing's forge\s+checks, never to this local gate/,
  );
  assert.match(
    initialGate,
    /Before reading any other result field, read `status`\. A result whose\s+`status` is `incomplete` is a failed dispatch, not an assessed verdict[\s\S]*the `incomplete` outcome, never a stall's attention[\s\S]*integrate a\s+commit only when the verdict licenses one[\s\S]*target-side `intermittent`\/`test-expectation` exception[\s\S]*breaches the helper's contract/,
  );
  assert.match(
    lifecycle,
    /\*\*Resuming a released hold\.\*\*[\s\S]*Recognition is forge state\s+alone[\s\S]*only work the forge\s+witnesses complete may be skipped[\s\S]*stage value is `finishing`[\s\S]*do not re-run the engagement gate, the\s+review workflow, the fix loop, or the brief stage[\s\S]*continue directly at step 8's finishing/,
  );

  const repeatedGate = lifecycle.slice(lifecycle.indexOf("After the push"), lifecycle.indexOf("6. A `terminal`"));
  assert.match(
    repeatedGate,
    /exact configured build and test commands again,[\s\S]*complete\s+combined output[\s\S]*preserving each command's exit status[\s\S]*overwriting its output file on every execution/,
  );
  assert.match(
    repeatedGate,
    /\$\{MINOS_REVIEW_WORKFLOW%\/\*\}\/completion-policy\.mjs[\s\S]*JSON `status` field[\s\S]*empty strings are skipped[\s\S]*step 3's gate repair discipline[\s\S]*same progress bound[\s\S]*same command failing the same way ends\s+the run as attention[\s\S]*no further review round or merge action happens on the red head[\s\S]*Only after both gates pass or skip[\s\S]*adjudication-wrapper/,
  );

  const briefWave = lifecycle.slice(lifecycle.indexOf("7. Once the main loop"), lifecycle.indexOf("8. Enter finishing"));
  assert.match(
    briefWave,
    /capture their complete combined output[\s\S]*preserve each\s+command's exit status[\s\S]*overwrite its output file on every execution[\s\S]*red build or test\s+result after the wave enters step 3's gate repair discipline[\s\S]*re-run the\s+configured commands with the same explicit capture, exit-status\s+preservation, and overwrite requirements, under the same progress bound[\s\S]*same command failing the same way ends\s+the run as attention/,
  );
});

test("a verified finishing repair merges in the same attempt", () => {
  const finishing = lifecycle.slice(lifecycle.indexOf("8. Enter finishing"), lifecycle.indexOf("9. Keep"));
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
    /mainline pull request[\s\S]*script's pushed head[\s\S]*fork or AGit pull request[\s\S]*`local-only` outcome as\s+success[\s\S]*snapshot head to remain the fetched head[\s\S]*no\s+pushed head exists[\s\S]*exact configured build and test commands[\s\S]*nothing downstream pushes it/,
  );
  assert.match(
    finishing,
    /forge check-logs HEAD TARGET[\s\S]*check-logs\.json[\s\S]*Invoke the same shipped `rootcause\.js`/,
  );
  assert.match(
    finishing,
    /Build and tests are this repair's verification[\s\S]*merge proceeds in\s+the same attempt[\s\S]*no re-review and no successor run[\s\S]*continue to\s+step 9/,
  );
  assert.doesNotMatch(finishing, /classify-finishing-change|tests-only|review-required|FINISHING_REVIEWED_HEAD/);
  assert.match(
    finishing,
    /same shipped `rootcause\.js`[\s\S]*forge check evidence rather[\s\S]*configured-command output files[\s\S]*--evidence "\$MINOS_RUN_DIR\/check-logs\.json"[\s\S]*rootcause-result\.json/,
  );
  assert.doesNotMatch(finishing, /say so in the helper prompt/);
  assert.match(finishing, /background task\s+under step 4's workflow discipline[\s\S]*empty `runs` array[\s\S]*do not\s+invent/);
  assert.doesNotMatch(finishing, /in the foreground/);
  assert.match(
    finishing,
    /helper's `writeUp`[\s\S]*post[\s\S]*one `comment` review/,
  );
  assert.match(
    finishing,
    /Never carry a failing repair to\s+merge/,
  );
  assert.match(
    finishing,
    /same progress bound as every gate\s+repair[\s\S]*the failure this repair was dispatched against[\s\S]*appending each comparison's one-sentence basis exactly as step 3\s+directs[\s\S]*stall and ends the run as \*\*attention\*\*[\s\S]*assessed unsuccessful repair, not an\s+inability to assess[\s\S]*failed\s+integration or publication is infrastructure[\s\S]*stops incomplete/,
  );
  assert.match(
    finishing,
    /helper returns no commit and no pushed head appears[\s\S]*made\s+no mutation[\s\S]*On the red-check path, branch on the `cause` verdict[\s\S]*`side` of `target` ends the\s+run as held[\s\S]*Any other `side`, `unproven` included, is a stall[\s\S]*end as attention[\s\S]*Only on the label-only[\s\S]*step 9 receive the unchanged verified head\s+and target/,
  );
  assert.match(
    finishing,
    /`intermittent` with locus\s+`test-expectation` — returns a commit, so it leaves by the integration\s+path above[\s\S]*undeliverable through the pull-request\s+branch, and ends the run held with the rest/,
  );
  assert.match(
    finishing,
    /target-side comment opening with `Held at: finishing` \(the review\s+stages concluded clean for this pull request and the hold interrupted\s+finishing[\s\S]*file the diagnosis with `kind` `held-diagnosis` to the\s+reviewed project's annexe exactly as step 3's held stop directs/,
  );
  assert.match(
    finishing,
    /`side` of `infrastructure`[\s\S]*excludes the reconciled\s+tree, even where the infrastructure cause itself stays unproven[\s\S]*exactly one fresh check run[\s\S]*rides a real push,\s+because the forge cannot re-run an existing check[\s\S]*run `sync-target` again exactly\s+as this step began[\s\S]*sync triggers no\s+re-review, and the merge proceeds in this same attempt[\s\S]*target is unmoved there is nothing to push: end\s+the run as \*\*held\*\*[\s\S]*`severity` is `infrastructure`[\s\S]*published review stands for that successor's resume/,
  );
  assert.match(
    finishing,
    /fork pull request neither arm exists[\s\S]*honest request-changes report and ends as attention[\s\S]*exclusion earns one fresh run, never a ladder[\s\S]*same-failure judgement \(as\s+at every gate\) finds the same failure, the diagnosis is spent[\s\S]*never answer a spent exclusion with another fresh run[\s\S]*successor admitted from that hold receives the predecessor's exclusion\s+diagnosis with its release recognition[\s\S]*dispatches the repair rather than re-earning a fresh run/,
  );
  assert.match(
    finishing,
    /Take a fresh\s+`"\$MINOS_BIN" forge snapshot` now — the last one predates the helper's\s+whole dispatch/,
  );
  assert.match(
    finishing,
    /Before reading any other result field, read `status` exactly as step 3\s+directs[\s\S]*only a licensed commit\s+\(`side` `pull-request`, or the target flake exception\) is integrated/,
  );
  assert.match(
    finishing,
    /step-2 dispatch exists and its background task has not completed, first\s+wait for it through its `flake-repair-result\.done` flag[\s\S]*overwrites the\s+evidence files that task reads/,
  );
  assert.match(
    finishing,
    /Retain the `Flaky Test` label on that path, whatever the\s+verdict says: required checks are green there[\s\S]*not a reason to withhold a pull request the forge considers passing/,
  );
  assert.match(
    finishing,
    /One\s+passing run does not decide that second judgement[\s\S]*a green sample is not proof it is gone[\s\S]*leave the label on when the account does not carry it/,
  );
  assert.match(
    finishing,
    /re-dispatch carries the evidence its own failure produced[\s\S]*local failure[\s\S]*`--command`, `--exit-status`[\s\S]*captured[\s\S]*`build-command-output\.log` or `test-command-output\.log`[\s\S]*required check red on the forge carries a\s+fresh `check-logs\.json`[\s\S]*exact current head and target[\s\S]*Never hand a helper stale forge evidence[\s\S]*`rootcause-inputs\.mjs` executes either shape\s+unchanged/,
  );
});

test("head movement is judged by authorship and a foreign push takes the absorb judgement", () => {
  assert.match(
    lifecycle,
    /\*\*Branch movement, run-wide\.\*\*[\s\S]*run's own\s+pushes[\s\S]*spend\s+nothing and invalidate nothing[\s\S]*any commit this run did not push is foreign[\s\S]*even when the run's own commits arrive alongside or after\s+it[\s\S]*judgement, not automatic\s+invalidation[\s\S]*read\s+their actual diff[\s\S]*every review stage still ahead judge\s+it as part of the change[\s\S]*durable pull-request comment[\s\S]*naming\s+the absorbed commits and the judgement's basis[\s\S]*ends the run for a fresh successor at the new head[\s\S]*stays eligible[\s\S]*no size threshold or fixed policy decides it/,
  );
  assert.match(
    lifecycle,
    /Movement of the \*target\* is not this case and never invalidates a live\s+run, whatever its size or author[\s\S]*reconcile with the current target\s+at finishing/,
  );
  assert.match(
    lifecycle,
    /a target that differs from the one\s+setup established, carry on: target movement never invalidates the run/,
  );
  assert.match(
    lifecycle,
    /the head moved, classify it by the run-wide movement discipline[\s\S]*solely of commits this run pushed is the run's own motion[\s\S]*continue finishing on it[\s\S]*absorbed change is synced in and verified by\s+the exact configured build and test commands before any further finishing\s+action[\s\S]*durable absorb comment[\s\S]*fresh\s+successor exactly as the discipline directs, publishing nothing for\s+unverified code/,
  );
  assert.doesNotMatch(lifecycle, /unexpected head moved/);
});

test("the 👍 is awarded only at step 9, once checks pass, and retracted when a check turns red", () => {
  const merging = lifecycle.slice(lifecycle.indexOf("9. Keep"));
  assert.match(
    merging,
    /Once checks pass, add the 👍 with `"\$MINOS_BIN" forge reaction HEAD\s+TARGET \+1`/,
  );
  assert.match(
    merging,
    /check that turns red during this readiness wait\s+re-enters the repair exactly as above[\s\S]*remove it with `"\$MINOS_BIN" forge reaction-remove HEAD TARGET\s+\+1` before dispatching[\s\S]*re-add it only when checks pass again/,
  );
  // Every earlier stage — the main loop, the brief stage, finishing's
  // repairs — must award no 👍: it means ready to merge, nothing less.
  assert.doesNotMatch(lifecycle.slice(0, lifecycle.indexOf("9. Keep")), /\+1|add the 👍/);
});

test("a check going red at step 9 re-enters the finishing repair under the progress bound", () => {
  const merging = lifecycle.slice(lifecycle.indexOf("9. Keep"));
  assert.match(
    merging,
    /`failed_checks`\s+turns non-empty while waiting[\s\S]*not a readiness wait[\s\S]*re-enter step 8's root-cause\s+helper on that fresh evidence[\s\S]*fresh `check-logs\.json` for\s+the exact current head and target[\s\S]*same progress bound[\s\S]*gate-repair-ladder\.log[\s\S]*same check failing the same way is a stall[\s\S]*\*\*attention\*\*[\s\S]*honest request-changes report[\s\S]*`target` verdict the helper did not repair ends the run as \*\*held\*\*[\s\S]*repaired\s+target-side flake returns with its commit like any other/,
  );
  assert.match(
    merging,
    /verified repair from that re-entry returns here through step 8's\s+ordinary continuation on its fresh head[\s\S]*no counted\s+ceiling[\s\S]*hard timeout being the failsafe/,
  );
});

test("timings are recorded by scripts and the run report cites them, never hand-written", () => {
  assert.match(lifecycle, /time-on-exit" "\$MINOS_RUN_DIR\/timings\.ndjson"/);
  assert.match(lifecycle, /timing wrapper outermost/);
  assert.match(lifecycle, /\$MINOS_RUN_DIR\/report\.md/);
  assert.match(lifecycle, /own estimates/);
  assert.match(lifecycle, /killed result for the same stage/);
});
