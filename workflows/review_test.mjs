// Drives workflows/review.js without engines: the script runs in the same
// shape the Workflow tool gives it (an async function body with agent,
// parallel, pipeline, phase, log and args as globals), with agent() answered
// from synthetic fixtures keyed by label. Run with: node --test workflows/
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";

const source = await readFile(
  fileURLToPath(new URL("./review.js", import.meta.url)),
  "utf8"
);
const lifecycle = await readFile(
  fileURLToPath(new URL("../lifecycle/lifecycle.md", import.meta.url)),
  "utf8"
);
const body = source.replace(/^export const meta =/m, "const meta =");
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
const script = new AsyncFunction(
  "agent",
  "parallel",
  "pipeline",
  "phase",
  "log",
  "args",
  body
);

async function runScript(args, respond) {
  const calls = [];
  const agent = async (prompt, opts = {}) => {
    calls.push({ prompt, opts });
    return respond(opts.label || "", prompt, opts);
  };
  const parallel = async (thunks) =>
    Promise.all(thunks.map((thunk) => thunk().catch(() => null)));
  const pipeline = async (items, ...stages) =>
    Promise.all(
      items.map(async (item, index) => {
        let value = item;
        try {
          for (const stage of stages) value = await stage(value, item, index);
          return value;
        } catch {
          return null;
        }
      })
    );
  const result = await script(agent, parallel, pipeline, () => {}, () => {}, args);
  return { result, calls };
}

const ARGS = { target: "aaa111", head: "bbb222" };

function scoutFixture({ files = [{ path: "internal/x.go", added: 30, deleted: 30 }], briefs = [] } = {}) {
  return { files, briefs };
}

// A responder built from per-label-prefix handlers, with sane defaults:
// reviewers report one finding each, verifiers confirm on the right family.
// Both legs self-report a model: a reviewer's default self-report matches its
// own pinned family (aspect `-gpt` legs GPT, everything else Claude), so a
// fixture that doesn't care about proposer family need not set it — and a leg
// that means to lie sets selfReportedModel explicitly.
function responder({ scout = scoutFixture(), scopes, review, verify } = {}) {
  return (label, prompt, opts) => {
    if (label === "scout") return scout;
    if (label === "scope-files") return scopes || { scopes: [] };
    if (label.includes("-verify-")) {
      const expected = label.endsWith("-gpt") ? "gpt-5.6-sol" : "claude-fable-5";
      return verify
        ? verify(label, prompt, opts, expected)
        : { confirmed: true, reason: "reproduced", selfReportedModel: expected };
    }
    const reviewerModel = label.endsWith("-gpt") ? "gpt-5.6-sol" : "claude-opus-4-8";
    const result = review
      ? review(label, prompt, opts)
      : {
          findings: [
            { title: `finding from ${label}`, priority: "high", path: "internal/x.go", line: 3, explanation: "e" },
          ],
        };
    if (result && typeof result === "object" && result.selfReportedModel === undefined)
      return { ...result, selfReportedModel: reviewerModel };
    return result;
  };
}

test("right-family verdicts complete the run", async () => {
  const { result } = await runScript(ARGS, responder());
  assert.equal(result.verdictsComplete, true);
  assert.deepEqual(result.incomplete, []);
  assert.ok(result.findings.length > 0);
  for (const finding of result.findings) {
    assert.equal(finding.verdict, "confirmed");
    assert.ok(["gpt", "claude"].includes(finding.verify.expectedFamily));
  }
});

test("verification crosses to the other family", async () => {
  const { result } = await runScript(ARGS, responder());
  for (const finding of result.findings) {
    const reviewer = result.reviewers.find((r) => r.label === finding.source);
    assert.ok(reviewer, `finding source ${finding.source} is a reviewer`);
    assert.notEqual(finding.verify.expectedFamily, reviewer.family);
  }
});

