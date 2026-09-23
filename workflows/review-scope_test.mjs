import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import test from "node:test";

import { briefEngagement } from "./brief-dispositions.mjs";
import { verdictDigest } from "./verdict-classification.mjs";
import { adjudicate } from "./run-record-adjudicator.mjs";

const workflowsDir = dirname(fileURLToPath(import.meta.url));
const scriptPath = join(workflowsDir, "review-scope.js");
const inputScriptPath = join(workflowsDir, "review-scope-inputs.mjs");
const wrapperPath = join(workflowsDir, "adjudicated-review");
const source = await readFile(scriptPath, "utf8");
const briefWorkflowSource = await readFile(join(workflowsDir, "review-briefs.js"), "utf8");
const lifecycle = await readFile(join(workflowsDir, "..", "lifecycle", "lifecycle.md"), "utf8");
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
const script = new AsyncFunction(
  "agent", "parallel", "pipeline", "phase", "log", "args",
  source.replace(/^export const meta =/m, "const meta ="),
);
const briefWorkflow = new AsyncFunction(
  "agent", "parallel", "pipeline", "phase", "log", "args",
  briefWorkflowSource.replace(/^export const meta =/m, "const meta ="),
);

async function runScript(args, respond) {
  const calls = [];
  const agent = async (prompt, opts = {}) => {
    calls.push({ prompt, opts });
    return respond(opts.label || "", prompt, opts);
  };
  const parallel = async (thunks) => Promise.all(thunks.map((thunk) => thunk().catch(() => null)));
  const result = await script(agent, parallel, async (items) => items, () => {}, () => {}, args);
  return { result, calls };
}

function fixtureBrief(path, content, { scope = null, scopeExists = true } = {}) {
  return { path, content, scope, scopeExists };
}

const noTriggerBrief = fixtureBrief(".review/prose.md", "Judge the prose.");
const occasionBrief = fixtureBrief(".review/release.md", "---\noccasion: release\n---\nJudge the release.");
const untouchedScopeBrief = fixtureBrief(".review/deploy/rollout.md", "Judge the rollout.", { scope: "deploy" });
const touchedScopeBrief = fixtureBrief(".review/pkg/layout.md", "Judge the layout.", { scope: "pkg" });
const relevanceBrief = fixtureBrief(".review/schema.md", "---\nrelevance: schema changes\n---\nJudge the schema.");
const misconfiguredBrief = fixtureBrief(".review/gone/tidy.md", "Judge tidiness.", { scope: "gone", scopeExists: false });

const settledBriefs = [noTriggerBrief, occasionBrief, untouchedScopeBrief, misconfiguredBrief];

function workflowArgs(overrides = {}) {
  const engagement = briefEngagement(settledBriefs, null, ["pkg/a.go"]);
  return {
    target: "aaa111",
    head: "bbb222",
    occasion: null,
    guidance: {
      grounding: "annexe",
      path: "/workspace/README.md",
      content: "SCOPE_TEST_COMMISSION_TEAL",
    },
    instructionBriefs: [{
      path: "workflows/review-briefs/scope.md",
      readPath: join(workflowsDir, "review-briefs", "scope.md"),
      content: "SCOPE_ROLE_BRIEF_TEAL",
    }],
    changedFiles: [{ path: "pkg/a.go", added: 4, deleted: 1 }],
    hasReviewDirectory: true,
    briefsEngage: engagement.briefsEngage,
    briefDispositions: engagement.dispositions,
    briefMisconfigurations: engagement.misconfigurations,
    ...overrides,
  };
}

function scopeResult(decision = "nothing-engages", reason = "documentation wording only; no concern has work") {
  return { decision, reason };
}

