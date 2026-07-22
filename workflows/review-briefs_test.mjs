import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

const scriptPath = fileURLToPath(new URL("./review-briefs.js", import.meta.url));
const inputScriptPath = fileURLToPath(new URL("./review-brief-inputs.mjs", import.meta.url));
const source = await readFile(scriptPath, "utf8");
const body = source.replace(/^export const meta =/m, "const meta =");
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
const script = new AsyncFunction("agent", "parallel", "pipeline", "phase", "log", "args", body);

function brief(path, content, overrides = {}) {
  const inner = path.replace(/^\.review\//, "");
  const scope = inner.includes("/") ? inner.replace(/\/[^/]*$/, "") : null;
  return { path, readPath: `/workspace/${path}`, content, scope, scopeExists: true, ...overrides };
}

function finding(overrides = {}) {
  return {
    title: "repository defect",
    severity: "High",
    confidence: 84,
    path: "pkg/x.go",
    line: 7,
    explanation: "the repository concern is violated",
    ...overrides,
  };
}

function specialistResult(findings = [finding()], applicability = { status: "applicable", reason: "the concern applies" }) {
  return { applicability, findings };
}

function args(overrides = {}) {
  return {
    target: "target111",
    head: "head222",
    occasion: null,
    workspace: "/workspace",
    hasReviewDirectory: true,
    changedPaths: ["pkg/x.go"],
    trackedFiles: [{ path: "pkg/x.go", bytes: 100 }],
    briefs: [brief(".review/pkg/errors.md", "---\nrelevance: Error-path changes.\n---\nJudge errors.")],
    guidance: { grounding: "repository", path: "/workspace/AGENTS.md", content: "PROJECT_GUIDANCE_TEAL" },
    instructionBriefs: [
      { path: "workflows/review-briefs/repository.md", readPath: "/minos/workflows/review-briefs/repository.md", content: "MINOS_REPOSITORY_BRIEF_V1" },
      { path: "workflows/review-briefs/verifier.md", readPath: "/minos/workflows/review-briefs/verifier.md", content: "MINOS_ADVERSARIAL_VERIFIER_V1" },
    ],
    ...overrides,
  };
}

function responder({ relevance, specialist, verify } = {}) {
  return (label, prompt, opts) => {
    if (label === "brief-relevance") {
      if (relevance) return relevance(label, prompt, opts);
      const concerns = JSON.parse(prompt.match(/Concerns: (\[[^\n]+\])/)[1]);
      return { decisions: concerns.map((entry) => ({ brief: entry.brief, applicable: true, reason: "relevant" })) };
    }
    if (label.startsWith("verify-"))
      return verify ? verify(label, prompt, opts) : { verdict: "upheld", confidence: 92, reason: "confirmed" };
    if (specialist) return specialist(label, prompt, opts);
    return specialistResult();
  };
}

async function run(input, respond = responder()) {
  const calls = [];
  const agent = async (prompt, opts = {}) => {
    calls.push({ prompt, opts });
    return respond(opts.label || "", prompt, opts);
  };
  const parallel = async (thunks) => Promise.all(thunks.map((thunk) => thunk().catch(() => null)));
  const result = await script(agent, parallel, async () => [], () => {}, () => {}, input);
  return { result, calls };
}

test("an absent .review directory emits a complete-stage envelope with no legs", async () => {
  const { result, calls } = await run(args({ hasReviewDirectory: false, briefs: [] }));
  assert.equal(result.stage, "absent");
  assert.deepEqual(result.requiredModelEvidence, []);
  assert.deepEqual(result.proposedFindings, []);
  assert.equal(calls.length, 0);
});

test("a root brief with no run condition is skipped without dispatch", async () => {
  const candidate = brief(".review/no-condition.md", "# No condition\nJudge something.");
  const { result, calls } = await run(args({ briefs: [candidate] }));
  assert.equal(calls.length, 0);
  assert.deepEqual(result.briefs, [{
    brief: candidate.path,
    title: "No Condition",
    status: "skipped",
    skipKind: "no-condition",
    reason: "brief has no relevance, occasion, path scope, or full-extent condition",
  }]);
});

test("relevance remains an explicit Terra leg and can skip a concern", async () => {
  const candidate = brief(".review/errors.md", "---\nrelevance: Error-path changes.\n---\nJudge errors.");
  const { result, calls } = await run(args({ briefs: [candidate] }), responder({
    relevance: () => ({ decisions: [{ brief: candidate.path, applicable: false, reason: "no error path changed" }] }),
  }));
  assert.deepEqual(result.requiredModelEvidence, [{
    label: "brief-relevance",
    role: "relevance",
    expectedFamily: "gpt",
    pinnedModel: "gpt-5.6-terra",
  }]);
  assert.equal(result.briefs[0].skipKind, "relevance");
  assert.deepEqual([calls[0].opts.engine, calls[0].opts.model], ["codex", "gpt-5.6-terra"]);
});

test("occasion, nested path scope, and full extent are positive run signals", async () => {
  const candidates = [
    brief(".review/occasion.md", "---\noccasion: release\n---\nJudge release.") ,
    brief(".review/pkg/scoped.md", "# Scoped\nJudge package."),
    brief(".review/full.md", "---\nextent: full\nsweep: whole-tree\n---\nJudge repository."),
  ];
  const { result } = await run(args({ briefs: candidates, occasion: "release" }), responder({ specialist: () => specialistResult([]) }));
  assert.equal(result.dispatches.length, 3);
  assert.ok(result.briefs.every((entry) => entry.status === "run"));
});

test("per-file chunking is lossless beyond forty files", async () => {
  const trackedFiles = Array.from({ length: 90 }, (_, index) => ({ path: `pkg/f${index}.go`, bytes: 10 }));
  const candidate = brief(".review/full.md", "---\nextent: full\nsweep: per-file\n---\nJudge every file.");
  const { result } = await run(args({ briefs: [candidate], trackedFiles }), responder({ specialist: () => specialistResult([]) }));
  assert.deepEqual(result.dispatches.map((entry) => entry.files.length), [40, 40, 10]);
  assert.equal(result.dispatches.flatMap((entry) => entry.files).length, 90);
});

test("all repository specialists dispatch beyond the former twenty-four limit", async () => {
  const briefs = Array.from({ length: 25 }, (_, index) =>
    brief(`.review/pkg/concern-${index + 1}.md`, `# Concern ${index + 1}\nJudge it.`));
  const { result, calls } = await run(args({ briefs }), responder({ specialist: () => specialistResult([]) }));
  assert.equal(result.dispatches.length, 25);
  assert.equal(calls.filter((call) => call.opts.label?.startsWith("repository-")).length, 25);
  assert.ok(result.briefs.every((entry) => entry.status === "run"));
});

test("a large whole-tree brief dispatches instead of becoming not-run", async () => {
  const trackedFiles = Array.from({ length: 400 }, (_, index) => ({ path: `pkg/f${index}.go`, bytes: 4_000 }));
  const candidate = brief(".review/full.md", "---\nextent: full\nsweep: whole-tree\n---\nJudge repository.");
  const { result } = await run(args({ briefs: [candidate], trackedFiles }), responder({ specialist: () => specialistResult([]) }));
  assert.equal(result.dispatches.length, 1);
  assert.equal(result.dispatches[0].files.length, 400);
  assert.equal(result.briefs[0].status, "run");
});

test("specialist inapplicability is recorded and never reaches verification", async () => {
  const { result, calls } = await run(args(), responder({
    specialist: () => specialistResult(
      [finding({ title: "false finding" })],
      { status: "inapplicable", reason: "the diff gives this concern nothing to judge" },
    ),
  }));
  assert.equal(result.proposedFindings.length, 0);
  assert.equal(calls.filter((call) => call.opts.label?.startsWith("verify-")).length, 0);
  assert.equal(result.briefs.find((entry) => entry.skipKind === "inapplicable").reason, "the diff gives this concern nothing to judge");
});

test("an applicable finding emits opposite-family routing and raw verifier output", async () => {
  const { result, calls } = await run(args());
  const specialist = calls.find((call) => call.opts.label?.startsWith("repository-"));
  const verifier = calls.find((call) => call.opts.label?.startsWith("verify-"));
  assert.deepEqual([specialist.opts.engine, specialist.opts.model], ["claude", "claude-opus-4-8"]);
  assert.deepEqual([verifier.opts.engine, verifier.opts.model], ["codex", "gpt-5.6-sol"]);
  assert.deepEqual(result.proposedFindings[0].rawVerifier, { verdict: "upheld", confidence: 92, reason: "confirmed" });
  assert.equal(result.requiredModelEvidence.length, 3);
});

test("every repository-brief finding reaches verification beyond the former bound", async () => {
  const { result, calls } = await run(args(), responder({
    specialist: () => specialistResult([
      finding({ title: "one" }),
      finding({ title: "two", line: 8 }),
      finding({ title: "three survives", severity: "Medium", line: 9 }),
      finding({ title: "four survives", severity: "Low", line: 10 }),
    ]),
  }));
  assert.deepEqual(result.proposedFindings.map((entry) => entry.title), ["one", "two", "three survives", "four survives"]);
  const beyondBoundVerifier = calls.find((call) => call.opts.label === "verify-brief-1-4-gpt");
  assert.ok(beyondBoundVerifier);
  assert.match(beyondBoundVerifier.prompt, /"title":"four survives"/);
});

test("a missing specialist result is a not-run disposition with a required archive leg", async () => {
  const { result } = await run(args(), responder({ specialist: () => null }));
  assert.equal(result.briefs.find((entry) => entry.status === "not-run").reason, "specialist returned no result");
  assert.ok(result.requiredModelEvidence.some((leg) => leg.role === "specialist"));
});

test("the enumerator emits large deterministic input directly as JSON and rejects generated-script mode", () => {
  const root = mkdtempSync(join(tmpdir(), "minos-brief-input-"));
  execFileSync("git", ["init", "-q", root]);
  execFileSync("git", ["-C", root, "config", "user.name", "Fixture"]);
  execFileSync("git", ["-C", root, "config", "user.email", "fixture@example.test"]);
  writeFileSync(join(root, "AGENTS.md"), `GUIDANCE_BOUNDARY_${"g".repeat(110_000)}`);
  writeFileSync(join(root, "code.txt"), "before\n");
  execFileSync("git", ["-C", root, "add", "."]);
  execFileSync("git", ["-C", root, "commit", "-qm", "base"]);
  const target = execFileSync("git", ["-C", root, "rev-parse", "HEAD"], { encoding: "utf8" }).trim();
  mkdirSync(join(root, ".review"));
  writeFileSync(join(root, ".review", "boundary.md"), `---\nrelevance: Boundary.\n---\n${"b".repeat(60_000)}`);
  writeFileSync(join(root, "code.txt"), "after\n");
  execFileSync("git", ["-C", root, "add", "."]);
  execFileSync("git", ["-C", root, "commit", "-qm", "head"]);
  const head = execFileSync("git", ["-C", root, "rev-parse", "HEAD"], { encoding: "utf8" }).trim();
  const orientationPath = join(root, "orientation.json");
  writeFileSync(orientationPath, JSON.stringify({ repository: root, grounding: "repository", guidance: join(root, "AGENTS.md") }));
  const env = { ...process.env, MINOS_ORIENTATION: orientationPath, MINOS_WORKSPACE: root };
  const deterministicJson = execFileSync(process.execPath, [inputScriptPath, target, head], { encoding: "utf8", env });
  const enumerated = JSON.parse(deterministicJson);
  assert.ok(Buffer.byteLength(deterministicJson) > 147_000);
  assert.equal(enumerated.briefs[0].path, ".review/boundary.md");
  assert.throws(
    () => execFileSync(process.execPath, [inputScriptPath, target, head, "--workflow-script", join(root, "generated.js")], {
      encoding: "utf8", env, stdio: ["ignore", "pipe", "pipe"],
    }),
    /usage:/,
  );
});
