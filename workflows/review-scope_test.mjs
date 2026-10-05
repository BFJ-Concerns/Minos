import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, renameSync, rmSync, writeFileSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { briefEngagement } from "./brief-dispositions.mjs";
import { executableVerdict as wrapperVerdict } from "./executable-verdict-fixture.mjs";
import { resolveRouting } from "./role-routing.mjs";
import { verdictDigest } from "./verdict-classification.mjs";
import { adjudicateEnvelope } from "./archive-fixture.mjs";

const pairedRouting = resolveRouting({ provisioned: ["claude", "codex"] });

const workflowsDir = dirname(fileURLToPath(import.meta.url));
const scriptPath = join(workflowsDir, "review-scope.js");
const inputScriptPath = join(workflowsDir, "review-scope-inputs.mjs");
function executableVerdict(t, envelope) {
  return wrapperVerdict(t, envelope, { namespace: "scope", workflow: "review-scope.js" });
}
const source = await readFile(scriptPath, "utf8");
const lifecycle = await readFile(join(workflowsDir, "..", "lifecycle", "lifecycle.md"), "utf8");
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
const script = new AsyncFunction(
  "agent", "parallel", "pipeline", "phase", "log", "args",
  source.replace(/^export const meta =/m, "const meta ="),
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
    guidance: [{ repository: null, path: "README.md", origin: "checked-in", content: "SCOPE_TEST_COMMISSION_TEAL" }],
    routing: pairedRouting,
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

// --- input builder ---

function enumeratedArgs({ briefs = [], occasion, reviewDirectory = true, rename = false, numstatFault = null } = {}) {
  const root = mkdtempSync(join(tmpdir(), "minos-scope-inputs-"));
  try {
    const workspace = join(root, "workspace");
    mkdirSync(workspace);
    const env = rename || numstatFault
      ? Object.fromEntries(Object.entries(process.env).filter(([name]) => !/^(MINOS_|ENSEMBLE_)/.test(name)))
      : process.env;
    const git = (...args) => execFileSync("git", ["-C", workspace, ...args], {
      encoding: "utf8",
      env: {
        ...env,
        GIT_AUTHOR_NAME: "Scope Fixture",
        GIT_AUTHOR_EMAIL: "scope@example.invalid",
        GIT_COMMITTER_NAME: "Scope Fixture",
        GIT_COMMITTER_EMAIL: "scope@example.invalid",
      },
      stdio: ["ignore", "pipe", "pipe"],
    });
    git("init", "--quiet", "--initial-branch=main");
    mkdirSync(join(workspace, "pkg"));
    const before = rename ? "package a\n" + "// retained line\n".repeat(19) : "package a\n";
    writeFileSync(join(workspace, "pkg", "a.go"), before);
    mkdirSync(join(workspace, "deploy"));
    writeFileSync(join(workspace, "deploy", "notes.txt"), "untouched\n");
    writeFileSync(join(workspace, "README.md"), "MINOS_SCOPE_COMMISSION_CORAL");
    git("add", "--all");
    git("commit", "--quiet", "-m", "base");
    const target = git("rev-parse", "HEAD").trim();
    if (rename) renameSync(join(workspace, "pkg", "a.go"), join(workspace, "pkg", "renamed.go"));
    writeFileSync(join(workspace, "pkg", rename ? "renamed.go" : "a.go"), before + "\nfunc A() {}\n");
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
      guidance: [{ source: { path: "README.md" }, location: join(workspace, "README.md"), origin: "checked-in" }],
      misconfigurations: [],
    }));
    const cliArgs = [inputScriptPath, target, head];
    if (occasion !== undefined) cliArgs.push(occasion);
    const cliEnvironment = { ...env, MINOS_ORIENTATION: orientationPath, MINOS_WORKSPACE: workspace, MINOS_PROVISIONED_ENGINES: "claude codex", MINOS_ROUTING: "" };
    if (numstatFault) {
      // Corrupt the real Git transport at its boundary; the builder must
      // refuse the response instead of publishing a zero-count inventory.
      const realGit = execFileSync("sh", ["-c", "command -v git"], { encoding: "utf8", env }).trim();
      const bin = join(root, "bin");
      mkdirSync(bin);
      const quotedGit = "'" + realGit.replaceAll("'", "'\\''") + "'";
      const fault = numstatFault === "malformed"
        ? `${quotedGit} "$@" || exit $?\nprintf 'unparseable\\0'`
        : `${quotedGit} "$@" >/dev/null`;
      writeFileSync(join(bin, "git"), `#!/bin/sh\ncase " $* " in\n  *" --numstat "*) ${fault} ;;\n  *) exec ${quotedGit} "$@" ;;\nesac\n`, { mode: 0o755 });
      cliEnvironment.PATH = `${bin}:${env.PATH}`;
      return spawnSync(process.execPath, cliArgs, { encoding: "utf8", env: cliEnvironment });
    }
    const output = JSON.parse(execFileSync(process.execPath, cliArgs, {
      encoding: "utf8",
      env: cliEnvironment,
      stdio: ["ignore", "pipe", "pipe"],
    }));
    return output;
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
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
  assert.deepEqual(input.guidance, [{ repository: null, path: "README.md", origin: "checked-in", content: "MINOS_SCOPE_COMMISSION_CORAL" }]);
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


test("the scope inventory counts both sides of a renamed file", () => {
  const input = enumeratedArgs({ rename: true, reviewDirectory: false });
  assert.deepEqual(input.changedFiles, [
    { path: "pkg/a.go", added: 0, deleted: 20 },
    { path: "pkg/renamed.go", added: 22, deleted: 0 },
  ]);
});

test("the scope builder refuses an unparseable numstat record with usage exit 2", () => {
  const result = enumeratedArgs({ numstatFault: "malformed" });
  assert.equal(result.status, 2, result.stderr);
  assert.match(result.stderr, /unparseable git numstat record/);
  assert.equal(result.stdout, "");
});

test("the scope builder refuses an inventory path without numstat counts", () => {
  const result = enumeratedArgs({ numstatFault: "missing" });
  assert.equal(result.status, 2, result.stderr);
  assert.match(result.stderr, /git numstat omitted counts for pkg\/a.go/);
  assert.equal(result.stdout, "");
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