async function adjudicateEnvelope(t, envelope) {
  const recordDir = mkdtempSync(join(tmpdir(), "minos-scope-record-"));
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

// --- input builder ---

function enumeratedArgs({ briefs = [], occasion, reviewDirectory = true } = {}) {
  const root = mkdtempSync(join(tmpdir(), "minos-scope-inputs-"));
  const workspace = join(root, "workspace");
  mkdirSync(workspace);
  const git = (...args) => execFileSync("git", ["-C", workspace, ...args], {
    encoding: "utf8",
    env: {
      ...process.env,
      GIT_AUTHOR_NAME: "Scope Fixture",
      GIT_AUTHOR_EMAIL: "scope@example.invalid",
      GIT_COMMITTER_NAME: "Scope Fixture",
      GIT_COMMITTER_EMAIL: "scope@example.invalid",
    },
    stdio: ["ignore", "pipe", "pipe"],
  });
  git("init", "--quiet", "--initial-branch=main");
  mkdirSync(join(workspace, "pkg"));
  writeFileSync(join(workspace, "pkg", "a.go"), "package a\n");
  mkdirSync(join(workspace, "deploy"));
  writeFileSync(join(workspace, "deploy", "notes.txt"), "untouched\n");
  writeFileSync(join(workspace, "README.md"), "MINOS_SCOPE_COMMISSION_CORAL");
  git("add", "--all");
  git("commit", "--quiet", "-m", "base");
  const target = git("rev-parse", "HEAD").trim();
  writeFileSync(join(workspace, "pkg", "a.go"), "package a\n\nfunc A() {}\n");
  git("add", "--all");
  git("commit", "--quiet", "-m", "change");
  const head = git("rev-parse", "HEAD").trim();

  if (reviewDirectory) {
    mkdirSync(join(workspace, ".review"), { recursive: true });
    for (const brief of briefs) {
      const absolute = join(workspace, brief.path);
      mkdirSync(dirname(absolute), { recursive: true });
      writeFileSync(absolute, brief.content);
    }
  }
  const orientationPath = join(root, "orientation.json");
  writeFileSync(orientationPath, JSON.stringify({
    repository: workspace,
    grounding: "repository",
    guidance: join(workspace, "README.md"),
  }));
  const cliArgs = [inputScriptPath, target, head];
  if (occasion !== undefined) cliArgs.push(occasion);
  const output = JSON.parse(execFileSync(process.execPath, cliArgs, {
    encoding: "utf8",
    env: { ...process.env, MINOS_ORIENTATION: orientationPath, MINOS_WORKSPACE: workspace },
    stdio: ["ignore", "pipe", "pipe"],
  }));
  rmSync(root, { recursive: true, force: true });
  return output;
}

test("the input builder enumerates the diff and settles brief engagement deterministically", () => {
  const input = enumeratedArgs({
    briefs: [noTriggerBrief, occasionBrief, untouchedScopeBrief, touchedScopeBrief],
  });
  assert.equal(input.hasReviewDirectory, true);
  assert.deepEqual(input.changedFiles, [{ path: "pkg/a.go", added: 2, deleted: 0 }]);
  assert.equal(input.briefsEngage, true);
  const byBrief = new Map(input.briefDispositions.map((entry) => [entry.brief, entry]));
  assert.equal(byBrief.get(".review/prose.md").skipKind, "no-trigger");
  assert.equal(byBrief.get(".review/release.md").skipKind, "occasion");
  assert.equal(byBrief.get(".review/deploy/rollout.md").skipKind, "empty");
  assert.deepEqual(byBrief.get(".review/pkg/layout.md"), {
    brief: ".review/pkg/layout.md",
    title: "Layout",
    engaged: true,
    via: "path-scope",
  });
  assert.equal(input.guidance.content, "MINOS_SCOPE_COMMISSION_CORAL");
  assert.equal(input.instructionBriefs[0].path, "workflows/review-briefs/scope.md");
  assert.match(input.instructionBriefs[0].content, /nothing-engages/);
});

test("relevance-conditioned and misconfigured briefs settle conservatively", () => {
  const relevanceInput = enumeratedArgs({ briefs: [relevanceBrief] });
  assert.equal(relevanceInput.briefsEngage, true);
  assert.deepEqual(relevanceInput.briefDispositions, [{
    brief: ".review/schema.md",
    title: "Schema",
    engaged: true,
    via: "relevance-pending",
  }]);

  const misconfiguredInput = enumeratedArgs({ briefs: [misconfiguredBrief] });
  assert.equal(misconfiguredInput.briefsEngage, false);
  assert.equal(misconfiguredInput.briefDispositions[0].skipKind, "misconfigured-scope");
});

test("a repository without .review/ yields an idle engagement record", () => {
  const input = enumeratedArgs({ reviewDirectory: false });
  assert.equal(input.hasReviewDirectory, false);
  assert.equal(input.briefsEngage, false);
  assert.deepEqual(input.briefDispositions, []);
  assert.deepEqual(input.briefMisconfigurations, []);
});

test("a matched occasion engages the brief it opted in", () => {
  const input = enumeratedArgs({ briefs: [occasionBrief], occasion: "release" });
  assert.equal(input.briefsEngage, true);
  assert.deepEqual(input.briefDispositions, [{
    brief: ".review/release.md",
    title: "Release",
    engaged: true,
    via: "occasion",
  }]);
});

// --- disposition parity with the sandboxed brief workflow ---

test("the Node dispositions match the brief workflow's own deterministic pass", async () => {
  const briefs = [...settledBriefs, touchedScopeBrief].map((brief) => ({
    ...brief,
    readPath: `/workspace/${brief.path}`,
  }));
  const changedPaths = ["pkg/a.go"];
  const engagement = briefEngagement(briefs, null, changedPaths);

  const agent = async (_prompt, opts = {}) => {
    assert.match(String(opts.label), /^repository-/, "only the engaged brief dispatches a specialist");
    return {
      applicability: { status: "applicable", reason: "the assigned change exercises the concern" },
      findings: [],
      outOfScopeObservations: [],
    };
  };
  const parallel = async (thunks) => Promise.all(thunks.map((thunk) => thunk().catch(() => null)));
  const envelope = await briefWorkflow(agent, parallel, async (items) => items, () => {}, () => {}, {
    target: "aaa111",
    head: "bbb222",
    workspace: "/workspace",
    hasReviewDirectory: true,
    changedPaths,
    trackedFiles: changedPaths.map((path) => ({ path, bytes: 10 })),
    briefs,
    guidance: { grounding: "repository", path: "/workspace/AGENTS.md", content: "PARITY_GUIDANCE_CORAL" },
    instructionBriefs: [
      { path: "workflows/review-briefs/repository.md", readPath: "/x/repository.md", content: "R" },
      { path: "workflows/review-briefs/verifier.md", readPath: "/x/verifier.md", content: "V" },
    ],
  });

  const workflowSkips = envelope.briefs
    .filter((entry) => entry.status === "skipped")
    .map(({ brief, title, status, skipKind, reason }) => ({ brief, title, status, skipKind, reason }));
  const moduleSkips = engagement.dispositions.filter((entry) => entry.status === "skipped");
  assert.deepEqual(
    workflowSkips.sort((a, b) => a.brief.localeCompare(b.brief)),
    moduleSkips.sort((a, b) => a.brief.localeCompare(b.brief)),
  );
  assert.deepEqual(envelope.misconfigurations, engagement.misconfigurations);
  assert.deepEqual(
    envelope.dispatches.map((dispatch) => dispatch.brief),
    engagement.dispositions.filter((entry) => entry.engaged).map((entry) => entry.brief),
  );
});

// --- the gate workflow ---

test("nothing-engages returns a complete clean review envelope", async (t) => {
  const { result, calls } = await runScript(workflowArgs(), () => scopeResult());

  assert.deepEqual(Object.keys(result).sort(), [
    "briefs", "dispatches", "misconfigurations", "outOfScopeObservations", "proposedFindings",
    "requiredModelEvidence", "reviewed", "reviewers", "scopeDecision", "stage",
  ]);
  assert.equal(result.stage, "present");
  assert.deepEqual(result.requiredModelEvidence, [
    { label: "review-scope", role: "scope", pinnedModel: "gpt-6-sol" },
  ]);
  assert.deepEqual(result.scopeDecision, {
    status: "nothing-engages",
    reason: "documentation wording only; no concern has work",
  });
  assert.equal(result.briefs.length, 4);
  assert.ok(result.briefs.every((entry) => entry.status === "skipped"));
  assert.equal(calls.length, 1);
  assert.equal(calls[0].opts.engine, "codex");
  assert.equal(calls[0].opts.model, "gpt-6-sol");
  assert.equal(calls[0].opts.effort, "medium");
  assert.match(calls[0].prompt, /SCOPE_ROLE_BRIEF_TEAL/);
  assert.match(calls[0].prompt, /SCOPE_TEST_COMMISSION_TEAL/);
  assert.match(calls[0].prompt, /"pkg\/a\.go"/);

  const verdict = await adjudicateEnvelope(t, result);
  assert.equal(verdict.status, "complete", (verdict.incomplete || []).join("\n"));
  assert.deepEqual(verdict.confirmedFindings, []);
  assert.equal(verdict.skipped.length, 4);
});

test("review-required carries the leg's reason without changing the envelope shape", async () => {
  const { result } = await runScript(
    workflowArgs(),
    () => scopeResult("review-required", "the change rewires the spawn path"),
  );
  assert.deepEqual(result.scopeDecision, {
    status: "review-required",
    reason: "the change rewires the spawn path",
  });
  assert.equal(result.reviewers[0].status, "done");
});

test("an engaged brief answers review-required without spending a leg", async () => {
  const engagement = briefEngagement([...settledBriefs, touchedScopeBrief], null, ["pkg/a.go"]);
  const { result, calls } = await runScript(workflowArgs({
    briefsEngage: engagement.briefsEngage,
    briefDispositions: engagement.dispositions,
    briefMisconfigurations: engagement.misconfigurations,
  }), () => {
    throw new Error("no leg may dispatch when a brief engages");
  });
  assert.equal(calls.length, 0);
  assert.deepEqual(result.requiredModelEvidence, []);
  assert.deepEqual(result.scopeDecision, {
    status: "review-required",
    reason: "a repository review brief engages this change",
  });
});

test("a scope-leg null still emits its required leg and falls back to review-required", async () => {
  const { result } = await runScript(workflowArgs(), () => null);
  assert.deepEqual(result.requiredModelEvidence, [
    { label: "review-scope", role: "scope", pinnedModel: "gpt-6-sol" },
  ]);
  assert.deepEqual(result.reviewers, [{ label: "review-scope", role: "scope", status: "no-result" }]);
  assert.deepEqual(result.scopeDecision, {
    status: "review-required",
    reason: "scope leg returned no result",
  });
});

// --- the executable wrapper journey ---

const preloadSource = String.raw`
import childProcess from "node:child_process";
import { EventEmitter } from "node:events";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { syncBuiltinESMExports } from "node:module";
import { join } from "node:path";
import { PassThrough } from "node:stream";

childProcess.spawn = function recordedSpawn(_command, _arguments, options) {
  const envelope = JSON.parse(readFileSync(process.env.MINOS_SCOPE_ENVELOPE, "utf8"));
  const recordDir = options.env.ENSEMBLE_RUN_RECORD_DIR;
  writeFileSync(process.env.MINOS_SCOPE_RECORD, recordDir);
  const archive = join(recordDir, "runs", "cwd", "scope", "run");
  mkdirSync(join(archive, "agents"), { recursive: true });
  writeFileSync(
    join(archive, "manifest.json"),
    JSON.stringify({ kind: "run_manifest", status: "complete" }),
  );
  envelope.requiredModelEvidence.forEach((leg, index) => {
    const directory = join(archive, "agents", String(index + 1).padStart(6, "0"));
    mkdirSync(directory, { recursive: true });
    writeFileSync(
      join(directory, "agent.json"),
      JSON.stringify({ label: leg.label, status: "complete" }),
    );
  });

  const child = new EventEmitter();
  child.stdout = new PassThrough();
  child.stderr = new PassThrough();
  setImmediate(() => {
    child.stdout.end(JSON.stringify(envelope) + "\n");
    child.stderr.end();
    child.emit("close", 0, null);
  });
  return child;
};
syncBuiltinESMExports();
`;

function executableVerdict(t, envelope) {
  const root = mkdtempSync(join(tmpdir(), "minos-scope-wrapper-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const envelopePath = join(root, "envelope.json");
  const preloadPath = join(root, "record-ensemble.mjs");
  const recordPath = join(root, "record-dir.txt");
  const argsPath = join(root, "args.json");
  const workflowPath = join(root, "review-scope.js");
  writeFileSync(envelopePath, JSON.stringify(envelope));
  writeFileSync(preloadPath, preloadSource);
  writeFileSync(argsPath, "{}");
  writeFileSync(workflowPath, "return {};\n");

  const result = spawnSync(
    wrapperPath,
    [workflowPath, "--json-args", `@${argsPath}`],
    {
      cwd: workflowsDir,
      encoding: "utf8",
      env: {
        ...process.env,
        NODE_OPTIONS: `--import=${pathToFileURL(preloadPath).href}`,
        MINOS_SCOPE_ENVELOPE: envelopePath,
        MINOS_SCOPE_RECORD: recordPath,
      },
    },
  );
  assert.equal(result.status, 0, result.stderr);
  const recordDir = readFileSync(recordPath, "utf8");
  assert.equal(existsSync(recordDir), false, "the wrapper removes the consumed archive");
  return JSON.parse(result.stdout);
}

test("the wrapper attaches a valid scope decision to the adjudicated verdict", async (t) => {
  const { result } = await runScript(workflowArgs(), () => scopeResult());
  const verdict = executableVerdict(t, result);
  assert.equal(verdict.status, "complete", (verdict.incomplete || []).join("\n"));
  assert.deepEqual(verdict.scopeDecision, {
    status: "nothing-engages",
    reason: "documentation wording only; no concern has work",
  });
  assert.equal(verdict.skipped.length, 4);
  assert.deepEqual(verdict.confirmedFindings, []);

  const digest = verdictDigest({ review: verdict });
  assert.equal(digest.status, "complete");
  assert.equal(digest.thresholdIndication, "clean");
  assert.deepEqual(digest.findings, []);
  assert.deepEqual(digest.atOrAboveThresholdKeys, []);
});

test("verdicts for envelopes without a scope decision are unchanged", async (t) => {
  const { result } = await runScript(workflowArgs(), () => scopeResult());
  const { scopeDecision: omitted, ...plainEnvelope } = result;
  const verdict = executableVerdict(t, plainEnvelope);
  assert.equal(verdict.status, "complete", (verdict.incomplete || []).join("\n"));
  assert.equal("scopeDecision" in verdict, false);
});

test("a malformed scope decision is not attached", async (t) => {
  const { result } = await runScript(workflowArgs(), () => scopeResult());
  const verdict = executableVerdict(t, { ...result, scopeDecision: { status: "clean", reason: "x" } });
  assert.equal("scopeDecision" in verdict, false);
});

// --- lifecycle prose ---

test("the lifecycle runs the gate first and treats it as an optimisation, never a blocker", () => {
  assert.match(
    lifecycle,
    /Before the first review of a run[\s\S]*review-scope-inputs\.mjs[\s\S]*review-scope-args\.json/,
  );
  assert.match(lifecycle, /`briefsEngage`[\s\S]*skip\s+the gate workflow/);
  assert.match(lifecycle, /dispatch-stage" review-scope review-scope\.js/);
  assert.match(
    lifecycle,
    /`scopeDecision\.status` is `nothing-engages`[\s\S]*copy that\s+verdict to `\$MINOS_RUN_DIR\/review-result\.json`[\s\S]*without invoking the review workflow/,
  );
  assert.match(lifecycle, /The gate is an\s+optimisation, never a blocker/);
  assert.match(
    lifecycle,
    /engagement gate's\s+`nothing-engages` verdict[\s\S]*stage is already settled[\s\S]*nothing to run, judge, or publish/,
  );
});