// Decision 2026-07-17: the lead's session model is GPT, so an unpinned agent
// inherits GPT and can never be the Claude family. Every review and verify leg
// must therefore carry an explicit model pin — none may rely on inheritance —
// or cross-family verification silently collapses onto one family. This test
// runs with the harness's default (unpinned = GPT) and proves no leg is left
// unpinned and every family carries its own id.
test("every review and verify leg is explicitly pinned to its family's model, never inheriting", async () => {
  const { calls } = await runScript(ARGS, responder());
  const workLegs = calls.filter((c) => {
    const label = c.opts.label || "";
    return label !== "scout" && label !== "scope-files";
  });
  assert.ok(workLegs.length > 0);
  for (const call of workLegs) {
    const label = call.opts.label || "";
    assert.notEqual(call.opts.model, undefined, `${label} must not inherit the session model`);
    // Aspect and verify legs encode their family in the suffix; an unsuffixed
    // brief review leg is always the Claude family.
    const family = label.endsWith("-gpt") ? "gpt" : "claude";
    assert.equal(
      call.opts.model,
      family === "gpt" ? "gpt-5.6-sol" : "claude-opus-4-8",
      `${label} pinned to its ${family} model`
    );
  }
});

test("a wrong-family self-report is no verdict and the run is incomplete", async () => {
  const { result } = await runScript(
    ARGS,
    responder({
      verify: (label) =>
        label.endsWith("-gpt")
          ? { confirmed: false, reason: "looks fine", selfReportedModel: "claude-fable-5" }
          : { confirmed: true, reason: "reproduced", selfReportedModel: "claude-fable-5" },
    })
  );
  assert.equal(result.verdictsComplete, false);
  const wrong = result.findings.filter((f) => f.verify.expectedFamily === "gpt");
  assert.ok(wrong.length > 0);
  for (const finding of wrong) {
    assert.equal(finding.verdict, "no-verdict");
    assert.match(finding.verification, /fallen back/);
  }
  assert.ok(result.incomplete.some((line) => line.includes("no verdict")));
});

// Cross-family independence binds the proposer as well as the verifier: a
// review leg that silently fell back to the GPT session and self-reports GPT
// while pinned to Claude cannot yield a publishable verdict, even if its
// verifier is a genuine other-family check.
test("a Claude review leg that self-reports GPT yields no verdict and an incomplete run", async () => {
  const { result } = await runScript(
    ARGS,
    responder({
      review: (label) => ({
        findings: [
          { title: `f ${label}`, priority: "high", path: "internal/x.go", line: 3, explanation: "e" },
        ],
        selfReportedModel: label.endsWith("-claude") ? "gpt-5.6-sol" : undefined,
      }),
    })
  );
  const fellBack = result.findings.filter((f) => f.review.configuredFamily === "claude");
  assert.ok(fellBack.length > 0);
  for (const finding of fellBack) {
    assert.equal(finding.verdict, "no-verdict");
    assert.match(finding.verification, /reviewer self-reported gpt-5\.6-sol/);
    assert.equal(finding.review.selfReportedModel, "gpt-5.6-sol");
  }
  assert.equal(result.verdictsComplete, false);
  assert.ok(result.incomplete.some((line) => line.includes("no verdict")));
});

// The result must carry, per finding, everything the lead needs for the
// external actual-model check on BOTH legs — label, configured/expected family,
// pinned model and self-report — so a lying or absent self-report is detectable
// against the Workflow run record.
test("each finding exposes both legs' families and pins for the lead's actual-model check", async () => {
  const { result } = await runScript(ARGS, responder());
  assert.ok(result.findings.length > 0);
  for (const finding of result.findings) {
    assert.ok(finding.review, "finding carries its review leg");
    assert.equal(finding.review.label, finding.source);
    assert.ok(["gpt", "claude"].includes(finding.review.configuredFamily));
    assert.equal(
      finding.review.pinnedModel,
      finding.review.configuredFamily === "gpt" ? "gpt-5.6-sol" : "claude-opus-4-8"
    );
    assert.equal(typeof finding.review.selfReportedModel, "string");
    assert.notEqual(finding.verify.expectedFamily, finding.review.configuredFamily);
    assert.equal(
      finding.verify.pinnedModel,
      finding.verify.expectedFamily === "gpt" ? "gpt-5.6-sol" : "claude-opus-4-8"
    );
  }
  assert.match(result.modelCheck, /review leg/);
  assert.match(result.modelCheck, /verify leg/);
});

test("a missing verifier result is no verdict, never not-confirmed", async () => {
  const { result } = await runScript(ARGS, responder({ verify: () => null }));
  assert.equal(result.verdictsComplete, false);
  for (const finding of result.findings) {
    assert.equal(finding.verdict, "no-verdict");
    assert.notEqual(finding.verdict, "not-confirmed");
  }
  assert.equal(result.incomplete.length, result.findings.length);
});

