import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const scriptPath = fileURLToPath(new URL("./review-briefs.js", import.meta.url));
const inputScriptPath = fileURLToPath(new URL("./review-brief-inputs.mjs", import.meta.url));
const lifecycle = await readFile(fileURLToPath(new URL("../lifecycle/lifecycle.md", import.meta.url)), "utf8");
const source = await readFile(scriptPath, "utf8");
const body = source.replace(/^export const meta =/m, "const meta =");
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
const script = new AsyncFunction("agent", "parallel", "pipeline", "phase", "log", "args", body);

const instructionPaths = ["repository.md", "verifier.md", "fixer.md"];
const instructionBriefs = instructionPaths.map((name) => {
  const readPath = fileURLToPath(new URL(`./review-briefs/${name}`, import.meta.url));
  return { path: `workflows/review-briefs/${name}`, readPath, content: readFileSync(readPath, "utf8") };
});

function brief(path, frontmatter = "", scopeExists = true) {
  const content = frontmatter ? `---\n${frontmatter}\n---\nJudge the concern.` : "Judge the concern.";
  const inner = path.replace(/^\.review\//, "");
  const scope = inner.includes("/") ? inner.replace(/\/[^/]*$/, "") : null;
  return { path, readPath: `/workspace/${path}`, content, scope, scopeExists };
}

function args(overrides = {}) {
  return {
    target: "aaa111",
    head: "bbb222",
    occasion: null,
    workspace: "/workspace",
    hasReviewDirectory: true,
    changedPaths: ["internal/x.go"],
    trackedFiles: [{ path: "internal/x.go", bytes: 100 }],
    briefs: [brief(".review/error-tone.md", "title: Error Tone")],
    guidance: { grounding: "annexe", path: "/annexe/README.md", content: "COMMISSION_CERULEAN_719" },
    instructionBriefs,
    ...overrides,
  };
}

function finding(overrides = {}) {
  return {
    title: "misleading error",
    severity: "High",
    confidence: 88,
    path: "internal/x.go",
    line: 3,
    explanation: "the message contradicts the failure",
    ...overrides,
  };
}

function responder({ applicable = true, specialist = { findings: [] }, verify } = {}) {
  return (label) => {
    if (label === "brief-relevance") return {
      decisions: [{ brief: ".review/error-tone.md", applicable, reason: applicable ? "the change touches errors" : "version-only change" }],
    };
    if (label.startsWith("verify-")) return verify || { verdict: "upheld", confidence: 92, reason: "reproduced" };
    if (label.startsWith("repository-")) return typeof specialist === "function" ? specialist(label) : specialist;
    throw new Error(`unexpected agent ${label}`);
  };
}

async function run(input, respond = responder()) {
  const calls = [];
  const agent = async (prompt, options) => {
    calls.push({ prompt, options });
    return respond(options.label, prompt, options);
  };
  const parallel = async (thunks) => Promise.all(thunks.map((thunk) => thunk().catch(() => null)));
  const result = await script(agent, parallel, async () => [], () => {}, () => {}, input);
  return { result, calls };
}

function runRecordFor(result, overrides = {}) {
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
  };
}

async function runRecorded(input, respond = responder(), overrides = {}) {
  const first = await run(input, respond);
  return run({ ...input, runRecord: runRecordFor(first.result, overrides) }, respond);
}

test("the main sweep source contains no repository-brief dispatch machinery", async () => {
  const mainReview = await readFile(fileURLToPath(new URL("./review.js", import.meta.url)), "utf8");
  assert.doesNotMatch(mainReview, /repositorySpecialist|repositoryUnit|\.review\/ Markdown briefs|repository-/);
});

test("no .review directory is a complete no-op with no agent dispatch", async () => {
  const { result, calls } = await run(args({ hasReviewDirectory: false, briefs: [] }));
  assert.equal(result.status, "complete");
  assert.equal(result.stage, "absent");
  assert.equal(result.briefReview, null);
  assert.equal(calls.length, 0);
});

test("relevance excludes a concern conservatively and records a skip without review prose", async () => {
  const input = args({ briefs: [brief(".review/error-tone.md", "title: Error Tone\nrelevance: Changes error messages or error paths.")] });
  const { result, calls } = await runRecorded(input, responder({ applicable: false }));
  assert.equal(result.status, "complete");
  assert.equal(result.briefs[0].status, "skipped");
  assert.equal(result.briefs[0].skipKind, "relevance");
  assert.equal(result.briefReview, null);
  assert.deepEqual(calls.map((call) => call.options.label), ["brief-relevance"]);
});

