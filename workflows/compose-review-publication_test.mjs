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

test("a gating main verdict composes one request-changes review carrying only its blocking finding; the advisory one is triage", (t) => {
  const { result, plan, read, readJson, scratch } = compose(t, {
    main: verdict([finding("a"), finding("b", { severity: "Low", line: 7 })]),
    mainDecision: decision("request-changes", [{ key: "a", gating: true }, { key: "b", gating: false }]),
  });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(plan.kind, "minos-publication-plan-v1");
  assert.equal(plan.verdict, "request-changes");
  assert.deepEqual(posts(plan), [{ review: "main", verdict: "request-changes" }]);
  const body = read(plan.posts[0].body);
  assert.match(body, /^\*\*Minos: changes need attention\.\*\*\n/);
  assert.match(body, /1 blocking finding \(1 High\) in `internal\/review\.go`[^\n]*it must be resolved before this review approves\./);
  assert.match(body, /1 advisory finding is filed for triage, not on this review\./);
  const comments = readJson(plan.posts[0].comments);
  assert.deepEqual(comments.map(({ path, line }) => ({ path, line })), [{ path: "internal/review.go", line: 42 }]);
  assert.match(comments[0].body, /^\*\*Blocking · High: Finding a\*\*/);
  assert.doesNotMatch(JSON.stringify(comments), /confidence|proposingLabel|verifyLabel|rawVerifier/);
  assert.deepEqual({ ...plan.triage, entries: undefined }, { advisory: 1, observations: 0, misconfigurations: 0, entries: undefined });
  const triage = readJson(plan.triage.entries);
  assert.equal(triage.length, 1);
  assert.equal(triage[0].kind, "advisory-finding");
  assert.equal(triage[0].id, "b");
  assert.deepEqual(readJson(join(scratch, "out", "publication-plan.json")), plan);
});

test("a clean main verdict composes an approving review; below-threshold findings leave it for triage", (t) => {
  const { result, plan, read, readJson } = compose(t, {
    main: verdict([finding("b", { severity: "Medium", line: 7 })]),
    mainDecision: decision("clean", [{ key: "b", gating: false }]),
  });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(plan.verdict, "clean");
  assert.deepEqual(plan.posts.map((post) => post.verdict), ["approve"]);
  const body = read(plan.posts[0].body);
  assert.match(body, /^\*\*Minos: changes approved\.\*\*\n[\s\S]*\nNo blocking findings\.\n1 advisory finding is filed for triage, not on this review\.\n/);
  assert.deepEqual(readJson(plan.posts[0].comments), []);
  assert.equal(readJson(plan.triage.entries)[0].kind, "advisory-finding");
});

test("a clean verdict with nothing to triage composes an approving review and an empty triage file", (t) => {
  const { result, plan, read, readJson } = compose(t, {
    main: verdict([]),
    mainDecision: decision("clean", []),
  });
  assert.equal(result.status, 0, result.stderr);
  assert.match(read(plan.posts[0].body), /\nNo blocking findings\.\n\nThis review stands for head/);
  assert.deepEqual(readJson(plan.triage.entries), []);
  assert.deepEqual({ ...plan.triage, entries: undefined }, { advisory: 0, observations: 0, misconfigurations: 0, entries: undefined });
});

test("a brief group with no gating finding of its own posts nothing; its findings are triage", (t) => {
  const { result, plan, readJson } = compose(t, {
    main: verdict([finding("a")]),
    mainDecision: decision("request-changes", [{ key: "a", gating: true }]),
    brief: verdict([finding("money:1", { source: "Money safety", severity: "Medium", line: 90, title: "Float in money path" })], {
      ran: [{ brief: ".review/money-safety.md", title: "Money safety" }],
    }),
    briefDecision: decision("clean", [{ key: "money:1", gating: false }]),
  });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(posts(plan), [{ review: "main", verdict: "request-changes" }]);
  const triage = readJson(plan.triage.entries);
  assert.deepEqual(triage.map((entry) => [entry.kind, entry.id]), [["advisory-finding", "money:1"]]);
});

test("a brief group whose own decision gates posts request-changes after the main review, naming its briefs", (t) => {
  const { result, plan, read } = compose(t, {
    main: verdict([]),
    mainDecision: decision("clean", []),
    brief: verdict([finding("money:1", { source: "Money safety", line: 90, title: "Float in money path" })], {
      ran: [{ brief: ".review/money-safety.md", title: "Money safety" }],
    }),
    briefDecision: decision("request-changes", [{ key: "money:1", gating: true }]),
  });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(plan.verdict, "request-changes", "a gating brief group makes the run's verdict request-changes");
  assert.deepEqual(posts(plan), [
    { review: "main", verdict: "approve" },
    { review: "brief", verdict: "request-changes" },
  ]);
  const briefBody = read(plan.posts[1].body);
  assert.match(briefBody, /^\*\*Minos repository-brief review: changes need attention\.\*\*\n/);
  assert.match(briefBody, /Briefs applied: `\.review\/money-safety\.md`\./);
  assert.doesNotMatch(briefBody, /approved/);
});

