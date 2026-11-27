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
    proposingModel: { pinnedModel: "gpt-6.1-sol", resolvedModel: "gpt-6.1-sol" },
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

const OBSERVATION = { title: "Unverified leak", path: "internal/legacy.go", line: 9, explanation: "Not verified.", verified: false };

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
  const env = Object.fromEntries(Object.entries(process.env).filter(([name]) => !/^(MINOS_|ENSEMBLE_)/.test(name)));
  const result = spawnSync(process.execPath, args, { encoding: "utf8", env });
  const plan = result.status === 0 ? JSON.parse(result.stdout) : null;
  return { result, plan, scratch, read: (path) => readFileSync(path, "utf8"), readJson: (path) => JSON.parse(readFileSync(path, "utf8")) };
}

function posts(plan) {
  return plan.posts.map(({ review, verdict: argument }) => ({ review, verdict: argument }));
}

test("a blocking round posts only what blocks and files the rest", (t) => {
  const confirmed = [finding("a"), finding("b", { severity: "Medium", line: 7 }), finding("c", { severity: "Low", line: 9 })];
  const { result, plan, read, readJson } = compose(t, {
    main: verdict(confirmed, { outOfScopeObservations: [OBSERVATION] }),
    mainDecision: decision("request-changes", confirmed.map((f) => ({ key: f.id, gating: f.id === "a" }))),
  });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(posts(plan), [{ review: "main", verdict: "request-changes" }]);
  const comments = readJson(plan.posts[0].comments);
  assert.equal(comments.length, 1);
  assert.match(comments[0].body, /Blocking · High/);
  assert.match(read(plan.posts[0].body), /1 blocking finding.*1 High/);
  assert.doesNotMatch(read(plan.posts[0].body), /advisory/i);
  assert.doesNotMatch(JSON.stringify(comments), /Unverified leak/);
  const entries = readJson(plan.triage.entries);
  assert.deepEqual(entries.map((entry) => [entry.kind, entry.severity]),
    [["advisory-finding", "Medium"], ["advisory-finding", "Low"], ["out-of-scope-observation", undefined]]);
  assert.deepEqual(plan.triage, { entries: plan.triage.entries, advisory: 2, unsettled: 0, observations: 1, misconfigurations: 0, guidanceMisconfigurations: 0 });
});

