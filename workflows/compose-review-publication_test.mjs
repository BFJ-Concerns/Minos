import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import test from "node:test";
import { spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

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
    proposingModel: { pinnedModel: "gpt-5.6-terra", resolvedModel: "gpt-5.6-terra" },
    verifyingModel: { pinnedModel: "claude-opus-5", resolvedModel: "claude-opus-5" },
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

function compose(t, { main, mainDecision, brief, briefDecision, threshold = "High" }) {
  const scratch = mkdtempSync(join(tmpdir(), "compose-review-publication-"));
  const write = (name, value) => {
    const path = join(scratch, name);
    writeFileSync(path, JSON.stringify(value));
    return path;
  };
  const args = [cliPath, join(scratch, "out"), threshold, write("main.json", main), write("main-decision.json", mainDecision)];
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

test("clean main and brief findings go to the annexe without observations", (t) => {
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