test("an unconfirmed finding on the right family is not-confirmed and still complete", async () => {
  const { result } = await runScript(
    ARGS,
    responder({
      verify: (label, prompt, opts, expected) => ({
        confirmed: false,
        reason: "could not reproduce",
        selfReportedModel: expected,
      }),
    })
  );
  assert.equal(result.verdictsComplete, true);
  for (const finding of result.findings) assert.equal(finding.verdict, "not-confirmed");
});

test("empty reviews complete with no findings and no verifier calls", async () => {
  const { result, calls } = await runScript(ARGS, responder({ review: () => ({ findings: [] }) }));
  assert.equal(result.verdictsComplete, true);
  assert.deepEqual(result.findings, []);
  assert.equal(calls.filter((c) => (c.opts.label || "").includes("-verify-")).length, 0);
});

test("a reviewer that returns no result makes the run incomplete", async () => {
  const { result } = await runScript(
    ARGS,
    responder({ review: (label) => (label === "review-1-gpt" ? null : { findings: [] }) })
  );
  assert.equal(result.verdictsComplete, false);
  assert.ok(result.incomplete.some((line) => line.includes("review-1-gpt")));
});

test("missing args fail closed", async () => {
  const { result, calls } = await runScript(undefined, responder());
  assert.equal(result.verdictsComplete, false);
  assert.equal(calls.length, 0);
});

test("a scout that returns no result fails closed", async () => {
  const { result } = await runScript(ARGS, (label) => (label === "scout" ? null : {}));
  assert.equal(result.verdictsComplete, false);
  assert.ok(result.incomplete.some((line) => line.includes("scout")));
});

test("breadth scales: a small diff runs two reviewers, a large one more than four", async () => {
  const small = await runScript(
    ARGS,
    responder({
      scout: scoutFixture({ files: [{ path: "a.go", added: 5, deleted: 5 }] }),
      review: () => ({ findings: [] }),
    })
  );
  assert.equal(small.result.reviewers.length, 2);

  const bigFiles = Array.from({ length: 12 }, (_, i) => ({ path: `pkg/f${i}.go`, added: 150, deleted: 50 }));
  const large = await runScript(
    ARGS,
    responder({ scout: scoutFixture({ files: bigFiles }), review: () => ({ findings: [] }) })
  );
  assert.ok(large.result.reviewers.length > 4, `got ${large.result.reviewers.length}`);
});

// --- .review/ briefs ---

function brief(path, frontmatter, { scopeExists = true } = {}) {
  const front = frontmatter ? `---\n${frontmatter}\n---\n` : "";
  return { path, content: `${front}Judge the concern.`, scopeExists };
}

test("a brief the diff gives nothing to do is reported skipped, never passed", async () => {
  const { result } = await runScript(
    ARGS,
    responder({
      scout: scoutFixture({ briefs: [brief(".review/error-tone.md", "title: Error Tone")] }),
      review: (label) =>
        label.startsWith("brief-")
          ? { applicable: false, reason: "version-only bump; no user-facing text changed", findings: [] }
          : { findings: [] },
    })
  );
  const report = result.briefs.find((b) => b.brief === ".review/error-tone.md");
  assert.equal(report.status, "skipped");
  assert.match(report.reason, /version-only/);
  assert.equal(result.verdictsComplete, true);
});

test("an occasion-gated brief is skipped unless the run names a matching occasion", async () => {
  const briefs = [brief(".review/release-notes.md", "occasion: release")];
  const respond = responder({
    scout: scoutFixture({ briefs }),
    review: (label) =>
      label.startsWith("brief-")
        ? { applicable: true, reason: "ran", findings: [] }
        : { findings: [] },
  });

  const without = await runScript(ARGS, respond);
  assert.equal(without.result.briefs[0].status, "skipped");
  assert.match(without.result.briefs[0].reason, /occasion/);

  const mismatched = await runScript({ ...ARGS, occasion: "nightly" }, respond);
  assert.equal(mismatched.result.briefs[0].status, "skipped");

  const matched = await runScript({ ...ARGS, occasion: "release" }, respond);
  assert.equal(matched.result.briefs[0].status, "run");
});

