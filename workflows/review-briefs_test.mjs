import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

import { fileInventoryFromPrompt, relevanceConditionsFromPrompt } from "./brief-planner-prompt-fixture.mjs";
import { findingsFromVerifierPrompt, verdictFor } from "./verifier-prompt-fixture.mjs";
import { invokeWorkflow, loadWorkflow } from "./workflow-invocation-fixture.mjs";
import { adjudicateEnvelope } from "./archive-fixture.mjs";
import { resolveRouting } from "./role-routing.mjs";

const pairedRouting = resolveRouting({ provisioned: ["claude", "codex"] });

import { briefRecords } from "./brief-dispositions.mjs";
import { shippedBriefPrompts, shippedContracts } from "./shipped-prompts-fixture.mjs";

const inputScriptPath = fileURLToPath(new URL("./review-brief-inputs.mjs", import.meta.url));
const scopeInputScriptPath = fileURLToPath(new URL("./review-scope-inputs.mjs", import.meta.url));
const script = loadWorkflow("review-briefs.js");

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
    dependsOn: "",
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
  outsideConcernFindings = [],
) {
  return { applicability, findings, outsideConcernFindings, outOfScopeObservations };
}

// The builder attaches each brief's frontmatter, title and deterministic
// disposition; the test arguments do the same through the one module, so a
// case names a brief by path and content and the occasion and changed paths
// it runs under, exactly as a deployment would.
function args(overrides = {}) {
  const merged = {
    target: "target111",
    head: "head222",
    occasion: null,
    hasReviewDirectory: true,
    changedPaths: ["pkg/x.go"],
    trackedFiles: [{ path: "pkg/x.go" }],
    briefs: [brief(".review/pkg/errors.md", "---\nrelevance: Error-path changes.\n---\nJudge errors.")],
    guidance: [{ repository: null, path: "AGENTS.md", origin: "checked-in", content: "PROJECT_GUIDANCE_TEAL" }],
    routing: pairedRouting,
    prompts: shippedBriefPrompts,
    contracts: shippedContracts(),
    ...overrides,
  };
  return { ...merged, briefs: briefRecords(merged.briefs, merged.occasion || null, merged.changedPaths) };
}

// Groups of one, so that no finding is merged unless a test says so.
function separateGroups(prompt) {
  return {
    groups: findingsFromVerifierPrompt(prompt).map((entry) => ({ findingIds: [entry.id], primary: entry.id, sharedCause: "" })),
  };
}

function responder({ relevance, partition, specialist, merge, verify } = {}) {
  return (label, prompt, opts) => {
    if (label === "brief-relevance") {
      if (relevance) return relevance(label, prompt, opts);
      const concerns = relevanceConditionsFromPrompt(prompt);
      return { decisions: concerns.map((entry) => ({ brief: entry.brief, applicable: true, reason: "relevant" })) };
    }
    if (label.startsWith("brief-partition-")) {
      if (partition) return partition(label, prompt, opts);
      const files = fileInventoryFromPrompt(prompt);
      return { units: [{ id: "complete-scope", concern: "complete assigned scope", files }] };
    }
    if (label === "brief-merge" || label.startsWith("brief-merge-round-")) return merge ? merge(label, prompt, opts) : separateGroups(prompt);
    if (label.startsWith("verify-")) {
      if (verify) return verify(label, prompt, opts);
      return {
        verdicts: findingsFromVerifierPrompt(prompt).map((entry) => verdictFor(entry.id, { confidence: 92, reason: "confirmed" })),
        newFindings: [],
        outOfScopeObservations: [],
      };
    }
    if (specialist) return specialist(label, prompt, opts);
    return specialistResult();
  };
}