test("an unsettled finding is filed as an open question and never posted or classified", (t) => {
  const confirmed = [finding("a")];
  const unsettled = {
    ...finding("u", { line: 77, severity: "Medium", title: "Stream lookup treats a 404 as empty" }),
    verdict: "unsettled",
    outsideFact: "The service answers 404 for a stream that has no messages.",
    wouldSettle: "The service's API documentation for the stream endpoint.",
  };
  const elsewhere = { ...unsettled, id: "v", line: 42, title: "Second question at a confirmed site" };
  const { result, plan, readJson } = compose(t, {
    main: verdict(confirmed, { unsettledFindings: [unsettled, elsewhere] }),
    mainDecision: decision("request-changes", [{ key: "a", gating: true }]),
  });
  assert.equal(result.status, 0, result.stderr);
  const comments = readJson(plan.posts[0].comments);
  assert.equal(comments.length, 1);
  assert.doesNotMatch(JSON.stringify(comments), /Stream lookup/);
  const entries = readJson(plan.triage.entries);
  assert.deepEqual(entries, [{
    kind: "unsettled-finding",
    title: "Stream lookup treats a 404 as empty",
    severity: "Medium",
    path: "internal/review.go",
    line: 77,
    explanation: "Explanation for u.",
    outsideFact: "The service answers 404 for a stream that has no messages.",
    wouldSettle: "The service's API documentation for the stream endpoint.",
  }]);
  assert.equal(plan.triage.unsettled, 1);
  const filed = issueLogEntry(entries[0], "Filed by Minos");
  assert.match(filed, /^- \*\*Unsettled · Medium: Stream lookup treats a 404 as empty\*\*/);
  assert.match(filed, /neither confirm nor refute/);
  assert.match(filed, /not a confirmed defect/);
  assert.match(filed, /What would settle it: The service's API documentation/);
  assert.doesNotMatch(filed, /Advisory|Blocking/);
});

for (const severities of [[], ["Medium", "Low"]]) {
  test(`a clean round with ${severities.length} findings writes no review and files all confirmed findings`, (t) => {
    const confirmed = severities.map((severity, i) => finding(String(i), { severity }));
    const { result, plan, readJson, scratch } = compose(t, {
      main: verdict(confirmed),
      mainDecision: decision("clean", confirmed.map((f) => ({ key: f.id, gating: false }))),
    });
    assert.equal(result.status, 0, result.stderr);
    assert.equal(plan.verdict, "clean");
    assert.deepEqual(plan.posts, []);
    assert.equal(existsSync(join(scratch, "out", "main-review.md")), false);
    const entries = readJson(plan.triage.entries);
    assert.deepEqual(entries.map((entry) => entry.severity), severities);
    assert.ok(entries.every((entry) => entry.kind === "advisory-finding"));
  });
}

for (const gatingGroup of ["main", "brief"]) {
  test(`a blocker in ${gatingGroup} posts that group's finding and files the other group's advice`, (t) => {
    const { result, plan, readJson, read } = compose(t, {
      main: verdict([finding("a", { severity: gatingGroup === "main" ? "High" : "Low" })]),
      mainDecision: decision(gatingGroup === "main" ? "request-changes" : "clean", [{ key: "a", gating: gatingGroup === "main" }]),
      brief: verdict([finding("b", { severity: gatingGroup === "brief" ? "High" : "Medium", line: 90 })], {
        ran: [{ brief: ".review/money.md" }],
      }),
      briefDecision: decision(gatingGroup === "brief" ? "request-changes" : "clean", [{ key: "b", gating: gatingGroup === "brief" }]),
    });
    assert.equal(result.status, 0, result.stderr);
    assert.deepEqual(posts(plan), [{ review: "main", verdict: "request-changes" }]);
    const comments = readJson(plan.posts[0].comments);
    assert.equal(comments.length, 1);
    assert.match(comments[0].body, /Blocking · High/);
    assert.match(read(plan.posts[0].body), /Briefs applied: `.review\/money.md`/);
    assert.deepEqual(readJson(plan.triage.entries).map((entry) => [entry.kind, entry.id]),
      [["advisory-finding", gatingGroup === "main" ? "b" : "a"]]);
  });
}

test("observations from both groups are filed as unverified, except where a confirmed finding stands at the site", (t) => {
  const { result, plan, readJson } = compose(t, {
    main: verdict([finding("a", { severity: "Medium" })], {
      outOfScopeObservations: [OBSERVATION, { ...OBSERVATION, title: "Restates the finding", path: "internal/review.go", line: 42 }],
    }),
    mainDecision: decision("clean", [{ key: "a", gating: false }]),
    brief: verdict([finding("b", { severity: "Low", line: 90 })], {
      outOfScopeObservations: [{ ...OBSERVATION, title: "Brief observation", line: 12, observingLabel: "specialist-2", id: "x:observation:1" }],
      misconfigurations: [{ kind: "missing-scope", title: "Missing directory", brief: ".review/a.md", reason: "absent" }],
    }),
    briefDecision: decision("clean", [{ key: "b", gating: false }]),
  });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(plan.posts, []);
  const entries = readJson(plan.triage.entries);
  assert.deepEqual(entries.map((entry) => entry.kind),
    ["advisory-finding", "advisory-finding", "out-of-scope-observation", "out-of-scope-observation", "review-brief-misconfiguration"]);
  assert.deepEqual(entries.filter((entry) => entry.kind === "out-of-scope-observation"), [
    { kind: "out-of-scope-observation", title: "Unverified leak", path: "internal/legacy.go", line: 9, explanation: "Not verified." },
    { kind: "out-of-scope-observation", title: "Brief observation", path: "internal/legacy.go", line: 12, explanation: "Not verified." },
  ]);
});

test("a finding classed as speculative hardening reaches no review and no filing", (t) => {
  const confirmed = [finding("a"), finding("b", { severity: "Medium", line: 7 }), finding("c", { line: 9 })];
  const { result, plan, readJson } = compose(t, {
    main: verdict(confirmed),
    mainDecision: decision("request-changes", [
      { key: "a", gating: true },
      { key: "b", gating: false, class: "speculative-hardening" },
      { key: "c", gating: false, class: "speculative-hardening" },
    ]),
  });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(readJson(plan.posts[0].comments).length, 1);
  assert.deepEqual(readJson(plan.triage.entries), []);
  assert.equal(plan.speculativeHardening, 2);
  assert.equal(plan.alreadyRaised, 0);
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
    brief: verdict([finding("b", { title: "  LOST   update  ", source: "Money safety" })]),
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

test("a blocking comment does not point at a finding that is filed and not posted", (t) => {
  const { plan, readJson } = compose(t, {
    main: verdict([finding("a", { title: "Lost update" }), finding("b", { severity: "Low", title: "Unchecked error" })]),
    mainDecision: decision("request-changes", [{ key: "a", gating: true }, { key: "b", gating: false }]),
  });
  const comments = readJson(plan.posts[0].comments);
  assert.equal(comments.length, 1);
  assert.doesNotMatch(comments[0].body, /See also on this line/);
});

test("findings the decisions label as one defect merge across groups and sites into the highest-severity member", (t) => {
  const { result, plan, readJson } = compose(t, {
    main: verdict([finding("a", { severity: "Medium", title: "MinInt64 overflows Split", line: 83 })]),
    mainDecision: decision("request-changes", [{ key: "a", gating: true, defect: "minint-overflow", undergrade: "the overflow reaches money arithmetic" }]),
    brief: verdict([
      finding("b", { severity: "Low", title: "Split rejects the minimum value", line: 75, source: "Conservation" }),
      finding("c", { severity: "Low", title: "Remainder sign on MinInt64", line: 73, source: "Contracts" }),
    ]),
    briefDecision: decision("request-changes", [
      { key: "b", gating: true, defect: "minint-overflow", undergrade: "the overflow reaches money arithmetic" },
      { key: "c", gating: true, defect: "minint-overflow", undergrade: "the overflow reaches money arithmetic" },
    ]),
  });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(plan.verdict, "request-changes");
  const comments = readJson(plan.posts[0].comments);
  assert.equal(comments.length, 1);
  assert.equal(comments[0].line, 83);
  assert.match(comments[0].body, /Blocking · Medium: MinInt64 overflows Split/);
  assert.match(comments[0].body, /Also raised by: Contracts, Conservation\./);
  assert.match(comments[0].body, /Also at: `internal\/review\.go:73`, `internal\/review\.go:75`\./);
  assert.doesNotMatch(comments[0].body, /See also on this line/);
});

test("a labelled defect at one site under two titles merges where the site-and-title rule cannot", (t) => {
  const { plan, readJson } = compose(t, {
    main: verdict([
      finding("a", { title: "Negative total loses pence", line: 37 }),
      finding("b", { title: "Pence lost when the total is below zero", line: 37, source: "Conservation" }),
    ]),
    mainDecision: decision("request-changes", [
      { key: "a", gating: true, defect: "negative-total-pence" },
      { key: "b", gating: true, defect: "negative-total-pence" },
    ]),
  });
  const comments = readJson(plan.posts[0].comments);
  assert.equal(comments.length, 1);
  assert.match(comments[0].body, /Blocking · High: Negative total loses pence/);
  assert.match(comments[0].body, /Also raised by: Conservation\./);
  assert.doesNotMatch(comments[0].body, /Also at/);
  assert.doesNotMatch(comments[0].body, /See also on this line/);
});

test("a label whose findings gate differently across the two decisions stops the composer with nothing written", (t) => {
  const { result, scratch } = compose(t, {
    main: verdict([finding("a", { title: "MinInt64 overflows Split", line: 83 })]),
    mainDecision: decision("request-changes", [{ key: "a", gating: true, defect: "minint-overflow" }]),
    brief: verdict([finding("b", { severity: "Low", title: "Split rejects the minimum value", line: 75, source: "Conservation" })]),
    briefDecision: decision("clean", [{ key: "b", gating: false, defect: "minint-overflow" }]),
  });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /labelled one defect gate differently across the decisions: minint-overflow/);
  assert.equal(existsSync(join(scratch, "out", "publication-plan.json")), false);
});

test("a labelled brief twin merged by site and title still pulls its labelled sibling in", (t) => {
  const { result, plan, readJson } = compose(t, {
    main: verdict([finding("a", { title: "Lost update", line: 42 })]),
    mainDecision: decision("request-changes", [{ key: "a", gating: true }]),
    brief: verdict([
      finding("b", { title: "lost update", line: 42, source: "Money safety" }),
      finding("c", { title: "Update dropped under contention", line: 90, source: "Money safety" }),
    ]),
    briefDecision: decision("request-changes", [
      { key: "b", gating: true, defect: "lost-update" },
      { key: "c", gating: true, defect: "lost-update" },
    ]),
  });
  assert.equal(result.status, 0, result.stderr);
  const comments = readJson(plan.posts[0].comments);
  assert.equal(comments.length, 1);
  assert.equal(comments[0].line, 42);
  assert.match(comments[0].body, /Also raised by: Money safety\./);
  assert.match(comments[0].body, /Also at: `internal\/review\.go:90`\./);
});

for (const className of ["declared-out-of-scope", "speculative-hardening"])
  for (const classedGroup of ["main", "brief"])
    test(`a gating finding is posted though its ${classedGroup} twin was classed ${className}`, (t) => {
      const classed = { gating: false, class: className };
      const gating = { gating: true };
      const { result, plan, readJson } = compose(t, {
        main: verdict([finding("a", { title: "Lost update" })]),
        mainDecision: decision(classedGroup === "main" ? "clean" : "request-changes", [{ key: "a", ...(classedGroup === "main" ? classed : gating) }]),
        brief: verdict([finding("b", { title: "Lost update", source: "Money safety" })]),
        briefDecision: decision(classedGroup === "brief" ? "clean" : "request-changes", [{ key: "b", ...(classedGroup === "brief" ? classed : gating) }]),
      });
      assert.equal(result.status, 0, result.stderr);
      assert.deepEqual(posts(plan), [{ review: "main", verdict: "request-changes" }]);
      const comments = readJson(plan.posts[0].comments);
      assert.equal(comments.length, 1);
      assert.match(comments[0].body, /Blocking · High: Lost update/);
      assert.equal(plan.alreadyRaised, 0);
      assert.equal(plan.speculativeHardening, 0);
      assert.deepEqual(readJson(plan.triage.entries), []);
    });

for (const raisedGroup of ["main", "brief"])
  test(`a gating finding whose ${raisedGroup} twin was judged already raised stops the composer with nothing written`, (t) => {
    const raised = { gating: false, class: "already-raised" };
    const gating = { gating: true };
    const { result, scratch } = compose(t, {
      main: verdict([finding("a", { title: "Lost update" })]),
      mainDecision: decision(raisedGroup === "main" ? "clean" : "request-changes", [{ key: "a", ...(raisedGroup === "main" ? raised : gating) }]),
      brief: verdict([finding("b", { title: "Lost update", source: "Money safety" })]),
      briefDecision: decision(raisedGroup === "brief" ? "clean" : "request-changes", [{ key: "b", ...(raisedGroup === "brief" ? raised : gating) }]),
    });
    assert.equal(result.status, 1);
    assert.match(result.stderr, /one finding at internal\/review\.go:42 is already raised in one decision and new in the other/);
    assert.equal(existsSync(join(scratch, "out", "publication-plan.json")), false);
  });

test("brief twins of one main finding merge to one result in either listing order", (t) => {
  const briefTwins = [
    finding("b", { severity: "Critical", title: "Lost update", source: "Money safety" }),
    finding("c", { severity: "Medium", title: "lost  update", source: "Conservation" }),
  ];
  const dispositions = [{ key: "b", gating: true }, { key: "c", gating: false, class: "speculative-hardening" }];
  const outcomes = [briefTwins, [...briefTwins].reverse()].map((listed) => {
    const { result, plan, read, readJson } = compose(t, {
      main: verdict([finding("a", { severity: "Medium", title: "Lost update" })]),
      mainDecision: decision("clean", [{ key: "a", gating: false, class: "speculative-hardening" }]),
      brief: verdict(listed),
      briefDecision: decision("request-changes", listed === briefTwins ? dispositions : [...dispositions].reverse()),
    });
    assert.equal(result.status, 0, result.stderr);
    return { posts: posts(plan), comments: readJson(plan.posts[0].comments), body: read(plan.posts[0].body), triage: readJson(plan.triage.entries) };
  });
  assert.deepEqual(outcomes[0], outcomes[1]);
  assert.equal(outcomes[0].comments.length, 1);
  assert.match(outcomes[0].comments[0].body, /Blocking · Critical: Lost update/);
  assert.match(outcomes[0].comments[0].body, /Also raised by: Conservation, Money safety\./);
});

for (const order of ["listed", "reversed"])
  test(`brief twins classed apart from their main twin stop the composer whichever is listed first (${order})`, (t) => {
    const listed = [
      finding("b", { severity: "Medium", title: "Lost update", source: "Money safety" }),
      finding("c", { severity: "Medium", title: "Lost update", source: "Conservation" }),
    ];
    const dispositions = [{ key: "b", gating: false, class: "declared-out-of-scope" }, { key: "c", gating: false }];
    const { result } = compose(t, {
      main: verdict([finding("a", { severity: "Medium", title: "Lost update" })]),
      mainDecision: decision("clean", [{ key: "a", gating: false }]),
      brief: verdict(order === "listed" ? listed : [...listed].reverse()),
      briefDecision: decision("clean", order === "listed" ? dispositions : [...dispositions].reverse()),
    });
    assert.equal(result.status, 1);
    assert.match(result.stderr, /classed differently across the decisions: unclassed and declared-out-of-scope/);
  });

// Two main findings at one site and title describe one defect at one site,
// so whether the author already has it is one answer: a main decision that
// calls one already raised and the other new is refused, with or without a
// brief twin beside them, and the refusal says the main decision is the
// one to correct.
for (const order of ["raised first", "gating first"])
  for (const briefTwin of [false, true])
    test(`main findings at one site and title that disagree on whether the finding is new stop the composer (${order}, ${briefTwin ? "with" : "without"} a brief twin)`, (t) => {
      const mainFindings = [finding("a", { title: "Lost update" }), finding("b", { title: "Lost update" })];
      const mainDispositions = [{ key: "a", gating: false, class: "already-raised" }, { key: "b", gating: true }];
      const reversed = order === "gating first";
      const { result, scratch } = compose(t, {
        main: verdict(reversed ? [...mainFindings].reverse() : mainFindings),
        mainDecision: decision("request-changes", reversed ? [...mainDispositions].reverse() : mainDispositions),
        ...(briefTwin ? {
          brief: verdict([finding("c", { title: "Lost update", source: "Money safety" })]),
          briefDecision: decision("clean", [{ key: "c", gating: false, class: "already-raised" }]),
        } : {}),
      });
      assert.equal(result.status, 1);
      assert.equal(result.stderr, "main decision does not validate: findings at internal/review.go:42 under one title are already raised in one disposition and new in another\n");
      assert.equal(existsSync(join(scratch, "out", "publication-plan.json")), false);
    });

// A label's findings at one line under two titles are one defect at one
// site too: one already raised beside one that gates would post the defect
// again where the author already has it.
for (const order of ["raised first", "gating first"])
  for (const placement of ["one decision", "across the decisions"])
    test(`a label's findings at one line that disagree on whether the defect is new stop the composer (${placement}, ${order})`, (t) => {
      const raised = { found: finding("old", { title: "MinInt64 overflows Split", line: 83 }), disposition: { key: "old", gating: false, class: "already-raised", defect: "minint-overflow" } };
      const fresh = { found: finding("new", { title: "Split rejects the minimum value", line: 83, source: "Conservation" }), disposition: { key: "new", gating: true, defect: "minint-overflow" } };
      const [first, second] = order === "raised first" ? [raised, fresh] : [fresh, raised];
      const groups = placement === "one decision"
        ? { main: verdict([first.found, second.found]), mainDecision: decision("request-changes", [first.disposition, second.disposition]) }
        : {
          main: verdict([first.found]), mainDecision: decision(first === fresh ? "request-changes" : "clean", [first.disposition]),
          brief: verdict([second.found]), briefDecision: decision(second === fresh ? "request-changes" : "clean", [second.disposition]),
        };
      const { result, scratch } = compose(t, groups);
      assert.equal(result.status, 1);
      assert.match(result.stderr, placement === "one decision"
        ? /^main decision does not validate: findings labelled minint-overflow at internal\/review\.go:83 are already raised in one disposition and new in another$/m
        : /^findings labelled minint-overflow at internal\/review\.go:83 are already raised in one decision and new in the other$/m);
      assert.equal(existsSync(join(scratch, "out", "publication-plan.json")), false);
    });

test("advisory twins sharing a class keep it", (t) => {
  const { result, plan, readJson } = compose(t, {
    main: verdict([finding("a", { title: "Lost update" })]),
    mainDecision: decision("clean", [{ key: "a", gating: false, class: "already-raised" }]),
    brief: verdict([finding("b", { title: "Lost update", source: "Money safety" })]),
    briefDecision: decision("clean", [{ key: "b", gating: false, class: "already-raised" }]),
  });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(plan.posts, []);
  assert.equal(plan.alreadyRaised, 1);
  assert.deepEqual(readJson(plan.triage.entries), []);
});

for (const [mainClass, briefClass] of [["already-raised", undefined], [undefined, "speculative-hardening"], ["already-raised", "declared-out-of-scope"]])
  test(`advisory twins classed ${mainClass ?? "unclassed"} and ${briefClass ?? "unclassed"} stop the composer with nothing written`, (t) => {
    const { result, scratch } = compose(t, {
      main: verdict([finding("a", { severity: "Medium", title: "Lost update" })]),
      mainDecision: decision("clean", [{ key: "a", gating: false, ...(mainClass ? { class: mainClass } : {}) }]),
      brief: verdict([finding("b", { severity: "Medium", title: "Lost update", source: "Money safety" })]),
      briefDecision: decision("clean", [{ key: "b", gating: false, ...(briefClass ? { class: briefClass } : {}) }]),
    });
    assert.equal(result.status, 1);
    assert.match(result.stderr, new RegExp(`classed differently across the decisions: ${mainClass ?? "unclassed"} and ${briefClass ?? "unclassed"}`));
    assert.equal(existsSync(join(scratch, "out", "publication-plan.json")), false);
  });

test("a label whose findings are classed differently across the two decisions stops the composer with nothing written", (t) => {
  const { result, scratch } = compose(t, {
    main: verdict([finding("a", { severity: "Medium", title: "MinInt64 overflows Split", line: 83 })]),
    mainDecision: decision("clean", [{ key: "a", gating: false, class: "declared-out-of-scope", defect: "minint-overflow" }]),
    brief: verdict([finding("b", { severity: "Low", title: "Split rejects the minimum value", line: 75, source: "Conservation" })]),
    briefDecision: decision("clean", [{ key: "b", gating: false, defect: "minint-overflow" }]),
  });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /labelled one defect are classed differently across the decisions: minint-overflow/);
  assert.equal(existsSync(join(scratch, "out", "publication-plan.json")), false);
});

test("site-and-title twins carrying two different labels stop the composer", (t) => {
  const { result } = compose(t, {
    main: verdict([finding("a", { title: "Lost update" })]),
    mainDecision: decision("request-changes", [{ key: "a", gating: true, defect: "one" }]),
    brief: verdict([finding("b", { title: "Lost update", source: "Money safety" })]),
    briefDecision: decision("request-changes", [{ key: "b", gating: true, defect: "two" }]),
  });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /two defect labels across the decisions: one and two/);
});