test("a diff-extent scoped brief is skipped without dispatch when nothing in scope changed", async () => {
  const { result, calls } = await runScript(
    ARGS,
    responder({
      scout: scoutFixture({
        files: [{ path: "cmd/main.go", added: 3, deleted: 1 }],
        briefs: [brief(".review/internal/api/limits.md", null)],
      }),
      review: () => ({ findings: [] }),
    })
  );
  assert.equal(result.briefs[0].status, "skipped");
  assert.match(result.briefs[0].reason, /internal\/api/);
  assert.equal(calls.filter((c) => (c.opts.label || "").startsWith("brief-")).length, 0);
});

test("a brief whose subfolder matches no repo directory runs repo-wide, never citing the ghost scope, and warns", async () => {
  const { result, calls } = await runScript(
    ARGS,
    responder({
      scout: scoutFixture({
        files: [{ path: "cmd/main.go", added: 3, deleted: 1 }],
        briefs: [brief(".review/ghost/limits.md", null, { scopeExists: false })],
      }),
      review: (label) =>
        label.startsWith("brief-") ? { applicable: true, reason: "ran", findings: [] } : { findings: [] },
    })
  );
  const report = result.briefs.find((b) => b.brief === ".review/ghost/limits.md");
  assert.equal(report.status, "run");
  assert.match(report.warning, /matches no repository directory/);
  // The reviewer is actually prompted repo-wide — the nonexistent scope path is
  // not preserved in the prompt (which would silently narrow the review to a
  // directory that does not exist).
  const briefCall = calls.find((c) => (c.opts.label || "").startsWith("brief-"));
  assert.ok(briefCall);
  assert.doesNotMatch(briefCall.prompt, /ghost/);
});

test("a full-extent brief runs even when nothing in its scope changed", async () => {
  const { result } = await runScript(
    ARGS,
    responder({
      scout: scoutFixture({
        files: [{ path: "cmd/main.go", added: 3, deleted: 1 }],
        briefs: [brief(".review/internal/api/registry.md", "extent: full")],
      }),
      scopes: { scopes: [{ scope: "internal/api", files: ["internal/api/a.go"] }] },
      review: (label) =>
        label.startsWith("brief-") ? { applicable: true, reason: "audited", findings: [] } : { findings: [] },
    })
  );
  assert.equal(result.briefs[0].status, "run");
});

const filesNamed = (n) => Array.from({ length: n }, (_, i) => `pkg/f${i}.go`);

const briefScopeResponder = (frontmatter, fileCount, bytes = 0) =>
  responder({
    scout: scoutFixture({ briefs: [brief(".review/pkg/style.md", frontmatter)] }),
    scopes: { scopes: [{ scope: "pkg", files: filesNamed(fileCount), bytes }] },
    review: (label) =>
      label.startsWith("brief-") ? { applicable: true, reason: "audited", findings: [] } : { findings: [] },
  });

test("a large per-file full-extent brief is split; a whole-tree brief within limit never is", async () => {
  const perFile = await runScript(ARGS, briefScopeResponder("extent: full", 90));
  const perFileUnits = perFile.result.reviewers.filter((r) => r.label.startsWith("brief-"));
  assert.equal(perFileUnits.length, 3);

  const wholeTree = await runScript(ARGS, briefScopeResponder("extent: full\nsweep: whole-tree", 50));
  const wholeTreeUnits = wholeTree.result.reviewers.filter((r) => r.label.startsWith("brief-"));
  assert.equal(wholeTreeUnits.length, 1);
});

// One weighted reading-volume budget, not a hard file cliff: content bytes plus
// a per-file navigation overhead, refused only when the combined volume exceeds
// the budget. The prior counterexample — 61 trivially small files — must run,
// while a genuinely large tree and a few huge files must both refuse.
test("the 61-tiny-files counterexample runs under the weighted reading budget", async () => {
  const { result, calls } = await runScript(
    ARGS,
    briefScopeResponder("extent: full\nsweep: whole-tree", 61, 610)
  );
  const report = result.briefs.find((b) => b.brief === ".review/pkg/style.md");
  assert.equal(report.status, "run");
  assert.equal(
    calls.filter((c) => (c.opts.label || "").startsWith("brief-")).length,
    1
  );
  assert.equal(result.verdictsComplete, true);
});