test("one defect raised by both groups at one site is one finding, gating if either decision gates", (t) => {
  const { result, plan, readJson, read } = compose(t, {
    main: verdict([finding("a", { title: "Remainder dropped on split", severity: "Medium", line: 8 })]),
    mainDecision: decision("clean", [{ key: "a", gating: false }]),
    brief: verdict([
      finding("money:1", { source: "Money safety", title: "Remainder dropped on split", severity: "High", line: 8 }),
      finding("money:2", { source: "Money safety", title: "Rounding mode unstated", severity: "Low", line: 8 }),
    ]),
    briefDecision: decision("request-changes", [{ key: "money:1", gating: true }, { key: "money:2", gating: false }]),
  });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(posts(plan), [{ review: "main", verdict: "request-changes" }]);
  const mainComments = readJson(plan.posts[0].comments);
  assert.equal(mainComments.length, 1);
  assert.match(mainComments[0].body, /^\*\*Blocking · High: Remainder dropped on split\*\*/);
  assert.match(mainComments[0].body, /Also raised by: Money safety\./);
  assert.match(read(plan.posts[0].body), /1 blocking finding \(1 High\)/);
  const triage = readJson(plan.triage.entries);
  assert.deepEqual(triage.map((entry) => entry.id), ["money:2"], "the merged twin is not filed twice");
});

test("distinct blocking findings on one line cross-reference each other by title", (t) => {
  const { result, plan, readJson } = compose(t, {
    main: verdict([
      finding("a", { title: "Lost update", line: 8 }),
      finding("b", { title: "Unchecked error", line: 8 }),
    ]),
    mainDecision: decision("request-changes", [{ key: "a", gating: true }, { key: "b", gating: true }]),
  });
  assert.equal(result.status, 0, result.stderr);
  const comments = readJson(plan.posts[0].comments);
  assert.match(comments[0].body, /See also on this line: "Unchecked error"\./);
  assert.match(comments[1].body, /See also on this line: "Lost update"\./);
});

test("unverified observations and misconfigurations from both groups are triage entries in channel order, never posts", (t) => {
  const { result, plan, read, readJson } = compose(t, {
    main: verdict([finding("b", { severity: "Low", line: 7 })], {
      outOfScopeObservations: [{
        id: "specialist-1:observation:1",
        source: "Correctness",
        title: "Unrelated nil deref",
        path: "pkg/server.go",
        line: 12,
        explanation: "A nil map write outside the reviewed range.",
        observingLabel: "specialist-1",
        verified: false,
      }],
    }),
    mainDecision: decision("clean", [{ key: "b", gating: false }]),
    brief: verdict([], {
      misconfigurations: [{ brief: ".review/security.md", title: "Security brief", kind: "missing-scope", reason: "declared scope directory does not exist" }],
    }),
    briefDecision: decision("clean", []),
  });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(posts(plan), [{ review: "main", verdict: "approve" }]);
  assert.match(
    read(plan.posts[0].body),
    /1 advisory finding, 1 unverified observation and 1 review-brief misconfiguration are filed for triage, not on this review\./,
  );
  const triage = readJson(plan.triage.entries);
  assert.deepEqual(triage.map((entry) => entry.kind), ["advisory-finding", "out-of-scope-observation", "review-brief-misconfiguration"]);
  assert.equal(triage[2].kind, "review-brief-misconfiguration", "the channel kind wins over the misconfiguration's own kind");
  assert.deepEqual({ ...plan.triage, entries: undefined }, { advisory: 1, observations: 1, misconfigurations: 1, entries: undefined });
});

test("an id-less verdict is gated by the digest's content key, never silently rendered advisory", (t) => {
  const { id: omittedId, ...idless } = finding("a");
  const contentKey = JSON.stringify(["internal/review.go", 42, "finding a"]);
  const { result, plan, readJson } = compose(t, {
    main: verdict([idless]),
    mainDecision: decision("request-changes", [{ key: contentKey, gating: true }]),
  });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(plan.verdict, "request-changes");
  assert.deepEqual(plan.posts.map((post) => post.verdict), ["request-changes"]);
  assert.match(readJson(plan.posts[0].comments)[0].body, /^\*\*Blocking · High: Finding a\*\*/);
});

test("a decision that does not validate against its verdict composes nothing and exits 1", (t) => {
  const { result, scratch } = compose(t, {
    main: verdict([finding("a")]),
    mainDecision: decision("clean", [{ key: "a", gating: false }]),
  });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /main decision does not validate: an at-or-above-threshold finding may be judged non-gating only as/);
  assert.equal(existsSync(join(scratch, "out")), false, "nothing is written from an unvalidated decision");
});

test("argument errors exit 2 with usage", () => {
  const result = spawnSync(process.execPath, [cliPath, "only-one"], { encoding: "utf8" });
  assert.equal(result.status, 2);
  assert.match(result.stderr, /^usage: node workflows\/compose-review-publication\.mjs OUTPUT_DIR THRESHOLD/);
});