test("a distinct defect at an absorbed member's site cross-references the merged defect", (t) => {
  const { plan, readJson } = compose(t, {
    main: verdict([
      finding("d-low", { severity: "Low", title: "Remainder sign on MinInt64", line: 42 }),
      finding("d-high", { title: "MinInt64 overflows Split", line: 80 }),
      finding("e", { title: "Unchecked error", line: 42 }),
    ]),
    mainDecision: decision("request-changes", [
      { key: "d-low", gating: true, defect: "minint-overflow", undergrade: "the overflow reaches money arithmetic" },
      { key: "d-high", gating: true, defect: "minint-overflow" },
      { key: "e", gating: true },
    ]),
  });
  const comments = readJson(plan.posts[0].comments);
  assert.equal(comments.length, 2);
  const merged = comments.find((comment) => comment.line === 80);
  const distinct = comments.find((comment) => comment.line === 42);
  assert.match(merged.body, /Also at: `internal\/review\.go:42`\./);
  assert.doesNotMatch(merged.body, /See also on this line/);
  assert.match(distinct.body, /See also on this line: "MinInt64 overflows Split"/);
});

test("a clean run files a labelled defect as one entry naming its other sites", (t) => {
  const { plan, readJson } = compose(t, {
    main: verdict([finding("a", { severity: "Low", title: "MinInt64 overflows Split", line: 83 })]),
    mainDecision: decision("clean", [{ key: "a", gating: false, defect: "minint-overflow" }]),
    brief: verdict([finding("b", { severity: "Low", title: "Split rejects the minimum value", line: 75, source: "Conservation" })]),
    briefDecision: decision("clean", [{ key: "b", gating: false, defect: "minint-overflow" }]),
  });
  assert.deepEqual(plan.posts, []);
  const entries = readJson(plan.triage.entries);
  assert.equal(entries.length, 1);
  assert.match(issueLogEntry(entries[0], "Filed"), /\(`internal\/review\.go:83`, also at `internal\/review\.go:75`; also raised by Conservation\)/);
});

