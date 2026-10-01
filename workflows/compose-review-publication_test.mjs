import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import test from "node:test";
import { spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import { issueLogEntry } from "./finding-presentation.mjs";

// The composer is the one step between validated verdicts and what leaves
// the run. These tests drive the real CLI over fixture verdicts and
// decisions and read back the files it writes: the plan's order and verdict
// arguments are the publication contract the lifecycle posts without
// judgement, and the triage entries are what the issue-log filing delivers.

const cliPath = fileURLToPath(new URL("./compose-review-publication.mjs", import.meta.url));

function finding(id, overrides = {}) {
  return {
    id,
    source: "Correctness",
    title: `Finding ${id}`,
    severity: "High",
    confidence: 86,
    verifierConfidence: 94,
    path: "internal/review.go",
    line: 42,
    explanation: `Explanation for ${id}.`,
    proposingModel: { pinnedModel: "gpt-6-sol", resolvedModel: "gpt-6-sol" },
    verifyingModel: { pinnedModel: "claude-opus-5-5", resolvedModel: "claude-opus-5-5" },
    ...overrides,
  };
}

function verdict(confirmedFindings, overrides = {}) {
  return {
    status: "complete",
    complete: true,
    reviewed: { target: "0981206c9876fed", head: "2c518981234abcd", occasion: null },
    incomplete: [],
    confirmedFindings,
    ran: [],
    skipped: [],
    misconfigurations: [],
    outOfScopeObservations: [],
    ...overrides,
  };
}

function decision(verdictName, findings) {
  return {
    kind: "minos-verdict-decision-v1",
    verdict: verdictName,
    basis: "fixture decision",
    findings,
  };
}

function compose(t, { main, mainDecision, brief, briefDecision, threshold = "High", orientation = { repository: "/run/minos/review-17/workspace", source: { owner: "owner", repo: "repo" }, misconfigurations: [] } }) {
  const scratch = mkdtempSync(join(tmpdir(), "compose-review-publication-"));
  t.after(() => rmSync(scratch, { recursive: true, force: true }));
  const write = (name, value) => {
    const path = join(scratch, name);
    writeFileSync(path, JSON.stringify(value));
    return path;
  };
  const args = [cliPath, join(scratch, "out"), write("orientation.json", orientation), threshold, write("main.json", main), write("main-decision.json", mainDecision)];
  if (brief) args.push(write("brief.json", brief), write("brief-decision.json", briefDecision));
  const result = spawnSync(process.execPath, args, { encoding: "utf8" });
  const plan = result.status === 0 ? JSON.parse(result.stdout) : null;
  return { result, plan, scratch, read: (path) => readFileSync(path, "utf8"), readJson: (path) => JSON.parse(readFileSync(path, "utf8")) };
}

function posts(plan) {
  return plan.posts.map(({ review, verdict: argument }) => ({ review, verdict: argument }));
}

test("a blocking round publishes all confirmed severities and files no findings", (t) => {
  const confirmed = [finding("a"), finding("b", { severity: "Medium", line: 7 }), finding("c", { severity: "Low", line: 9 })];
  const { result, plan, read, readJson } = compose(t, {
    main: verdict(confirmed, { outOfScopeObservations: [{ title: "Unverified leak", verified: false }] }),
    mainDecision: decision("request-changes", confirmed.map((f) => ({ key: f.id, gating: f.id === "a" }))),
  });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(posts(plan), [{ review: "main", verdict: "request-changes" }]);
  const comments = readJson(plan.posts[0].comments);
  assert.equal(comments.length, 3);
  assert.match(comments[0].body, /Blocking · High/);
  assert.match(comments[1].body, /Advisory · Medium/);
  assert.match(comments[2].body, /Advisory · Low/);
  assert.match(read(plan.posts[0].body), /1 blocking finding.*1 High/);
  assert.match(read(plan.posts[0].body), /2 advisory findings included/);
  assert.doesNotMatch(JSON.stringify(comments), /Unverified leak/);
  assert.deepEqual(readJson(plan.triage.entries), []);
});

for (const severities of [[], ["Medium", "Low"]]) {
  test(`a clean round with ${severities.length} findings writes no review and files all confirmed findings`, (t) => {
    const confirmed = severities.map((severity, i) => finding(String(i), { severity }));
    const { result, plan, readJson, scratch } = compose(t, {
      main: verdict(confirmed, { outOfScopeObservations: [{ title: "Unverified leak", verified: false }] }),
      mainDecision: decision("clean", confirmed.map((f) => ({ key: f.id, gating: false }))),
    });
    assert.equal(result.status, 0, result.stderr);
    assert.equal(plan.verdict, "clean");
    assert.deepEqual(plan.posts, []);
    assert.equal(existsSync(join(scratch, "out", "main-review.md")), false);
    const entries = readJson(plan.triage.entries);
    assert.deepEqual(entries.map((entry) => entry.severity), severities);
    assert.ok(entries.every((entry) => entry.kind === "advisory-finding"));
    assert.doesNotMatch(JSON.stringify(entries), /Unverified leak/);
  });
}

for (const gatingGroup of ["main", "brief"]) {
  test(`a blocker in ${gatingGroup} publishes verified findings from both groups in one request-changes review`, (t) => {
    const { result, plan, readJson, read } = compose(t, {
      main: verdict([finding("a", { severity: gatingGroup === "main" ? "High" : "Low" })]),
      mainDecision: decision(gatingGroup === "main" ? "request-changes" : "clean", [{ key: "a", gating: gatingGroup === "main" }]),
      brief: verdict([finding("b", { severity: gatingGroup === "brief" ? "High" : "Medium", line: 90 })], {
        ran: [{ brief: ".review/money.md" }], outOfScopeObservations: [{ title: "Brief observation", verified: false }],
      }),
      briefDecision: decision(gatingGroup === "brief" ? "request-changes" : "clean", [{ key: "b", gating: gatingGroup === "brief" }]),
    });
    assert.equal(result.status, 0, result.stderr);
    assert.deepEqual(posts(plan), [{ review: "main", verdict: "request-changes" }]);
    assert.equal(readJson(plan.posts[0].comments).length, 2);
    assert.match(read(plan.posts[0].body), /Briefs applied: `.review\/money.md`/);
    assert.deepEqual(readJson(plan.triage.entries), []);
  });
}

test("clean main and brief findings go to the filing destination without observations", (t) => {
  const { result, plan, readJson } = compose(t, {
    main: verdict([finding("a", { severity: "Medium" })]),
    mainDecision: decision("clean", [{ key: "a", gating: false }]),
    brief: verdict([finding("b", { severity: "Low" })], {
      outOfScopeObservations: [{ title: "Unverified", verified: false }],
      misconfigurations: [{ kind: "missing-scope", title: "Missing directory", brief: ".review/a.md", reason: "absent" }],
    }),
    briefDecision: decision("clean", [{ key: "b", gating: false }]),
  });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(plan.posts, []);
  assert.deepEqual(readJson(plan.triage.entries).map((entry) => entry.kind), ["advisory-finding", "advisory-finding", "review-brief-misconfiguration"]);
});

// Shaped as setup-workspace writes it: `repository` is the workspace path,
// the forge identity lives under `source`.
const unreadableGuidance = {
  repository: "/run/minos/review-17/workspace",
  source: { owner: "owner", repo: "repo", pr: 4, date: "2026-10-01" },
  misconfigurations: [{ kind: "guidance-source", source: { repository: "owner/guidance", path: "docs/INTENT.md" }, reason: "is missing or empty" }],
};

for (const verdictName of ["clean", "request-changes"]) {
  test(`an unreadable guidance source is filed as a configuration diagnostic on a ${verdictName} outcome, never as a finding`, (t) => {
    const confirmed = [finding("a", { severity: verdictName === "clean" ? "Medium" : "High" })];
    const { result, plan, read, readJson } = compose(t, {
      main: verdict(confirmed),
      mainDecision: decision(verdictName, [{ key: "a", gating: verdictName === "request-changes" }]),
      orientation: unreadableGuidance,
    });
    assert.equal(result.status, 0, result.stderr);
    const guidance = readJson(plan.triage.entries).filter((entry) => entry.kind === "guidance-source-misconfiguration");
    assert.deepEqual(guidance, [{
      kind: "guidance-source-misconfiguration", repository: "owner/repo",
      sourceRepository: "owner/guidance", path: "docs/INTENT.md", reason: "is missing or empty",
    }]);
    assert.equal(plan.triage.guidanceMisconfigurations, 1);
    assert.match(issueLogEntry(guidance[0], "(PR #4)"),
      /^- \*\*Guidance source misconfiguration: owner\/guidance:docs\/INTENT\.md\*\* — configured guidance for owner\/repo could not be read: is missing or empty\. \(PR #4\)\.$/);
    for (const post of plan.posts) {
      assert.doesNotMatch(read(post.body), /INTENT\.md/);
      assert.doesNotMatch(read(post.comments), /INTENT\.md/);
    }
    assert.equal(plan.verdict, verdictName);
  });
}

test("a malformed orientation misconfiguration stops the composer with nothing written", (t) => {
  const { result, scratch } = compose(t, {
    main: verdict([]),
    mainDecision: decision("clean", []),
    orientation: { ...unreadableGuidance, misconfigurations: [{ kind: "guidance-source", reason: "no source" }] },
  });
  assert.equal(result.status, 1);
  assert.equal(existsSync(join(scratch, "out")), false);
});

test("a local guidance source's diagnostic names the reviewed repository, never the workspace path", (t) => {
  const { result, plan, readJson } = compose(t, {
    main: verdict([]),
    mainDecision: decision("clean", []),
    orientation: { ...unreadableGuidance, misconfigurations: [{ kind: "guidance-source", source: { path: "docs/INTENT.md" }, reason: "is missing or empty" }] },
  });
  assert.equal(result.status, 0, result.stderr);
  const [entry] = readJson(plan.triage.entries);
  assert.equal(entry.repository, "owner/repo");
  assert.match(issueLogEntry(entry, "(PR #4)"), /^- \*\*Guidance source misconfiguration: owner\/repo:docs\/INTENT\.md\*\* — configured guidance for owner\/repo /);
  assert.doesNotMatch(issueLogEntry(entry, "(PR #4)"), /\/run\/minos/);
});

for (const [label, contents] of [["unreadable", null], ["invalid JSON", "{nope"]]) {
  test(`an ${label} orientation stops the composer with exit 1 and nothing written`, (t) => {
    const scratch = mkdtempSync(join(tmpdir(), "compose-review-publication-"));
    t.after(() => rmSync(scratch, { recursive: true, force: true }));
    const orientationPath = join(scratch, "orientation.json");
    if (contents !== null) writeFileSync(orientationPath, contents);
    const main = join(scratch, "main.json");
    const mainDecision = join(scratch, "main-decision.json");
    writeFileSync(main, JSON.stringify(verdict([])));
    writeFileSync(mainDecision, JSON.stringify(decision("clean", [])));
    const result = spawnSync(process.execPath, [cliPath, join(scratch, "out"), orientationPath, "High", main, mainDecision], { encoding: "utf8" });
    assert.equal(result.status, 1, result.stderr);
    assert.match(result.stderr, /orientation is missing or not valid JSON/);
    assert.equal(existsSync(join(scratch, "out")), false);
  });
}

test("duplicate findings merge once with the higher severity and either group's gating decision", (t) => {
  const { result, plan, readJson } = compose(t, {
    main: verdict([finding("a", { severity: "Medium", title: "Lost update" })]),
    mainDecision: decision("clean", [{ key: "a", gating: false }]),
    brief: verdict([finding("b", { title: "Lost update", source: "Money safety" })]),
    briefDecision: decision("request-changes", [{ key: "b", gating: true }]),
  });
  assert.equal(result.status, 0, result.stderr);
  const comments = readJson(plan.posts[0].comments);
  assert.equal(comments.length, 1);
  assert.match(comments[0].body, /Blocking · High: Lost update/);
  assert.match(comments[0].body, /Also raised by: Money safety/);
  assert.deepEqual(readJson(plan.triage.entries), []);
});

test("distinct findings on one line cross-reference each other", (t) => {
  const { plan, readJson } = compose(t, {
    main: verdict([finding("a", { title: "Lost update" }), finding("b", { title: "Unchecked error" })]),
    mainDecision: decision("request-changes", [{ key: "a", gating: true }, { key: "b", gating: true }]),
  });
  const comments = readJson(plan.posts[0].comments);
  assert.match(comments[0].body, /See also on this line: "Unchecked error"/);
  assert.match(comments[1].body, /See also on this line: "Lost update"/);
});

test("an id-less verdict uses its content key", (t) => {
  const { id, ...idless } = finding("a");
  const { result, plan } = compose(t, {
    main: verdict([idless]),
    mainDecision: decision("request-changes", [{ key: JSON.stringify(["internal/review.go", 42, "finding a"]), gating: true }]),
  });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(plan.verdict, "request-changes");
});

test("invalid and incomplete verdicts produce no publication", (t) => {
  for (const main of [verdict([finding("a")]), verdict([], { status: "incomplete" })]) {
    const { result, scratch } = compose(t, { main, mainDecision: decision("clean", []) });
    assert.equal(result.status, 1);
    assert.equal(existsSync(join(scratch, "out")), false);
  }
});

test("argument errors exit 2 with usage", () => {
  const result = spawnSync(process.execPath, [cliPath, "only-one"], { encoding: "utf8" });
  assert.equal(result.status, 2);
  assert.match(result.stderr, /^usage: node workflows\/compose-review-publication/);
});