test("a genuinely large many-file whole-tree scope is reported not run on weighted volume", async () => {
  const { result, calls } = await runScript(
    ARGS,
    briefScopeResponder("extent: full\nsweep: whole-tree", 250, 25_000)
  );
  const report = result.briefs.find((b) => b.brief === ".review/pkg/style.md");
  assert.equal(report.status, "not-run");
  assert.equal(report.scopeSize, 250);
  assert.equal(report.scopeBytes, 25_000);
  // Content is tiny, but 250 files' navigation overhead alone overruns the
  // budget: 25000 + 250 × 2000 = 525000 > 400000.
  assert.equal(report.readingVolume, 525_000);
  assert.match(report.reason, /weighted reading volume of 525000 bytes/);
  assert.match(report.reason, /400000-byte budget/);
  assert.equal(calls.filter((c) => (c.opts.label || "").startsWith("brief-")).length, 0);
  assert.equal(result.verdictsComplete, false);
  assert.ok(result.incomplete.some((line) => line.includes("not run")));
});

test("a few huge files is reported not run on weighted volume", async () => {
  const { result, calls } = await runScript(
    ARGS,
    briefScopeResponder("extent: full\nsweep: whole-tree", 5, 500_000)
  );
  const report = result.briefs.find((b) => b.brief === ".review/pkg/style.md");
  assert.equal(report.status, "not-run");
  assert.equal(report.scopeSize, 5);
  assert.equal(report.scopeBytes, 500_000);
  // 500000 + 5 × 2000 = 510000 > 400000.
  assert.equal(report.readingVolume, 510_000);
  assert.match(report.reason, /500000 non-binary bytes/);
  assert.equal(calls.filter((c) => (c.opts.label || "").startsWith("brief-")).length, 0);
  assert.equal(result.verdictsComplete, false);
});

// A scope that cannot be measured must not be reviewed blind: whether the
// inventory agent returns nothing at all, or returns without the brief's scope,
// the whole-tree brief is reported not run and no reviewer is dispatched.
test("a whole-tree brief whose scope inventory returns no result is reported not run", async () => {
  const { result, calls } = await runScript(ARGS, (label) => {
    if (label === "scout")
      return scoutFixture({ briefs: [brief(".review/pkg/style.md", "extent: full\nsweep: whole-tree")] });
    if (label === "scope-files") return null;
    return { findings: [], selfReportedModel: "gpt-5.6-sol" };
  });
  const report = result.briefs.find((b) => b.brief === ".review/pkg/style.md");
  assert.equal(report.status, "not-run");
  assert.match(report.reason, /measured scope unavailable/);
  assert.equal(calls.filter((c) => (c.opts.label || "").startsWith("brief-")).length, 0);
  assert.equal(result.verdictsComplete, false);
});

test("a whole-tree brief absent from the returned inventory is reported not run", async () => {
  const { result, calls } = await runScript(
    ARGS,
    responder({
      scout: scoutFixture({ briefs: [brief(".review/pkg/style.md", "extent: full\nsweep: whole-tree")] }),
      scopes: { scopes: [{ scope: "other", files: ["other/a.go"], bytes: 10 }] },
      review: (label) =>
        label.startsWith("brief-") ? { applicable: true, reason: "audited", findings: [] } : { findings: [] },
    })
  );
  const report = result.briefs.find((b) => b.brief === ".review/pkg/style.md");
  assert.equal(report.status, "not-run");
  assert.match(report.reason, /measured scope unavailable/);
  assert.equal(calls.filter((c) => (c.opts.label || "").startsWith("brief-")).length, 0);
  assert.equal(result.verdictsComplete, false);
});

// A per-file full brief plans off the same inventory (to shard), so an
// unmeasured scope must fail closed for it too — not silently collapse to one
// unsharded pass over an unknown scope.
test("a per-file full brief whose scope inventory returns no result is reported not run", async () => {
  const { result, calls } = await runScript(ARGS, (label) => {
    if (label === "scout")
      return scoutFixture({ briefs: [brief(".review/pkg/style.md", "extent: full")] });
    if (label === "scope-files") return null;
    return { findings: [], selfReportedModel: "gpt-5.6-sol" };
  });
  const report = result.briefs.find((b) => b.brief === ".review/pkg/style.md");
  assert.equal(report.status, "not-run");
  assert.match(report.reason, /measured scope unavailable/);
  assert.equal(calls.filter((c) => (c.opts.label || "").startsWith("brief-")).length, 0);
  assert.equal(result.verdictsComplete, false);
});