function run(input, respond = responder()) {
  return invokeWorkflow(script, input, respond);
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

test("a repository brief carries no grouped-run attribution", async (t) => {
  const { result } = await run(args());
  assert.equal("members" in result, false);
  assert.ok(result.proposedFindings.every((entry) => !("member" in entry)));
  const verdict = await adjudicateEnvelope(t, result);
  assert.equal("memberReviews" in verdict, false);
});

test("relevance gates full-extent briefs and remains an explicit Sol leg", async () => {
  const candidate = brief(".review/errors.md", "---\nextent: full\nsweep: whole-tree\nrelevance: Error-path changes.\n---\nJudge errors.");
  const met = await run(args({ briefs: [candidate] }), responder({ specialist: () => specialistResult([]) }));
  assert.equal(met.result.dispatches.length, 1);
  assert.equal(met.result.briefs[0].status, "run");
  assert.deepEqual([met.calls[0].opts.engine, met.calls[0].opts.model], ["codex", "gpt-6.1-sol"]);

  const missed = await run(args({ briefs: [candidate] }), responder({
    relevance: () => ({ decisions: [{ brief: candidate.path, applicable: false, reason: "no error path changed" }] }),
  }));
  assert.deepEqual(missed.result.requiredModelEvidence, [{
    label: "brief-relevance",
    role: "relevance",
    pinnedModel: "gpt-6.1-sol",
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
  assert.match(calls[0].prompt, /All of `missing\/`, a path from the repository root, as it stands at the head commit/);
  assert.doesNotMatch(calls[0].prompt, /the whole repository/);
  assert.deepEqual(result.dispatches, [{
    brief: candidate.path,
    title: "Scoped",
    label: "repository-review-missing-scoped-md-codex",
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
    ...Array.from({ length: 30 }, (_, index) => ({ path: `pkg/api/f${index}.go` })),
    ...Array.from({ length: 30 }, (_, index) => ({ path: `pkg/ui/f${index}.go` })),
    ...Array.from({ length: 30 }, (_, index) => ({ path: `cmd/tool/f${index}.go` })),
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
  assert.deepEqual([partitionCall.opts.engine, partitionCall.opts.model], ["codex", "gpt-6.1-sol"]);
  assert.match(partitionCall.prompt, /Judge every file/);
  assert.match(partitionCall.prompt, /pkg\/api\/f0\.go/);
});

test("adaptive partitioning dispatches every coherent unit without a breadth cap", async () => {
  const trackedFiles = Array.from({ length: 25 }, (_, index) => ({ path: `module-${index}/file.go` }));
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
    { path: "pkg/api.go" },
    { path: "pkg/ui.go" },
    { path: "pkg/jobs.go" },
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
        label: "repository-review-full-md-2-codex",
        concern: "UI behaviour",
        reason: "the UI partition contains no changed behaviour to judge",
      },
      {
        label: "repository-review-full-md-3-codex",
        concern: "Job behaviour",
        reason: "the job partition contains no changed behaviour to judge",
      },
    ],
  }]);
  assert.deepEqual(verdict.skipped, []);
});

test("an entirely inapplicable partition is one durable skipped disposition", async (t) => {
  const trackedFiles = [
    { path: "pkg/api.go" },
    { path: "pkg/ui.go" },
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
        label: "repository-review-full-md-1-codex",
        concern: "API behaviour",
        reason: "the API partition has no relevant change",
      },
      {
        label: "repository-review-full-md-2-codex",
        concern: "UI behaviour",
        reason: "the UI partition has no relevant change",
      },
    ],
  }]);
});