test("a finding classed as already raised reaches no review and no filing", (t) => {
  const confirmed = [finding("a"), finding("b", { line: 7 }), finding("c", { severity: "Low", line: 9 })];
  const blocking = compose(t, {
    main: verdict(confirmed),
    mainDecision: decision("request-changes", [
      { key: "a", gating: true },
      { key: "b", gating: false, class: "already-raised" },
      { key: "c", gating: false, class: "already-raised" },
    ]),
  });
  assert.equal(blocking.result.status, 0, blocking.result.stderr);
  assert.equal(blocking.plan.alreadyRaised, 2);
  const comments = blocking.readJson(blocking.plan.posts[0].comments);
  assert.deepEqual(comments.map((comment) => comment.line), [42]);
  assert.doesNotMatch(blocking.read(blocking.plan.posts[0].body), /advisory/);

  const clean = compose(t, {
    main: verdict(confirmed),
    mainDecision: decision("clean", [
      { key: "a", gating: false, class: "already-raised" },
      { key: "b", gating: false, class: "declared-out-of-scope" },
      { key: "c", gating: false, class: "already-raised" },
    ]),
  });
  assert.equal(clean.result.status, 0, clean.result.stderr);
  assert.deepEqual(clean.plan.posts, []);
  assert.equal(clean.plan.alreadyRaised, 2);
  assert.equal(clean.plan.declaredOutOfScope, 1);
  assert.deepEqual(clean.readJson(clean.plan.triage.entries), []);
});