test("a per-file full brief absent from the returned inventory is reported not run", async () => {
  const { result, calls } = await runScript(
    ARGS,
    responder({
      scout: scoutFixture({ briefs: [brief(".review/pkg/style.md", "extent: full")] }),
      scopes: { scopes: [{ scope: "other", files: ["other/a.go"], bytes: 10 }] },
      review: (label) =>
        label.startsWith("brief-") ? { applicable: true, reason: "audited", findings: [] } : { findings: [] },
    })
  );
  const report = result.briefs.find((b) => b.brief === ".review/pkg/style.md");
  assert.equal(report.status, "not-run");
  assert.match(report.reason, /measured scope unavailable/);
  assert.equal(calls.filter((c) => (c.opts.label || "").startsWith("brief-")).length, 0);
  assert.equal(result.verdictsComplete, false);
});

test("brief findings are verified by the other family and fail closed too", async () => {
  const { result } = await runScript(
    ARGS,
    responder({
      scout: scoutFixture({ briefs: [brief(".review/error-tone.md", "title: Error Tone")] }),
      review: (label) =>
        label.startsWith("brief-")
          ? {
              applicable: true,
              reason: "ran",
              findings: [{ title: "curt error", priority: "low", path: "internal/x.go", line: 9, explanation: "e" }],
            }
          : { findings: [] },
      verify: () => ({ confirmed: true, reason: "agreed", selfReportedModel: "claude-fable-5" }),
    })
  );
  const briefFinding = result.findings.find((f) => f.source === "Error Tone");
  assert.ok(briefFinding);
  assert.equal(briefFinding.verify.expectedFamily, "gpt");
  assert.equal(briefFinding.verdict, "no-verdict");
  assert.equal(result.verdictsComplete, false);
});

// --- lifecycle instruction: build/test hand-off ---
// The Go launcher resolves the repository's configured commands into
// $MINOS_BUILD_CMD and $MINOS_TEST_CMD (internal/shell/spawn.go). The lead can
// only run those exact commands if the instruction binds the obligation to
// those named variables — not to a vague "commands the repository configures"
// the lead would otherwise satisfy with a repository-native guess. These guards
// keep the two variable names and the exact-to-completion-before-review ordering
// from silently disappearing from the instruction.
// A single predicate over an instruction document. It isolates the build/test
// step (step 3, bounded before step 4) so a clause cannot drift in from an
// unrelated step, then requires *each* command variable to carry all four
// obligations — exact command string, no repository-native substitute, run to
// completion before review, and empty-means-unconfigured-skip — inside that
// variable's own bounded sentence, so a document cannot satisfy the check with
// prose that scopes those obligations to one command only. It separately
// requires both command variables to precede the review-workflow step. It
// returns the list of failed checks, so a compliant document yields [].
//
// Phrase regexes run against a whitespace-flattened copy of the bounded step so
// a Markdown line wrap between two words never causes a false failure (the
// pitfall the recommission verifier hit while calibrating), while staying tight
// — no broad `.*` spanning unrelated clauses.
const COMMAND_VARS = ["$MINOS_BUILD_CMD", "$MINOS_TEST_CMD"];

// The obligations every configured command must carry, checked within that
// command's own bounded sentence. `[a-z]+` in the empty-skip clause matches the
// command kind ("build"/"test") without spanning into a neighbouring clause.
const COMMAND_CLAUSES = [
  ["exact command string", /is non-empty, run that exact command string/],
  ["no repository-native substitute", /never a repository-native guess or substitute/],
  ["run to completion before review", /to completion before any review/],
  ["empty means unconfigured, skip it", /empty, no [a-z]+ command is configured, so skip it/],
];

// The bounded sentence introduced by "When `$VAR`", up to the next command's
// sentence or the end of the step — so each variable's obligations are read in
// isolation from the other's.
function commandSentence(step3, variable) {
  const start = step3.indexOf("When `" + variable + "`");
  if (start === -1) return "";
  let end = step3.length;
  for (const other of COMMAND_VARS) {
    if (other === variable) continue;
    const otherStart = step3.indexOf("When `" + other + "`");
    if (otherStart > start && otherStart < end) end = otherStart;
  }
  return step3.slice(start, end);
}