test("a missing relevance decision is not-run and withholds the review group", async () => {
  const input = args({ briefs: [brief(".review/error-tone.md", "title: Error Tone\nrelevance: Changes error messages or error paths.")] });
  const { result, calls } = await run(input, (label) => {
    if (label === "brief-relevance") return { decisions: [] };
    throw new Error(`unexpected agent ${label}`);
  });
  assert.equal(result.status, "incomplete");
  assert.equal(result.briefs[0].status, "not-run");
  assert.match(result.briefs[0].reason, /relevance judgement returned no decision/);
  assert.equal(result.briefReview, null);
  assert.deepEqual(calls.map((call) => call.options.label), ["brief-relevance"]);
});

test("positive relevance dispatches exactly the applicable concern", async () => {
  const relevant = brief(".review/error-tone.md", "title: Error Tone\nrelevance: Changes error messages or error paths.");
  const excluded = brief(".review/docs-tone.md", "title: Docs Tone\noccasion: release");
  const { result, calls } = await runRecorded(args({ briefs: [relevant, excluded] }), responder());
  assert.equal(result.status, "complete");
  assert.deepEqual(result.dispatches.map((entry) => entry.brief), [".review/error-tone.md"]);
  assert.equal(calls.filter((call) => call.options.label.startsWith("repository-")).length, 1);
  assert.equal(result.briefs.find((entry) => entry.brief === excluded.path).status, "skipped");
});

test("project guidance reaches relevance, repository specialist, and verifier prompts after the stage split", async () => {
  const marker = "MINOS_ESTATE_BRIEF_GUIDANCE_VERMILION_719";
  const input = args({
    briefs: [brief(".review/error-tone.md", "title: Error Tone\nrelevance: Changes error messages or error paths.")],
    guidance: { grounding: "annexe", path: "/annexe/README.md", content: marker },
  });
  const { calls } = await runRecorded(input, responder({ specialist: { findings: [finding()] } }));
  const relevant = calls.filter((call) =>
    call.options.label === "brief-relevance" ||
    call.options.label.startsWith("repository-") ||
    call.options.label.startsWith("verify-brief-"));
  assert.equal(relevant.length, 3);
  assert.ok(relevant.every((call) => call.prompt.includes(marker)));
});

test("brief paths keep specialist labels unique when display titles collide", async () => {
  const first = brief(".review/api/tone.md", "title: Tone", true);
  const second = brief(".review/ui/tone.md", "title: Tone", true);
  const { result } = await run(args({
    changedPaths: ["api/a.go", "ui/a.go"],
    briefs: [first, second],
    trackedFiles: [{ path: "api/a.go", bytes: 1 }, { path: "ui/a.go", bytes: 1 }],
  }), responder());
  assert.equal(new Set(result.dispatches.map((entry) => entry.label)).size, 2);
});

test("occasion and diff-scope skips happen before specialist dispatch", async () => {
  const release = brief(".review/release.md", "occasion: release");
  const scoped = brief(".review/internal/api/limits.md");
  const skipped = await run(args({ changedPaths: ["cmd/main.go"], briefs: [release, scoped] }));
  assert.ok(skipped.result.briefs.every((entry) => entry.status === "skipped"));
  assert.equal(skipped.calls.length, 0);

  const matched = await run(args({ occasion: "release", briefs: [release] }), responder());
  assert.equal(matched.calls.filter((call) => call.options.label.startsWith("repository-")).length, 1);
});

test("a ghost scope falls back repo-wide and records its warning", async () => {
  const ghost = brief(".review/ghost/limits.md", "", false);
  const { result, calls } = await runRecorded(args({ briefs: [ghost] }), responder());
  assert.equal(result.briefs[0].status, "run");
  assert.match(result.briefs[0].warning, /matches no repository directory/);
  assert.match(calls.find((call) => call.options.label.startsWith("repository-")).prompt, /Assigned scope: the whole repository/);
});

const tracked = (count, bytes = 0) => Array.from({ length: count }, (_, index) => ({ path: `pkg/f${index}.go`, bytes: Math.floor(bytes / count) }));