test("a finding classed as a declared out-of-scope gap reaches no review and no filing", (t) => {
  const confirmed = [finding("a"), finding("b", { line: 7 }), finding("c", { severity: "Low", line: 9 }), finding("d", { severity: "Low", line: 11 })];
  const { result, plan, read, readJson } = compose(t, {
    main: verdict(confirmed),
    mainDecision: decision("request-changes", [
      { key: "a", gating: true },
      { key: "b", gating: false, class: "declared-out-of-scope" },
      { key: "c", gating: false, class: "declared-out-of-scope" },
      { key: "d", gating: false },
    ]),
  });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(plan.declaredOutOfScope, 2);
  assert.deepEqual(readJson(plan.posts[0].comments).map((comment) => comment.line), [42]);
  assert.doesNotMatch(read(plan.posts[0].body), /Finding [bc]/);
  assert.deepEqual(readJson(plan.triage.entries).map((entry) => entry.id), ["d"]);
  assert.equal(plan.triage.advisory, 1);
});

// One defect the author was told of at one site and not at another: the
// earlier site is not said again and the new one carries the defect's call.
for (const order of ["earlier site first", "new site first"])
  for (const placement of ["one decision", "across the decisions"])
    test(`a labelled defect at an already-raised site and a new site posts only the new site (${placement}, ${order})`, (t) => {
      const earlier = { found: finding("old", { title: "MinInt64 overflows Split", line: 83 }), disposition: { key: "old", gating: false, class: "already-raised", defect: "minint-overflow" } };
      const fresh = { found: finding("new", { title: "Split rejects the minimum value", line: 75, source: "Conservation" }), disposition: { key: "new", gating: true, defect: "minint-overflow" } };
      const members = order === "earlier site first" ? [earlier, fresh] : [fresh, earlier];
      const groups = placement === "one decision"
        ? {
          main: verdict(members.map((member) => member.found)),
          mainDecision: decision("request-changes", members.map((member) => member.disposition)),
        }
        : {
          main: verdict([earlier.found]),
          mainDecision: decision("clean", [earlier.disposition]),
          brief: verdict([fresh.found]),
          briefDecision: decision("request-changes", [fresh.disposition]),
        };
      const { result, plan, readJson } = compose(t, groups);
      assert.equal(result.status, 0, result.stderr);
      assert.deepEqual(posts(plan), [{ review: "main", verdict: "request-changes" }]);
      const comments = readJson(plan.posts[0].comments);
      assert.deepEqual(comments.map((comment) => comment.line), [75]);
      assert.doesNotMatch(comments[0].body, /Also at/);
      assert.equal(plan.alreadyRaised, 1);
      assert.deepEqual(readJson(plan.triage.entries), []);
    });