function lifecycleCommandBindingFailures(text) {
  const failures = [];

  // Step 3 body, bounded strictly before the "4." step marker.
  const stepMatch = text.match(/\n3\. ([\s\S]*?)\n4\. /);
  if (!stepMatch) {
    failures.push("step 3 not found bounded before step 4");
    return failures;
  }
  const step3 = stepMatch[1].replace(/\s+/g, " ");

  // Each command variable must carry every obligation in its own sentence.
  for (const variable of COMMAND_VARS) {
    const sentence = commandSentence(step3, variable);
    if (!sentence) {
      failures.push(`${variable}: no bounded obligation sentence in step 3`);
      continue;
    }
    for (const [name, re] of COMMAND_CLAUSES) {
      if (!re.test(sentence)) failures.push(`${variable}: missing ${name}`);
    }
  }

  // Both command variables must precede the review-workflow step, compared
  // across the whole document — not only within the bounded block.
  const workflowIdx = text.indexOf("$MINOS_REVIEW_WORKFLOW");
  if (workflowIdx === -1) failures.push("review-workflow step not found");
  for (const variable of COMMAND_VARS) {
    const idx = text.indexOf(variable);
    if (idx === -1 || workflowIdx === -1 || idx >= workflowIdx)
      failures.push(`${variable}: not before the review-workflow step`);
  }

  return failures;
}

test("the lifecycle binds every build/test obligation to each of $MINOS_BUILD_CMD and $MINOS_TEST_CMD, before review", () => {
  assert.deepEqual(
    lifecycleCommandBindingFailures(lifecycle),
    [],
    "each command variable must carry its exact-string, no-substitute, to-completion-before-review and empty-skip obligation in its own step-3 sentence, and both must precede the review-workflow step"
  );
});

test("relocating $MINOS_TEST_CMD after the review workflow fails the binding predicate", () => {
  // Mirror the composed verifier's first false-green demonstration: keep the
  // build obligation inside step 3, but move the test variable to *after* the
  // review-workflow step. The predicate must reject it on ordering.
  const adversarial = lifecycle
    .replace(/\$MINOS_TEST_CMD/g, "$MINOS_QA_CMD")
    .replace(
      /(\n4\. Run the review workflow at `\$MINOS_REVIEW_WORKFLOW`[^\n]*)/,
      "$1\n   Then, when `$MINOS_TEST_CMD` is non-empty, run that exact command string."
    );
  // Sanity-check the mutation actually placed the test variable after the step.
  assert.ok(
    adversarial.indexOf("$MINOS_TEST_CMD") > adversarial.indexOf("$MINOS_REVIEW_WORKFLOW"),
    "the adversarial document must place $MINOS_TEST_CMD after the workflow step"
  );
  const failures = lifecycleCommandBindingFailures(adversarial);
  assert.ok(
    failures.includes("$MINOS_TEST_CMD: not before the review-workflow step"),
    `predicate must catch the relocated test obligation; got: ${failures.join(", ")}`
  );
});

test("scoping no-substitute/completion/empty-skip to $MINOS_BUILD_CMD only fails the binding predicate", () => {
  // Mirror the composed verifier's second false-green demonstration: retain both
  // variable names, both exact-string obligations and both orderings, but strip
  // the no-substitute / to-completion / empty-skip clauses from the *test*
  // command's sentence — i.e. state those three obligations for build only. The
  // per-command predicate must reject it.
  const buildOnly = lifecycle.replace(
    /When `\$MINOS_TEST_CMD` is non-empty,[\s\S]*?inventing one\./,
    "When `$MINOS_TEST_CMD` is non-empty, run that exact command string."
  );
  const flat = buildOnly.replace(/\s+/g, " ");
  // The mutation must keep the test name, its exact obligation and its ordering,
  // so the only thing lost is the three clauses' binding to the test command.
  assert.ok(
    /\$MINOS_TEST_CMD` is non-empty, run that exact command string/.test(flat),
    "test exact-string obligation must survive the mutation"
  );
  assert.ok(
    buildOnly.indexOf("$MINOS_TEST_CMD") < buildOnly.indexOf("$MINOS_REVIEW_WORKFLOW"),
    "test ordering must survive the mutation"
  );
  const failures = lifecycleCommandBindingFailures(buildOnly);
  assert.ok(
    failures.some((f) => f.startsWith("$MINOS_TEST_CMD: missing")),
    `predicate must catch obligations dropped from the test command; got: ${failures.join(", ")}`
  );
});
