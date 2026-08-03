import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

import { attachBriefPartitionDetails } from "./brief-partition-details.mjs";
import { adjudicate } from "./run-record-adjudicator.mjs";

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

function observation(overrides = {}) {
  return {
    title: "pre-existing repository defect",
    path: "pkg/legacy.go",
    line: 11,
    explanation: "Unverified observation: unchanged code violates the repository concern.",
    ...overrides,
  };
}

function specialistResult(
  findings = [finding()],
  applicability = { status: "applicable", reason: "the concern applies" },
  outOfScopeObservations = [],
) {
  return { applicability, findings, outOfScopeObservations };
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

function responder({ relevance, partition, specialist, verify } = {}) {
  return (label, prompt, opts) => {
    if (label === "brief-relevance") {
      if (relevance) return relevance(label, prompt, opts);
      const concerns = JSON.parse(prompt.match(/Concerns: (\[[^\n]+\])/)[1]);
      return { decisions: concerns.map((entry) => ({ brief: entry.brief, applicable: true, reason: "relevant" })) };
    }
    if (label.startsWith("brief-partition-")) {
      if (partition) return partition(label, prompt, opts);
      const files = JSON.parse(prompt.match(/Assigned file inventory: (\[[^\n]+\])/)[1]);
      return { units: [{ id: "complete-scope", concern: "complete assigned scope", files }] };
    }
    if (label.startsWith("verify-")) {
      if (verify) return verify(label, prompt, opts);
      const findings = JSON.parse(prompt.match(/Findings: (\[[^\n]+\])/)[1]);
      return {
        verdicts: findings.map((entry) => ({
          findingId: entry.id,
          verdict: "upheld",
          confidence: 92,
          reason: "confirmed",
        })),
      };
    }
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

async function adjudicateEnvelope(t, envelope) {
  const recordDir = mkdtempSync(join(tmpdir(), "minos-brief-partition-adjudication-"));
  t.after(() => rmSync(recordDir, { recursive: true, force: true }));
  const archive = join(recordDir, "runs", "cwd", "namespace", "run");
  mkdirSync(join(archive, "agents"), { recursive: true });
  writeFileSync(join(archive, "manifest.json"), JSON.stringify({
    kind: "run_manifest",
    status: "complete",
  }));
  envelope.requiredModelEvidence.forEach((leg, index) => {
    const directory = join(archive, "agents", String(index + 1).padStart(6, "0"));
    mkdirSync(directory, { recursive: true });
    writeFileSync(join(directory, "agent.json"), JSON.stringify({
      label: leg.label,
      status: "complete",
      resolved_model: leg.pinnedModel,
    }));
  });
  return attachBriefPartitionDetails(
    await adjudicate({ envelope, recordDir }),
    envelope,
  );
}

test("an absent .review directory emits a complete-stage envelope with no legs", async () => {
  const { result, calls } = await run(args({ hasReviewDirectory: false, briefs: [] }));
  assert.equal(result.stage, "absent");
  assert.deepEqual(result.requiredModelEvidence, []);
  assert.deepEqual(result.proposedFindings, []);
  assert.equal(calls.length, 0);
});

test("a bare full-extent brief with no trigger is skipped without dispatch", async () => {
  const candidate = brief(".review/no-trigger.md", "---\nextent: full\n---\nJudge something.");
  const { result, calls } = await run(args({ briefs: [candidate] }));
  assert.equal(calls.length, 0);
  assert.deepEqual(result.briefs, [{
    brief: candidate.path,
    title: "No Trigger",
    status: "skipped",
    skipKind: "no-trigger",
    reason: "brief declares no relevance, occasion, or path-scope trigger",
  }]);
});

test("relevance gates full-extent briefs and remains an explicit Terra leg", async () => {
  const candidate = brief(".review/errors.md", "---\nextent: full\nsweep: whole-tree\nrelevance: Error-path changes.\n---\nJudge errors.");
  const met = await run(args({ briefs: [candidate] }), responder({ specialist: () => specialistResult([]) }));
  assert.equal(met.result.dispatches.length, 1);
  assert.equal(met.result.briefs[0].status, "run");
  assert.deepEqual([met.calls[0].opts.engine, met.calls[0].opts.model], ["codex", "gpt-5.6-terra"]);

  const missed = await run(args({ briefs: [candidate] }), responder({
    relevance: () => ({ decisions: [{ brief: candidate.path, applicable: false, reason: "no error path changed" }] }),
  }));
  assert.deepEqual(missed.result.requiredModelEvidence, [{
    label: "brief-relevance",
    role: "relevance",
    pinnedModel: "gpt-5.6-terra",
  }]);
  assert.equal(missed.result.briefs[0].skipKind, "relevance");
  assert.equal(missed.calls.length, 1);
});

test("occasion remains a deterministic run gate", async () => {
  const candidate = brief(".review/occasion.md", "---\noccasion: release\n---\nJudge release.");
  const matched = await run(args({ briefs: [candidate], occasion: "release" }), responder({ specialist: () => specialistResult([]) }));
  assert.equal(matched.result.dispatches.length, 1);
  assert.equal(matched.result.briefs[0].status, "run");

  for (const occasion of [null, "deployment"]) {
    const missed = await run(args({ briefs: [candidate], occasion }));
    assert.equal(missed.calls.length, 0);
    assert.equal(missed.result.briefs[0].skipKind, "occasion");
  }
});

test("path scope gates full-extent width independently of extent", async () => {
  const candidate = brief(".review/pkg/scoped.md", "---\nextent: full\nsweep: whole-tree\n---\nJudge package.");
  const matched = await run(args({ briefs: [candidate], changedPaths: ["pkg/x.go"] }), responder({ specialist: () => specialistResult([]) }));
  assert.equal(matched.result.dispatches.length, 1);
  assert.equal(matched.result.briefs[0].status, "run");

  const missed = await run(args({ briefs: [candidate], changedPaths: ["cmd/main.go"] }));
  assert.equal(missed.calls.length, 0);
  assert.equal(missed.result.briefs[0].skipKind, "empty");
});

test("a brief whose missing scope is its only trigger skips as misconfigured", async () => {
  const candidate = brief(
    ".review/missing/scoped.md",
    "---\nextent: full\nsweep: whole-tree\n---\nJudge the missing scope.",
    { scopeExists: false },
  );
  const { result, calls } = await run(args({
    briefs: [candidate],
    changedPaths: ["pkg/x.go"],
  }));

  assert.equal(calls.length, 0);
  assert.deepEqual(result.dispatches, []);
  assert.deepEqual(result.briefs, [{
    brief: candidate.path,
    title: "Scoped",
    status: "skipped",
    skipKind: "misconfigured-scope",
    reason: "brief scope missing/ matches no repository directory",
  }]);
  assert.deepEqual(result.misconfigurations, [{
    brief: candidate.path,
    title: "Scoped",
    kind: "misconfigured-scope",
    reason: "brief scope missing/ matches no repository directory",
  }]);
});

test("a matched occasion runs a missing-scope brief and records the misconfiguration", async () => {
  const candidate = brief(
    ".review/missing/scoped.md",
    "---\nextent: full\nsweep: whole-tree\noccasion: release\n---\nJudge the missing scope.",
    { scopeExists: false },
  );
  const { result, calls } = await run(
    args({ briefs: [candidate], occasion: "release", changedPaths: ["pkg/x.go"] }),
    responder({ specialist: () => specialistResult([]) }),
  );

  assert.equal(calls.length, 1);
  assert.match(calls[0].opts.label, /^repository-/);
  assert.match(calls[0].prompt, /Assigned scope: missing\/\./);
  assert.doesNotMatch(calls[0].prompt, /Assigned scope: the whole repository/);
  assert.deepEqual(result.dispatches, [{
    brief: candidate.path,
    title: "Scoped",
    label: "repository-review-missing-scoped-md-gpt",
    extent: "full",
    scope: "missing",
    files: [],
  }]);
  assert.deepEqual(result.briefs, [{
    brief: candidate.path,
    title: "Scoped",
    status: "run",
    reason: "applicable concern reviewed",
  }]);
  assert.deepEqual(result.misconfigurations, [{
    brief: candidate.path,
    title: "Scoped",
    kind: "misconfigured-scope",
    reason: "brief scope missing/ matches no repository directory",
  }]);
});

test("per-file partitioning follows repository structure and clamps to a lossless plan", async () => {
  const trackedFiles = [
    ...Array.from({ length: 30 }, (_, index) => ({ path: `pkg/api/f${index}.go`, bytes: 10 })),
    ...Array.from({ length: 30 }, (_, index) => ({ path: `pkg/ui/f${index}.go`, bytes: 10 })),
    ...Array.from({ length: 30 }, (_, index) => ({ path: `cmd/tool/f${index}.go`, bytes: 10 })),
  ];
  const candidate = brief(".review/full.md", "---\nextent: full\nsweep: per-file\noccasion: release\n---\nJudge every file.");
  const apiFiles = trackedFiles.filter((entry) => entry.path.startsWith("pkg/api/")).map((entry) => entry.path);
  const uiFiles = trackedFiles.filter((entry) => entry.path.startsWith("pkg/ui/")).map((entry) => entry.path);
  const toolFiles = trackedFiles.filter((entry) => entry.path.startsWith("cmd/tool/")).map((entry) => entry.path);
  const { result, calls } = await run(
    args({ briefs: [candidate], trackedFiles, occasion: "release" }),
    responder({
      partition: () => ({
        units: [
          { id: "api", concern: "API module", files: [...apiFiles, "pkg/ui/f0.go", "not-tracked.go"] },
          { id: "ui", concern: "UI module", files: uiFiles },
          { id: "tool", concern: "Command module", files: toolFiles.slice(0, -1) },
        ],
      }),
      specialist: () => specialistResult([]),
    }),
  );
  assert.equal(result.dispatches.length, 4);
  assert.notEqual(result.dispatches.length, Math.ceil(trackedFiles.length / 40));
  assert.deepEqual(
    result.dispatches.flatMap((entry) => entry.files).sort(),
    trackedFiles.map((entry) => entry.path).sort(),
  );
  assert.equal(new Set(result.dispatches.flatMap((entry) => entry.files)).size, trackedFiles.length);
  const partitionCall = calls.find((call) => call.opts.label?.startsWith("brief-partition-"));
  assert.ok(partitionCall);
  assert.deepEqual([partitionCall.opts.engine, partitionCall.opts.model], ["codex", "gpt-5.6-terra"]);
  assert.match(partitionCall.prompt, /Judge every file/);
  assert.match(partitionCall.prompt, /pkg\/api\/f0\.go/);
});

test("adaptive partitioning dispatches every coherent unit without a breadth cap", async () => {
  const trackedFiles = Array.from({ length: 25 }, (_, index) => ({ path: `module-${index}/file.go`, bytes: 10 }));
  const candidate = brief(".review/full.md", "---\nextent: full\nsweep: per-file\noccasion: release\n---\nJudge every module.");
  const { result, calls } = await run(
    args({ briefs: [candidate], trackedFiles, occasion: "release" }),
    responder({
      partition: () => ({
        units: trackedFiles.map((entry, index) => ({
          id: `module-${index}`,
          concern: `Module ${index}`,
          files: [entry.path],
        })),
      }),
      specialist: () => specialistResult([]),
    }),
  );
  assert.equal(result.dispatches.length, 25);
  assert.equal(calls.filter((call) => call.opts.label?.startsWith("repository-")).length, 25);
});

test("a mixed partition is one durable run disposition with its inapplicable unit detail", async (t) => {
  const trackedFiles = [
    { path: "pkg/api.go", bytes: 10 },
    { path: "pkg/ui.go", bytes: 10 },
    { path: "pkg/jobs.go", bytes: 10 },
  ];
  const candidate = brief(".review/full.md", "---\nextent: full\nsweep: per-file\noccasion: release\n---\nJudge every area.");
  const { result } = await run(
    args({ briefs: [candidate], trackedFiles, occasion: "release" }),
    responder({
      partition: () => ({
        units: [
          { id: "api", concern: "API behaviour", files: ["pkg/api.go"] },
          { id: "ui", concern: "UI behaviour", files: ["pkg/ui.go"] },
          { id: "jobs", concern: "Job behaviour", files: ["pkg/jobs.go"] },
        ],
      }),
      specialist: (label) => {
        if (label.includes("-1-")) return specialistResult([]);
        return specialistResult([], {
          status: "inapplicable",
          reason: label.includes("-2-")
            ? "the UI partition contains no changed behaviour to judge"
            : "the job partition contains no changed behaviour to judge",
        });
      },
    }),
  );

  assert.equal(result.briefs.length, 1);
  assert.equal(result.briefs[0].status, "run");
  const verdict = await adjudicateEnvelope(t, result);
  assert.equal(verdict.status, "complete");
  assert.deepEqual(verdict.ran, [{
    brief: candidate.path,
    title: "Full",
    inapplicableUnits: [
      {
        label: "repository-review-full-md-2-gpt",
        concern: "UI behaviour",
        reason: "the UI partition contains no changed behaviour to judge",
      },
      {
        label: "repository-review-full-md-3-gpt",
        concern: "Job behaviour",
        reason: "the job partition contains no changed behaviour to judge",
      },
    ],
  }]);
  assert.deepEqual(verdict.skipped, []);
});

test("an entirely inapplicable partition is one durable skipped disposition", async (t) => {
  const trackedFiles = [
    { path: "pkg/api.go", bytes: 10 },
    { path: "pkg/ui.go", bytes: 10 },
  ];
  const candidate = brief(".review/full.md", "---\nextent: full\nsweep: per-file\noccasion: release\n---\nJudge every area.");
  const { result } = await run(
    args({ briefs: [candidate], trackedFiles, occasion: "release" }),
    responder({
      partition: () => ({
        units: [
          { id: "api", concern: "API behaviour", files: ["pkg/api.go"] },
          { id: "ui", concern: "UI behaviour", files: ["pkg/ui.go"] },
        ],
      }),
      specialist: (label) => specialistResult([], {
        status: "inapplicable",
        reason: label.includes("-1-")
          ? "the API partition has no relevant change"
          : "the UI partition has no relevant change",
      }),
    }),
  );

  assert.equal(result.briefs.length, 1);
  assert.equal(result.briefs[0].status, "skipped");
  const verdict = await adjudicateEnvelope(t, result);
  assert.equal(verdict.status, "complete");
  assert.deepEqual(verdict.ran, []);
  assert.deepEqual(verdict.skipped, [{
    brief: candidate.path,
    title: "Full",
    skipKind: "inapplicable",
    reason: "all 2 partition units were inapplicable",
    inapplicableUnits: [
      {
        label: "repository-review-full-md-1-gpt",
        concern: "API behaviour",
        reason: "the API partition has no relevant change",
      },
      {
        label: "repository-review-full-md-2-gpt",
        concern: "UI behaviour",
        reason: "the UI partition has no relevant change",
      },
    ],
  }]);
});

test("an entirely applicable partition remains one unchanged durable run disposition", async (t) => {
  const trackedFiles = [
    { path: "pkg/api.go", bytes: 10 },
    { path: "pkg/ui.go", bytes: 10 },
  ];
  const candidate = brief(".review/full.md", "---\nextent: full\nsweep: per-file\noccasion: release\n---\nJudge every area.");
  const { result } = await run(
    args({ briefs: [candidate], trackedFiles, occasion: "release" }),
    responder({
      partition: () => ({
        units: [
          { id: "api", concern: "API behaviour", files: ["pkg/api.go"] },
          { id: "ui", concern: "UI behaviour", files: ["pkg/ui.go"] },
        ],
      }),
      specialist: () => specialistResult([]),
    }),
  );

  assert.equal(result.briefs.length, 1);
  assert.equal(result.briefs[0].status, "run");
  const verdict = await adjudicateEnvelope(t, result);
  assert.equal(verdict.status, "complete");
  assert.deepEqual(verdict.ran, [{
    brief: candidate.path,
    title: "Full",
  }]);
  assert.deepEqual(verdict.skipped, []);
});

test("a missing partition result leaves the brief not-run", async () => {
  const trackedFiles = [{ path: "pkg/a.go", bytes: 10 }, { path: "pkg/b.go", bytes: 10 }];
  const candidate = brief(".review/full.md", "---\nextent: full\nsweep: per-file\noccasion: release\n---\nJudge every file.");
  const { result, calls } = await run(
    args({ briefs: [candidate], trackedFiles, occasion: "release" }),
    responder({ partition: () => null }),
  );
  assert.equal(result.dispatches.length, 0);
  assert.equal(calls.filter((call) => call.opts.label?.startsWith("repository-")).length, 0);
  assert.equal(result.briefs[0].status, "not-run");
  assert.equal(result.briefs[0].reason, "partition exploration returned no usable result");
  assert.ok(result.requiredModelEvidence.some((leg) => leg.role === "partition"));
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
  const candidate = brief(".review/full.md", "---\nextent: full\nsweep: whole-tree\noccasion: release\n---\nJudge repository.");
  const { result } = await run(args({ briefs: [candidate], trackedFiles, occasion: "release" }), responder({ specialist: () => specialistResult([]) }));
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

test("all applicable findings are proposed on Terra and verified cross-family on Claude", async () => {
  const { result, calls } = await run(args());
  const specialists = calls.filter((call) => call.opts.label?.startsWith("repository-"));
  const verifiers = calls.filter((call) => call.opts.label?.startsWith("verify-"));
  assert.ok(specialists.every((call) =>
    call.opts.engine === "codex" && call.opts.model === "gpt-5.6-terra"));
  assert.ok(verifiers.every((call) =>
    call.opts.engine === "claude" && call.opts.model === "claude-opus-5"));
  assert.ok(specialists.every((specialist) =>
    verifiers.every((verifier) => specialist.opts.engine !== verifier.opts.engine)));
  assert.deepEqual(result.proposedFindings[0].rawVerifier, { verdict: "upheld", confidence: 92, reason: "confirmed" });
  // The fixture brief sits under pkg/ and pkg/x.go changed, so its path-scope
  // trigger fires and relevance is moot: a specialist and its verifier only.
  assert.equal(result.requiredModelEvidence.length, 2);
});

test("a repository observation is returned separately and never reaches verification", async () => {
  const { result, calls } = await run(args(), responder({
    specialist: () => specialistResult([], undefined, [observation()]),
  }));

  assert.deepEqual(result.proposedFindings, []);
  assert.equal(calls.filter((call) => call.opts.label?.startsWith("verify-")).length, 0);
  assert.deepEqual(result.outOfScopeObservations, [{
    id: "repository-review-pkg-errors-md-gpt:observation:1",
    source: "Errors",
    title: "pre-existing repository defect",
    path: "pkg/legacy.go",
    line: 11,
    explanation: "Unverified observation: unchanged code violates the repository concern.",
    observingLabel: "repository-review-pkg-errors-md-gpt",
    verified: false,
  }]);
  const specialist = calls.find((call) => call.opts.label?.startsWith("repository-"));
  assert.ok(!specialist.opts.schema.required.includes("outOfScopeObservations"));
  assert.ok(specialist.opts.schema.properties.outOfScopeObservations);
  assert.deepEqual(
    specialist.opts.schema.properties.outOfScopeObservations.items.required,
    ["title", "path", "line", "explanation"],
  );
});

test("a repository specialist may omit an empty observation array", async () => {
  const { result, calls } = await run(args(), responder({
    specialist: () => ({
      applicability: { status: "applicable", reason: "the concern applies" },
      findings: [],
    }),
  }));

  assert.deepEqual(result.outOfScopeObservations, []);
  assert.deepEqual(result.proposedFindings, []);
  assert.equal(calls.filter((call) => call.opts.label?.startsWith("verify-")).length, 0);
  const specialist = calls.find((call) => call.opts.label?.startsWith("repository-"));
  assert.ok(!specialist.opts.schema.required.includes("outOfScopeObservations"));
});

test("a matched occasion runs a scoped brief even when its scope is untouched", async () => {
  const candidate = brief(".review/deploy/checklist.md", "---\noccasion: release\n---\nJudge deploy readiness.");
  const matched = await run(args({ briefs: [candidate], occasion: "release", changedPaths: ["pkg/x.go"] }), responder({ specialist: () => specialistResult([]) }));
  assert.equal(matched.result.dispatches.length, 1);
  assert.equal(matched.result.briefs[0].status, "run");
});

test("an unnamed occasion vetoes a brief even when its scope is touched", async () => {
  const candidate = brief(".review/deploy/checklist.md", "---\noccasion: release\n---\nJudge deploy readiness.");
  const vetoed = await run(args({ briefs: [candidate], occasion: null, changedPaths: ["deploy/x.yaml"] }));
  assert.equal(vetoed.calls.length, 0);
  assert.equal(vetoed.result.briefs[0].skipKind, "occasion");
});

test("a met relevance runs a scoped brief even when its scope is untouched", async () => {
  const candidate = brief(".review/pkg/errors.md", "---\nrelevance: Error-path changes.\n---\nJudge errors.");
  const ran = await run(args({ briefs: [candidate], changedPaths: ["cmd/main.go"] }), responder({ specialist: () => specialistResult([]) }));
  assert.equal(ran.result.dispatches.length, 1);
  assert.equal(ran.result.briefs[0].status, "run");

  const skipped = await run(args({ briefs: [candidate], changedPaths: ["cmd/main.go"] }), responder({
    relevance: () => ({ decisions: [{ brief: candidate.path, applicable: false, reason: "no error path changed" }] }),
  }));
  assert.equal(skipped.result.briefs[0].skipKind, "relevance");
});

test("every repository-brief finding reaches verification beyond the former bound", async () => {
  const { result, calls } = await run(args(), responder({
    specialist: () => specialistResult([
      finding({ title: "one" }),
      finding({ title: "two", line: 8 }),
      finding({ title: "three survives", severity: "Medium", line: 9 }),
      finding({ title: "four survives", severity: "Low", line: 10 }),
      finding({ title: "five survives", line: 11 }),
      finding({ title: "six survives", line: 12 }),
      finding({ title: "seven survives", line: 13 }),
    ]),
  }));
  assert.deepEqual(result.proposedFindings.map((entry) => entry.title), [
    "one", "two", "three survives", "four survives", "five survives", "six survives", "seven survives",
  ]);
  assert.ok(result.proposedFindings.every((entry) => entry.rawVerifier?.verdict === "upheld"));
  const verifierCalls = calls.filter((call) => call.opts.label?.startsWith("verify-brief-"));
  assert.deepEqual(verifierCalls.map((call) => call.opts.label), [
    "verify-brief-1-1-claude", "verify-brief-1-2-claude",
  ]);
  assert.match(verifierCalls[0].prompt, /"title":"six survives"/);
  assert.doesNotMatch(verifierCalls[0].prompt, /"title":"seven survives"/);
  assert.match(verifierCalls[1].prompt, /"title":"seven survives"/);
});

test("slug-colliding brief labels retain verdicts for their own findings", async () => {
  const first = brief(".review/pkg/check.one.md", "# First\nJudge the first concern.");
  const second = brief(".review/pkg/check-one.md", "# Second\nJudge the second concern.");
  const { result, calls } = await run(args({ briefs: [first, second] }), responder({
    specialist: (_label, prompt) => specialistResult([
      finding({ title: prompt.includes(first.path) ? "first finding" : "second finding" }),
    ]),
    verify: (_label, prompt) => {
      const [entry] = JSON.parse(prompt.match(/Findings: (\[[^\n]+\])/)[1]);
      return {
        verdicts: [{
          findingId: entry.id,
          verdict: entry.title === "first finding" ? "upheld" : "refuted",
          confidence: 92,
          reason: `verdict for ${entry.title}`,
        }],
      };
    },
  }));

  const specialistLabels = calls
    .filter((call) => call.opts.label?.startsWith("repository-"))
    .map((call) => call.opts.label);
  assert.deepEqual(specialistLabels, [
    "repository-review-pkg-check-one-md-gpt",
    "repository-review-pkg-check-one-md-gpt",
  ]);
  assert.deepEqual(result.proposedFindings.map(({ id, title, rawVerifier }) => ({ id, title, rawVerifier })), [
    {
      id: "repository-brief-unit-1:finding:1",
      title: "first finding",
      rawVerifier: { verdict: "upheld", confidence: 92, reason: "verdict for first finding" },
    },
    {
      id: "repository-brief-unit-2:finding:1",
      title: "second finding",
      rawVerifier: { verdict: "refuted", confidence: 92, reason: "verdict for second finding" },
    },
  ]);
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