test("full per-file scopes shard while whole-tree scopes remain one assignment", async () => {
  const perFile = await run(args({ briefs: [brief(".review/pkg/style.md", "extent: full")], trackedFiles: tracked(90) }), responder());
  assert.equal(perFile.result.dispatches.length, 3);
  const whole = await run(args({ briefs: [brief(".review/pkg/style.md", "extent: full\nsweep: whole-tree")], trackedFiles: tracked(90) }), responder());
  assert.equal(whole.result.dispatches.length, 1);
});

test("weighted whole-tree budget runs tiny trees and fails closed on genuinely large scopes", async () => {
  const tiny = await runRecorded(args({
    briefs: [brief(".review/pkg/style.md", "extent: full\nsweep: whole-tree")],
    trackedFiles: tracked(61, 610),
  }), responder());
  assert.equal(tiny.result.briefs[0].status, "run");

  const many = await run(args({
    briefs: [brief(".review/pkg/style.md", "extent: full\nsweep: whole-tree")],
    trackedFiles: tracked(250, 25_000),
  }), responder());
  assert.equal(many.result.briefs[0].status, "not-run");
  assert.equal(many.result.briefs[0].readingVolume, 525_000);
  assert.equal(many.result.status, "incomplete");
});

test("brief findings cross to GPT verification and form their own review group", async () => {
  const respond = responder({ specialist: { findings: [finding()] } });
  const { result } = await runRecorded(args(), respond);
  assert.equal(result.status, "complete");
  assert.equal(result.findings[0].verify.expectedFamily, "gpt");
  assert.equal(result.confirmedFindings[0].verifierConfidence, 92);
  assert.equal(result.briefReview.verdict, "comment");
  assert.match(result.briefReview.body, /Repository review brief findings/);
  assert.equal(result.fixRequired, true);
});

test("Claude model-id decoration is confirmed by family", async () => {
  const first = await run(args());
  const specialist = first.result.requiredModelEvidence.find((leg) => leg.role === "specialist");
  const decoratedModel = `${specialist.pinnedModel}[1m]`;
  const { result } = await run({ ...args(), runRecord: runRecordFor(first.result, { [specialist.label]: { model: decoratedModel } }) });
  const evidence = result.modelEvidence.find((leg) => leg.label === specialist.label);
  assert.equal(result.status, "complete");
  assert.equal(evidence.actualModel, decoratedModel);
  assert.equal(evidence.confirmed, true);
});

test("opposite-family fallback model makes the brief review incomplete", async () => {
  const first = await run(args());
  const specialist = first.result.requiredModelEvidence.find((leg) => leg.role === "specialist");
  const { result } = await run({ ...args(), runRecord: runRecordFor(first.result, { [specialist.label]: { fallbackModel: "gpt-5.6-sol" } }) });
  const evidence = result.modelEvidence.find((leg) => leg.label === specialist.label);
  assert.equal(result.status, "incomplete");
  assert.equal(evidence.actualModel, "gpt-5.6-sol");
  assert.equal(evidence.confirmed, false);
  assert.deepEqual(result.incomplete, [`actual model for ${specialist.label} was not confirmed as claude`]);
});

test("same-family fallback model confirms the verifier leg", async () => {
  const respond = responder({ specialist: { findings: [finding()] } });
  const first = await run(args(), respond);
  const verifier = first.result.requiredModelEvidence.find((leg) => leg.role === "verifier");
  const { result } = await run({ ...args(), runRecord: runRecordFor(first.result, { [verifier.label]: { fallbackModel: "gpt-5.6-sol" } }) }, respond);
  const evidence = result.modelEvidence.find((leg) => leg.label === verifier.label);
  assert.equal(result.status, "complete");
  assert.equal(evidence.actualModel, "gpt-5.6-sol");
  assert.equal(evidence.confirmed, true);
});

test("wrong-family verifier evidence withholds the brief review", async () => {
  const respond = responder({ specialist: { findings: [finding()] } });
  const first = await run(args(), respond);
  const verifier = first.result.requiredModelEvidence.find((leg) => leg.role === "verifier");
  const { result } = await run({ ...args(), runRecord: runRecordFor(first.result, { [verifier.label]: { model: "claude-opus-4-8" } }) }, respond);
  assert.equal(result.status, "incomplete");
  assert.equal(result.findings[0].verdict, "no-verdict");
  assert.equal(result.briefReview, null);
});