test("a labelled defect at an already-raised site files its new sites as one advisory entry", (t) => {
  const { result, plan, readJson } = compose(t, {
    main: verdict([
      finding("old", { severity: "Low", title: "MinInt64 overflows Split", line: 83 }),
      finding("new-a", { severity: "Low", title: "Split rejects the minimum value", line: 75 }),
      finding("new-b", { severity: "Low", title: "Split rejects the minimum value", line: 91 }),
    ]),
    mainDecision: decision("clean", [
      { key: "old", gating: false, class: "already-raised", defect: "minint-overflow" },
      { key: "new-a", gating: false, defect: "minint-overflow" },
      { key: "new-b", gating: false, defect: "minint-overflow" },
    ]),
  });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(plan.posts, []);
  assert.equal(plan.alreadyRaised, 1);
  const entries = readJson(plan.triage.entries);
  assert.equal(entries.length, 1);
  assert.match(issueLogEntry(entries[0], "Filed"), /\(`internal\/review\.go:75`, also at `internal\/review\.go:91`\)/);
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

// Read the producer's actual payload file, not an expected value rendered
// through the same presentation function as the code under test.
test("the publication composer writes anchor-normalised finding prose to its comments payload", (t) => {
  const { result, plan, readJson } = compose(t, {
    main: verdict([finding("citation", {
      title: "Invalid state at internal/review.go:99",
      explanation: "Compare internal/review.go:99 and cmd/minos/main.go:12 with internal/review.go:42.",
      proposingModel: undefined, verifyingModel: undefined,
    })]),
    mainDecision: decision("request-changes", [{ key: "citation", gating: true }]),
  });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(readJson(plan.posts[0].comments), [{
    path: "internal/review.go", line: 42,
    body: "**Blocking · High: Invalid state at internal/review.go**\n\n" +
      "Compare internal/review.go and cmd/minos/main.go with internal/review.go:42.",
  }], "the producer's comments file leaked model-written lines unrelated to its mechanical anchor");
});