test("an entirely applicable partition remains one unchanged durable run disposition", async (t) => {
  const trackedFiles = [
    { path: "pkg/api.go" },
    { path: "pkg/ui.go" },
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
  const trackedFiles = [{ path: "pkg/a.go" }, { path: "pkg/b.go" }];
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

test("every requested repository specialist dispatches", async () => {
  const briefs = Array.from({ length: 25 }, (_, index) =>
    brief(`.review/pkg/concern-${index + 1}.md`, `# Concern ${index + 1}\nJudge it.`));
  const { result, calls } = await run(args({ briefs }), responder({ specialist: () => specialistResult([]) }));
  assert.equal(result.dispatches.length, 25);
  assert.equal(calls.filter((call) => call.opts.label?.startsWith("repository-")).length, 25);
  assert.ok(result.briefs.every((entry) => entry.status === "run"));
});

test("a large whole-tree brief dispatches instead of becoming not-run", async () => {
  const trackedFiles = Array.from({ length: 400 }, (_, index) => ({ path: `pkg/f${index}.go` }));
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

test("all applicable findings are proposed on Astra and verified cross-family on Claude", async () => {
  const { result, calls } = await run(args());
  const specialists = calls.filter((call) => call.opts.label?.startsWith("repository-"));
  const verifiers = calls.filter((call) => call.opts.label?.startsWith("verify-"));
  assert.ok(specialists.every((call) =>
    call.opts.engine === "codex" && call.opts.model === "gpt-6.2-astra"));
  assert.ok(verifiers.every((call) =>
    call.opts.engine === "claude" && call.opts.model === "claude-opus-5-5"));
  assert.ok(specialists.every((specialist) =>
    verifiers.every((verifier) => specialist.opts.engine !== verifier.opts.engine)));
  const { findingId, ...rawVerifier } = verdictFor(result.proposedFindings[0].id, { confidence: 92, reason: "confirmed" });
  assert.deepEqual(result.proposedFindings[0].rawVerifier, rawVerifier);
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
    id: "repository-review-pkg-errors-md-codex:observation:1",
    source: "Errors",
    title: "pre-existing repository defect",
    path: "pkg/legacy.go",
    line: 11,
    explanation: "Unverified observation: unchanged code violates the repository concern.",
    observingLabel: "repository-review-pkg-errors-md-codex",
    verified: false,
  }]);
  const specialist = calls.find((call) => call.opts.label?.startsWith("repository-"));
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

test("repository-brief verifier batches retain every finding beyond six", async () => {
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
  assert.ok(result.proposedFindings.every((entry) => entry.rawVerifier?.verdict === "confirmed"));
  const verifierCalls = calls.filter((call) => call.opts.label?.startsWith("verify-brief-"));
  assert.deepEqual(verifierCalls.map((call) => call.opts.label), [
    "verify-brief-1-claude", "verify-brief-2-claude",
  ]);
  assert.match(verifierCalls[0].prompt, /"title": "six survives"/);
  assert.doesNotMatch(verifierCalls[0].prompt, /"title": "seven survives"/);
  assert.match(verifierCalls[1].prompt, /"title": "seven survives"/);
});

test("repository-brief verifier pools findings from separate units with their own context", async () => {
  const first = brief(".review/pkg/errors.md", "---\nextent: full\nsweep: per-file\n---\n# Errors\nJudge error paths.");
  const second = brief(".review/cmd/design.md", "---\nextent: full\nsweep: per-file\n---\n# Design\nJudge command design.");
  const { result, calls } = await run(args({
    briefs: [first, second],
    changedPaths: ["pkg/x.go", "cmd/main.go"],
    trackedFiles: [{ path: "pkg/x.go" }, { path: "cmd/main.go" }],
  }), responder({
    partition: (_label, prompt) => {
      const files = fileInventoryFromPrompt(prompt);
      return {
        units: [{
          id: files[0] === "pkg/x.go" ? "error-paths" : "command-design",
          concern: files[0] === "pkg/x.go" ? "error-path behaviour" : "command design",
          files,
        }],
      };
    },
    specialist: (label) => specialistResult([
      finding({ title: label.includes("errors") ? "error finding" : "design finding" }),
    ]),
  }));

  const verifierCalls = calls.filter((call) => call.opts.label?.startsWith("verify-brief-"));
  assert.equal(verifierCalls.length, 1);
  assert.equal(verifierCalls[0].opts.label, "verify-brief-1-claude");
  const findings = findingsFromVerifierPrompt(verifierCalls[0].prompt);
  assert.deepEqual(findings.map(({ title, wholeScope }) => ({ title, wholeScope })), [
    { title: "error finding", wholeScope: "pkg/" },
    { title: "design finding", wholeScope: "cmd/" },
  ]);
  assert.ok(findings.every((entry) => !("severity" in entry) && !("confidence" in entry)),
    "the verifier grades without the proposer's severity or confidence");
  assert.deepEqual(result.proposedFindings.map(({ verifyLabel, wholeScope }) => ({ verifyLabel, wholeScope })), [
    { verifyLabel: "verify-brief-1-claude", wholeScope: "pkg/" },
    { verifyLabel: "verify-brief-1-claude", wholeScope: "cmd/" },
  ]);
});

test("a finding about the change carries no whole scope to its verifier", async () => {
  const first = brief(".review/pkg/check-one.md", "---\nrelevance: Package changes.\n---\nJudge the first package concern.");
  const second = brief(".review/cmd/check-two.md", "---\nrelevance: Command changes.\n---\nJudge the second command concern.");
  const { calls } = await run(args({
    briefs: [first, second],
    changedPaths: ["pkg/x.go", "cmd/main.go"],
  }), responder({
    specialist: (label) => specialistResult([
      finding({ title: label.includes("check-one") ? "package finding" : "command finding" }),
    ]),
  }));

  const verifierCalls = calls.filter((call) => call.opts.label?.startsWith("verify-brief-"));
  assert.equal(verifierCalls.length, 1);
  const findings = findingsFromVerifierPrompt(verifierCalls[0].prompt);
  assert.deepEqual(findings.map(({ title, wholeScope }) => ({ title, wholeScope })), [
    { title: "package finding", wholeScope: undefined },
    { title: "command finding", wholeScope: undefined },
  ]);
});

test("a whole-scope finding keeps its scope through the merge when a finding about the change is primary", async () => {
  const whole = brief(".review/pkg/errors.md", "---\nextent: full\n---\n# Errors\nJudge error paths.");
  const diff = brief(".review/cmd/check-two.md", "---\nrelevance: Command changes.\n---\nJudge the command concern.");
  const { result, calls } = await run(args({
    briefs: [whole, diff],
    changedPaths: ["pkg/x.go", "cmd/main.go"],
    trackedFiles: [{ path: "pkg/x.go" }, { path: "cmd/main.go" }],
  }), responder({
    specialist: (_label, prompt) => specialistResult([
      finding({ title: prompt.includes(whole.path) ? "whole-scope finding" : "change finding" }),
    ]),
    merge: (_label, prompt) => {
      const entries = findingsFromVerifierPrompt(prompt);
      const primary = entries.find((entry) => entry.title === "change finding").id;
      return { groups: [{ findingIds: entries.map((entry) => entry.id), primary, sharedCause: "One fault." }] };
    },
  }));

  const verifierCalls = calls.filter((call) => call.opts.label?.startsWith("verify-brief-"));
  assert.equal(verifierCalls.length, 1);
  assert.deepEqual(findingsFromVerifierPrompt(verifierCalls[0].prompt).map(({ title, wholeScope }) => ({ title, wholeScope })), [
    { title: "change finding", wholeScope: "pkg/" },
  ]);
  assert.deepEqual(result.proposedFindings.map(({ title, wholeScope }) => ({ title, wholeScope })), [
    { title: "change finding", wholeScope: "pkg/" },
  ]);
});

test("slug-colliding brief labels retain verdicts for their own findings", async () => {
  const first = brief(".review/pkg/check.one.md", "# First\nJudge the first concern.");
  const second = brief(".review/pkg/check-one.md", "# Second\nJudge the second concern.");
  const { result, calls } = await run(args({ briefs: [first, second] }), responder({
    specialist: (_label, prompt) => specialistResult([
      finding({ title: prompt.includes(first.path) ? "first finding" : "second finding" }),
    ]),
    verify: (_label, prompt) => {
      const entries = findingsFromVerifierPrompt(prompt);
      return {
        verdicts: entries.map((entry) => verdictFor(entry.id, entry.title === "first finding"
          ? { confidence: 92, reason: `verdict for ${entry.title}` }
          : { verdict: "refuted", severity: "None", confidence: 92, reason: `verdict for ${entry.title}` })),
        newFindings: [],
        outOfScopeObservations: [],
      };
    },
  }));

  const specialistLabels = calls
    .filter((call) => call.opts.label?.startsWith("repository-"))
    .map((call) => call.opts.label);
  assert.deepEqual(specialistLabels, [
    "repository-review-pkg-check-one-md-codex",
    "repository-review-pkg-check-one-md-codex",
  ]);
  assert.deepEqual(result.proposedFindings.map(({ id, title, rawVerifier }) => ({ id, title, verdict: rawVerifier.verdict, reason: rawVerifier.reason })), [
    { id: "repository-brief-unit-1:finding:1", title: "first finding", verdict: "confirmed", reason: "verdict for first finding" },
    { id: "repository-brief-unit-2:finding:1", title: "second finding", verdict: "refuted", reason: "verdict for second finding" },
  ]);
});

test("a missing specialist result is a not-run disposition with a required archive leg", async () => {
  const { result } = await run(args(), responder({ specialist: () => null }));
  assert.equal(result.briefs.find((entry) => entry.status === "not-run").reason, "specialist returned no result");
  assert.ok(result.requiredModelEvidence.some((leg) => leg.role === "specialist"));
});

test("the enumerator emits large deterministic input directly as JSON and rejects generated-script mode", (t) => {
  const root = mkdtempSync(join(tmpdir(), "minos-brief-input-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
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
  writeFileSync(orientationPath, JSON.stringify({
    repository: root,
    guidance: [{ source: { path: "AGENTS.md" }, location: join(root, "AGENTS.md"), origin: "checked-in" }],
    misconfigurations: [],
  }));
  const env = { ...process.env, MINOS_ORIENTATION: orientationPath, MINOS_WORKSPACE: root, MINOS_PROVISIONED_ENGINES: "claude codex", MINOS_ROUTING: "" };
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

test("the brief stage's input builder hands its reviewers and verifiers the safeguard-request passages the setting chooses, and no depth passage", (t) => {
  const root = mkdtempSync(join(tmpdir(), "minos-brief-settings-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  execFileSync("git", ["init", "-q", root]);
  execFileSync("git", ["-C", root, "config", "user.name", "Fixture"]);
  execFileSync("git", ["-C", root, "config", "user.email", "fixture@example.test"]);
  writeFileSync(join(root, "AGENTS.md"), "Guidance.\n");
  writeFileSync(join(root, "code.txt"), "before\n");
  execFileSync("git", ["-C", root, "add", "."]);
  execFileSync("git", ["-C", root, "commit", "-qm", "base"]);
  const target = execFileSync("git", ["-C", root, "rev-parse", "HEAD"], { encoding: "utf8" }).trim();
  writeFileSync(join(root, "code.txt"), "after\n");
  execFileSync("git", ["-C", root, "commit", "-qam", "head"]);
  const head = execFileSync("git", ["-C", root, "rev-parse", "HEAD"], { encoding: "utf8" }).trim();
  const orientationPath = join(root, "orientation.json");
  writeFileSync(orientationPath, JSON.stringify({
    repository: root,
    guidance: [{ source: { path: "AGENTS.md" }, location: join(root, "AGENTS.md"), origin: "checked-in" }],
    misconfigurations: [],
  }));
  const build = (settings) => JSON.parse(execFileSync(process.execPath, [inputScriptPath, target, head], {
    encoding: "utf8",
    env: { ...process.env, MINOS_ORIENTATION: orientationPath, MINOS_WORKSPACE: root, MINOS_PROVISIONED_ENGINES: "claude codex", MINOS_ROUTING: "", MINOS_FILE_SAFEGUARD_REQUESTS: "", MINOS_REVIEW_DEPTH: "", ...settings },
  })).prompts;
  const passage = (slot, value) => readFileSync(new URL(`./review-briefs/${slot}/${value}.md`, import.meta.url), "utf8").trim();
  const off = build({});
  const on = build({ MINOS_FILE_SAFEGUARD_REQUESTS: "true" });
  assert.ok(off.repository.includes(passage("safeguard-requests", "false")), "a brief's reviewer reads false.md by default");
  assert.ok(on.repository.includes(passage("safeguard-requests", "true")), "a brief's reviewer reads true.md with filing on");
  assert.ok(!on.repository.includes(passage("safeguard-requests", "false")), "a brief's reviewer with filing on leaves out false.md");
  assert.ok(on.verifier.includes(passage("verifier-safeguard-requests", "true")), "a brief's verifier reads the request section with filing on");
  assert.ok(!off.verifier.includes(passage("verifier-safeguard-requests", "true")), "a brief's verifier reads no request section by default");
  for (const depth of ["light", "thorough"])
    assert.deepEqual(build({ MINOS_REVIEW_DEPTH: depth }), off, `the brief stage reads the same under ${depth} depth`);
});

test("a brief verifier observation reaches the result alongside its verdicts", async () => {
  const { result } = await run(args(), responder({
    verify: (label, prompt) => {
      const findings = findingsFromVerifierPrompt(prompt);
      return {
        verdicts: findings.map((entry) => verdictFor(entry.id)),
        newFindings: [],
        outOfScopeObservations: [observation({ title: "residue the verdict cannot carry" })],
      };
    },
  }));
  assert.equal(result.outOfScopeObservations.length, 1);
  const observed = result.outOfScopeObservations[0];
  assert.equal(observed.source, "verification");
  assert.equal(observed.title, "residue the verdict cannot carry");
  assert.equal(observed.verified, false);
  assert.match(observed.observingLabel, /^verify-brief-/);
});

test("the enumerator reads a tracked-file listing larger than the default child-process buffer", (t) => {
  const root = mkdtempSync(join(tmpdir(), "minos-brief-input-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  execFileSync("git", ["init", "-q", root]);
  execFileSync("git", ["-C", root, "config", "user.name", "Fixture"]);
  execFileSync("git", ["-C", root, "config", "user.email", "fixture@example.test"]);
  writeFileSync(join(root, "AGENTS.md"), "guidance\n");
  writeFileSync(join(root, "code.txt"), "before\n");
  execFileSync("git", ["-C", root, "add", "."]);
  const blob = execFileSync("git", ["-C", root, "hash-object", "-w", "--stdin"], { input: "tracked\n", encoding: "utf8" }).trim();
  const entries = Array.from({ length: 12_000 }, (_, index) =>
    `100644 ${blob}\ttree/${String(index).padStart(6, "0")}-${"p".repeat(90)}.txt`).join("\n");
  execFileSync("git", ["-C", root, "update-index", "--index-info"], { input: `${entries}\n` });
  execFileSync("git", ["-C", root, "commit", "-qm", "base"]);
  const target = execFileSync("git", ["-C", root, "rev-parse", "HEAD"], { encoding: "utf8" }).trim();
  writeFileSync(join(root, "code.txt"), "after\n");
  execFileSync("git", ["-C", root, "add", "code.txt"]);
  execFileSync("git", ["-C", root, "commit", "-qm", "head"]);
  const head = execFileSync("git", ["-C", root, "rev-parse", "HEAD"], { encoding: "utf8" }).trim();
  const listing = execFileSync("git", ["-C", root, "ls-files", "-z"], { maxBuffer: Infinity });
  assert.ok(listing.byteLength > 1024 * 1024);
  const orientationPath = join(root, "orientation.json");
  writeFileSync(orientationPath, JSON.stringify({
    repository: root,
    guidance: [{ source: { path: "AGENTS.md" }, location: join(root, "AGENTS.md"), origin: "checked-in" }],
    misconfigurations: [],
  }));
  const env = { ...process.env, MINOS_ORIENTATION: orientationPath, MINOS_WORKSPACE: root, MINOS_PROVISIONED_ENGINES: "claude codex", MINOS_ROUTING: "" };
  const enumerated = JSON.parse(execFileSync(process.execPath, [inputScriptPath, target, head], { encoding: "utf8", env }));
  assert.deepEqual(enumerated.briefs, []);
});

test("a file moved out of a brief's directory engages the brief for the resident lead and the brief builder alike", (t) => {
  const fixture = mkdtempSync(join(tmpdir(), "minos-brief-rename-"));
  t.after(() => rmSync(fixture, { recursive: true, force: true }));
  const root = join(fixture, "workspace");
  mkdirSync(join(root, "pkg"), { recursive: true });
  mkdirSync(join(root, ".review", "pkg"), { recursive: true });
  const git = (...gitArgs) => execFileSync("git", ["-C", root, ...gitArgs], { encoding: "utf8" });
  git("init", "-q");
  git("config", "user.name", "Fixture");
  git("config", "user.email", "fixture@example.test");
  writeFileSync(join(root, "AGENTS.md"), "guidance\n");
  writeFileSync(join(root, "pkg", "moved.go"), "package pkg\n" + "// retained line\n".repeat(20));
  writeFileSync(join(root, ".review", "pkg", "layout.md"), "# Layout\n\nJudge the package layout.\n");
  git("add", ".");
  git("commit", "-qm", "base");
  const target = git("rev-parse", "HEAD").trim();
  mkdirSync(join(root, "elsewhere"));
  git("mv", "pkg/moved.go", "elsewhere/moved.go");
  git("commit", "-qm", "head");
  const head = git("rev-parse", "HEAD").trim();
  const orientationPath = join(fixture, "orientation.json");
  writeFileSync(orientationPath, JSON.stringify({
    repository: root,
    guidance: [{ source: { path: "AGENTS.md" }, location: join(root, "AGENTS.md"), origin: "checked-in" }],
    misconfigurations: [],
  }));
  const env = { ...process.env, MINOS_ORIENTATION: orientationPath, MINOS_WORKSPACE: root, MINOS_PROVISIONED_ENGINES: "claude codex", MINOS_ROUTING: "" };
  const built = (script) => JSON.parse(execFileSync(process.execPath, [script, target, head], { encoding: "utf8", env }));

  const scope = built(scopeInputScriptPath);
  const briefInput = built(inputScriptPath);
  assert.deepEqual(scope, { briefsEngage: true });
  assert.deepEqual(briefInput.changedPaths, ["elsewhere/moved.go", "pkg/moved.go"]);
  assert.equal(briefInput.briefs.length, 1);
  assert.equal(briefInput.briefs[0].disposition.triggered, true);
});

const workerInput = args({
  briefs: [brief(".review/errors.md", "---\nrelevance: Error-path changes.\nextent: full\nsweep: per-file\n---\nJudge errors.")],
});
test("worker dispatch options strip capabilities and identify fan-out settlements", async (t) => {
  const respond = responder();
  const { result, calls } = await run(workerInput, respond);
  assert.equal(calls.length, 4);
  for (const { opts } of calls) {
    assert.deepEqual(opts.strip, ["skills", "agents"], `${opts.label} strips worker capabilities`);
    if (!["Relevance"].includes(opts.phase))
      assert.equal(opts.identity, true, `${opts.label} identifies its settlement`);
  }
  const verdict = await adjudicateEnvelope(t, result);
  assert.equal(verdict.complete, true, verdict.incomplete.join("\n"));
  assert.equal(verdict.confirmedFindings.length, 1);
});

test("identified partition failure reaches the verdict by label", async (t) => {
  const normal = responder();
  let failedLabel;
  const respond = (label, prompt, opts) => {
    if (label.startsWith("brief-partition-")) {
      failedLabel = label;
      return { identityEnvelope: { label, phase: opts.phase, output: null, failure: { message: "worker transport lost" } } };
    }
    return normal(label, prompt, opts);
  };
  const { result } = await run(workerInput, respond);
  const reason = `agent ${failedLabel} failed: worker transport lost`;
  assert.ok(result.incomplete?.includes(reason), JSON.stringify(result.incomplete));

  // Complete archive records isolate the producer's incompleteness channel.
  const verdict = await adjudicateEnvelope(t, result);
  assert.equal(verdict.complete, false);
  assert.ok(verdict.incomplete.includes(`workflow reported incomplete: ${reason}`));
  assert.deepEqual(verdict.confirmedFindings, []);
});

test("identified specialist failure reaches the verdict by label", async (t) => {
  const normal = responder();
  let failedLabel;
  const respond = (label, prompt, opts) => {
    if (opts.phase === "Review") {
      failedLabel = label;
      return { identityEnvelope: { label, phase: opts.phase, output: null, failure: { message: "worker transport lost" } } };
    }
    return normal(label, prompt, opts);
  };
  const { result } = await run(workerInput, respond);
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
  const { result } = await run(workerInput, respond);
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
    if (opts.phase === "Review") {
      answeredLabel = label;
      return { identityEnvelope: { label, phase: opts.phase, output: null, failure: null } };
    }
    return normal(label, prompt, opts);
  };
  const { result } = await run(workerInput, respond);
  assert.equal(result.reviewers.find((entry) => entry.label === answeredLabel).status, "done");
  assert.ok(result.incomplete.includes(`agent ${answeredLabel} answered without usable output`));
  assert.ok(!result.incomplete.some((reason) => reason.includes("failed:")));
});

test("the workflow refuses briefs the builder handed over without their deterministic pass", async () => {
  const bare = { path: ".review/pkg/errors.md", readPath: "/workspace/.review/pkg/errors.md", content: "Judge errors.", scope: "pkg", scopeExists: true };
  const input = { ...args(), briefs: [bare] };
  await assert.rejects(run(input), /deterministic brief enumeration is incomplete/);
  const half = { ...briefRecords([bare], null, ["pkg/x.go"])[0], disposition: { status: "skipped" } };
  await assert.rejects(run({ ...args(), briefs: [half] }), /deterministic brief enumeration is incomplete/);
});

test("the brief stage carries the pull request's title and description to its reviewers and verifiers", async () => {
  const pullRequest = { title: "Retry the upload", body: "Out of scope: the progress bar." };
  const { calls } = await run(args({ pullRequest }));
  const legs = calls.filter((call) => call.opts.label?.startsWith("repository-") || call.opts.label?.startsWith("verify-"));
  assert.ok(legs.length >= 2);
  for (const call of legs)
    assert.ok(call.prompt.includes('<pull-request-description title="Retry the upload">\nOut of scope: the progress bar.\n</pull-request-description>'));
  const without = await run(args());
  assert.ok(without.calls.every((call) => !call.prompt.includes("<pull-request-description")));
});

test("a diff-extent brief reviewer is assigned what the change did within the brief's directory", async () => {
  const { calls } = await run(args());
  const reviewer = calls.find((call) => call.opts.label?.startsWith("repository-"));
  assert.match(reviewer.prompt, /^# Repository-brief review\n/);
  assert.match(reviewer.prompt, /<repository-brief path="\.review\/pkg\/errors\.md">\n---\nrelevance: Error-path changes\.\n---\nJudge errors\.\n<\/repository-brief>/);
  assert.match(reviewer.prompt, /What `git diff target111\.\.\.head222` changed within `pkg\/`, a path from the repository root\./);
  assert.ok(!/\{\{[a-z-]+\}\}/.test(reviewer.prompt), "every slot is filled");
});

test("a brief reviewer's finding outside the brief's concern is verified without a whole scope", async () => {
  const candidate = brief(".review/pkg/errors.md", "---\nextent: full\nsweep: whole-tree\n---\nJudge errors.");
  const { result } = await run(args({ briefs: [candidate] }), responder({
    specialist: () => specialistResult([finding()], undefined, [], [finding({ title: "new race", line: 30 })]),
  }));
  assert.deepEqual(result.proposedFindings.map(({ title, outsideConcern, wholeScope }) => ({ title, outsideConcern, wholeScope })), [
    { title: "new race", outsideConcern: true, wholeScope: undefined },
    { title: "repository defect", outsideConcern: undefined, wholeScope: "pkg/" },
  ]);
});

test("a brief grouping that loses or repeats a finding stops the brief review before verification", async (t) => {
  const twoFindings = () => specialistResult([finding({ title: "first brief defect" }), finding({ title: "second brief defect", line: 9 })]);
  for (const groups of [
    (ids) => [{ findingIds: [ids[0]], primary: ids[0], sharedCause: "" }],
    (ids) => [{ findingIds: ids, primary: ids[0], sharedCause: "" }, { findingIds: [ids[1]], primary: ids[1], sharedCause: "" }],
    (ids) => [{ findingIds: ids, primary: "elsewhere", sharedCause: "" }],
    (ids) => [{ findingIds: [...ids, ids[1]], primary: ids[0], sharedCause: "" }],
  ]) {
    const { result, calls } = await run(args(), responder({
      specialist: twoFindings,
      merge: (_label, prompt) => ({ groups: groups(findingsFromVerifierPrompt(prompt).map((entry) => entry.id)) }),
    }));
    assert.equal(calls.filter((call) => call.opts.label?.startsWith("verify-")).length, 0);
    assert.deepEqual(result.proposedFindings, []);
    assert.ok(result.incomplete.some((reason) => reason.startsWith("agent brief-merge returned a grouping")), result.incomplete.join("\n"));
    const verdict = await adjudicateEnvelope(t, result);
    assert.equal(verdict.status, "incomplete");
  }
});

test("a finding a brief verifier raises is verified by a later leg, and one the last round raises stays unchecked", async (t) => {
  let raisedCount = 0;
  const { result, calls } = await run(args(), responder({
    verify: (label, prompt) => ({
      verdicts: findingsFromVerifierPrompt(prompt).map((entry) => verdictFor(entry.id, { confidence: 92, reason: "confirmed" })),
      newFindings: [finding({ title: `raised by ${label}`, line: 40 + (raisedCount += 1) })],
      outOfScopeObservations: [],
    }),
  }));
  const verifierLabels = calls.filter((call) => call.opts.label?.startsWith("verify-")).map((call) => call.opts.label);
  assert.equal(verifierLabels.length, 3);
  const lastLabel = verifierLabels.at(-1);
  assert.deepEqual(result.proposedFindings.map(({ source, verifyLabel }) => ({ source, verifyLabel })).slice(1), [
    { source: "verification", verifyLabel: verifierLabels[1] },
    { source: "verification", verifyLabel: verifierLabels[2] },
  ]);
  assert.deepEqual(result.uncheckedFindings.map(({ id, source }) => ({ id, source })), [
    { id: `${lastLabel}:raised:1`, source: "verification" },
  ]);
  assert.ok(result.incomplete.includes("verification stopped after 3 rounds with 1 finding(s) raised and not verified"));
  const verdict = await adjudicateEnvelope(t, result);
  assert.equal(verdict.status, "incomplete");
  assert.ok(verdict.incomplete.includes(
    `finding "raised by ${lastLabel}" has no complete verdict (raised in the last round of verification and never verified)`,
  ), verdict.incomplete.join("\n"));
  assert.deepEqual(verdict.confirmedFindings, []);
});

test("findings brief verifiers raise in one round are merged under the brief's merge label before the next round", async (t) => {
  const merges = [];
  const { result } = await run(args(), responder({
    merge: (label, prompt) => {
      merges.push(label);
      return separateGroups(prompt);
    },
    verify: (label, prompt) => ({
      verdicts: findingsFromVerifierPrompt(prompt).map((entry) => verdictFor(entry.id, { confidence: 92, reason: "confirmed" })),
      newFindings: label.startsWith("verify-brief-round-") ? [] : [
        finding({ title: "first raised defect", line: 50 }),
        finding({ title: "second raised defect", line: 60 }),
      ],
      outOfScopeObservations: [],
    }),
  }));
  assert.deepEqual(merges, ["brief-merge-round-2"], "one proposed finding needs no merge; the two raised ones do");
  assert.ok(result.requiredModelEvidence.some((leg) => leg.label === "brief-merge-round-2" && leg.role === "merge"));
  assert.equal(result.proposedFindings.filter((entry) => entry.source === "verification").length, 2);
  const verdict = await adjudicateEnvelope(t, result);
  assert.equal(verdict.status, "complete", verdict.incomplete.join("\n"));
});

test("the relevance and partition legs are sent their shipped prompts with every slot filled", async () => {
  const relevant = brief(".review/errors.md", "---\nrelevance: Error-path changes.\n---\nJudge errors.");
  const whole = brief(".review/pkg/layout.md", "---\nextent: full\nsweep: per-file\noccasion: release\n---\nJudge the layout.");
  const { calls } = await run(args({
    briefs: [relevant, whole],
    occasion: "release",
    trackedFiles: [{ path: "pkg/x.go" }, { path: "pkg/y.go" }, { path: "other/z.go" }],
  }), responder({ specialist: () => specialistResult([]) }));

  const relevance = calls.find((call) => call.opts.label === "brief-relevance").prompt;
  assert.match(relevance, /^# Brief relevance\n/);
  assert.match(relevance, /The change under review is `git diff target111\.\.\.head222`\./);
  assert.match(relevance, /PROJECT_GUIDANCE_TEAL/);
  assert.deepEqual(relevanceConditionsFromPrompt(relevance), [{ brief: ".review/errors.md", relevance: "Error-path changes." }]);
  assert.ok(!/\{\{[a-z-]+\}\}/.test(relevance), "every relevance slot is filled");

  const partition = calls.find((call) => call.opts.label.startsWith("brief-partition-")).prompt;
  assert.match(partition, /^# Brief partition\n/);
  assert.match(partition, /<repository-brief path="\.review\/pkg\/layout\.md">\n---\nextent: full\nsweep: per-file\noccasion: release\n---\nJudge the layout\.\n<\/repository-brief>/);
  assert.match(partition, /PROJECT_GUIDANCE_TEAL/);
  assert.deepEqual(fileInventoryFromPrompt(partition), ["pkg/x.go", "pkg/y.go"]);
  assert.ok(!/\{\{[a-z-]+\}\}/.test(partition), "every partition slot is filled");
});

test("a partitioned unit's reviewer is assigned its part, its unit and its files from the shipped assignment parts", async () => {
  const whole = brief(".review/layout.md", "---\nextent: full\nsweep: per-file\noccasion: release\n---\nJudge the layout.");
  const { calls } = await run(args({
    briefs: [whole],
    occasion: "release",
    trackedFiles: [{ path: "pkg/x.go" }, { path: "pkg/y.go" }],
  }), responder({
    partition: () => ({ units: [{ id: "all", concern: "The package's one module, read for its layout.", files: ["pkg/x.go", "pkg/y.go"] }] }),
    specialist: () => specialistResult([]),
  }));
  const reviewer = calls.find((call) => call.opts.label?.startsWith("repository-")).prompt;
  const expected = [
    "All of `./`, a path from the repository root, as it stands at the head commit, `head222`, whether or not the change touched it.",
    "The plan for reading that part gives you this unit of it: The package's one module, read for its layout.",
    "The files you answer for:\n\n- `pkg/x.go`\n- `pkg/y.go`",
  ].join("\n\n");
  assert.ok(reviewer.includes(expected), reviewer);
  assert.ok(!/\{\{[a-z-]+\}\}/.test(reviewer), "every slot is filled");
});

test("a brief input missing a shipped brief-planner or assignment prompt fails before dispatch", async () => {
  for (const prompts of [
    { ...shippedBriefPrompts, briefRelevance: undefined },
    { ...shippedBriefPrompts, briefPartition: `${shippedBriefPrompts.briefPartition}\n{{findings}}` },
    { ...shippedBriefPrompts, assignment: { ...shippedBriefPrompts.assignment, files: undefined } },
    { ...shippedBriefPrompts, assignment: undefined },
  ]) {
    const calls = [];
    await assert.rejects(run(args({ prompts }), (label, prompt, opts) => {
      calls.push(label);
      return responder()(label, prompt, opts);
    }));
    assert.deepEqual(calls, []);
  }
});
