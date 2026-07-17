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
    return review
      ? review(label, prompt, opts)
      : {
          findings: [
            { title: `finding from ${label}`, priority: "high", path: "internal/x.go", line: 3, explanation: "e" },
          ],
        };
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

test("a brief whose subfolder matches no repo directory runs repo-wide", async () => {
  const { result } = await runScript(
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
  assert.equal(result.briefs[0].status, "run");
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

const briefScopeResponder = (frontmatter, fileCount) =>
  responder({
    scout: scoutFixture({ briefs: [brief(".review/pkg/style.md", frontmatter)] }),
    scopes: { scopes: [{ scope: "pkg", files: filesNamed(fileCount) }] },
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

test("a whole-tree brief larger than one reviewer can hold is reported not run, never passed", async () => {
  const { result, calls } = await runScript(
    ARGS,
    briefScopeResponder("extent: full\nsweep: whole-tree", 90)
  );
  const report = result.briefs.find((b) => b.brief === ".review/pkg/style.md");
  assert.equal(report.status, "not-run");
  assert.equal(report.scopeSize, 90);
  assert.match(report.reason, /90 files/);
  // Not dispatched, and never silently treated as passed: it withholds
  // verdict-completeness so the lead cannot publish a clean result over it.
  assert.equal(calls.filter((c) => (c.opts.label || "").startsWith("brief-")).length, 0);
  assert.equal(result.verdictsComplete, false);
  assert.ok(result.incomplete.some((line) => line.includes("not run")));
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