test("the enumerator reads briefs and inventories from the reviewed repository", () => {
  const root = mkdtempSync(join(tmpdir(), "minos-brief-enumerator-"));
  execFileSync("git", ["init", "-q", root]);
  execFileSync("git", ["-C", root, "config", "user.name", "Fixture"]);
  execFileSync("git", ["-C", root, "config", "user.email", "fixture@example.test"]);
  writeFileSync(join(root, "AGENTS.md"), "PROJECT GUIDANCE");
  writeFileSync(join(root, "code.txt"), "before\n");
  execFileSync("git", ["-C", root, "add", "."]);
  execFileSync("git", ["-C", root, "commit", "-qm", "base"]);
  const target = execFileSync("git", ["-C", root, "rev-parse", "HEAD"], { encoding: "utf8" }).trim();
  mkdirSync(join(root, ".review"));
  writeFileSync(join(root, ".review", "errors.md"), "---\nrelevance: Error-path changes.\n---\nJudge errors.\n");
  writeFileSync(join(root, "code.txt"), "after\n");
  execFileSync("git", ["-C", root, "add", "."]);
  execFileSync("git", ["-C", root, "commit", "-qm", "head"]);
  const head = execFileSync("git", ["-C", root, "rev-parse", "HEAD"], { encoding: "utf8" }).trim();
  const orientationPath = join(root, "orientation.json");
  writeFileSync(orientationPath, JSON.stringify({ repository: root, grounding: "repository", guidance: join(root, "AGENTS.md") }));
  const enumerated = JSON.parse(execFileSync(process.execPath, [inputScriptPath, target, head], {
    encoding: "utf8",
    env: { ...process.env, MINOS_ORIENTATION: orientationPath, MINOS_WORKSPACE: root },
  }));
  assert.equal(enumerated.hasReviewDirectory, true);
  assert.equal(enumerated.briefs[0].path, ".review/errors.md");
  assert.match(enumerated.briefs[0].content, /relevance: Error-path changes/);
  assert.ok(enumerated.changedPaths.includes("code.txt"));
  assert.ok(enumerated.trackedFiles.some((entry) => entry.path === "code.txt" && entry.bytes > 0));
});

test("lifecycle gives the reaction to absent, clean, and fixed-and-tested brief journeys without an all-clear comment", () => {
  assert.match(lifecycle, /hasReviewDirectory` is false[\s\S]*forge reaction[\s\S]*\+1/);
  assert.match(lifecycle, /`fixRequired` is false[\s\S]*forge reaction[\s\S]*\+1/);
  assert.match(lifecycle, /all fixes were[\s\S]*integrated[\s\S]*build and tests pass[\s\S]*add the 👍/);
  assert.match(lifecycle, /Do not run another review loop/);
  assert.match(lifecycle, /never publish an all-clear comment/);
});

test("finishing uses one trusted snapshot for sync, bounded waiting, checks, labels, and merge", () => {
  assert.match(lifecycle, /sync-target[\s\S]*do not dispatch another review/);
  assert.match(lifecycle, /watch-snapshot[\s\S]*head_sha[\s\S]*target_sha[\s\S]*statuses[\s\S]*labels/);
  assert.match(lifecycle, /timeout[\s\S]*returned[\s\S]*fresh snapshot/);
  assert.match(lifecycle, /failed_checks[\s\S]*exact `Flaky Test`[\s\S]*root-cause[\s\S]*anthropic-gpt-5\.6-sol[\s\S]*high effort/);
  assert.match(lifecycle, /label-remove[\s\S]*Flaky Test/);
});

test("terminal finishing merges the exact head, cleans writable branches, preserves forks, and removes eyes", () => {
  assert.match(lifecycle, /forge merge HEAD[\s\S]*guarded merge binds the exact head/);
  assert.match(lifecycle, /delete-source-branch[\s\S]*Never try to delete a fork branch/);
  assert.match(lifecycle, /Merged,[\s\S]*request-changes[\s\S]*incomplete[\s\S]*end 👀-absent/);
  assert.match(lifecycle, /reaction-remove[\s\S]*eyes/);
});
