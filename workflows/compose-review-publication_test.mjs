import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import test from "node:test";
import { spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

// The composer is the one step between validated verdicts and the forge.
// These tests drive the real CLI over fixture verdicts and decisions and
// read back the files it writes: the plan's order and verdict arguments
// are the publication contract the lifecycle posts without judgement.

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

test("a gating main verdict composes one request-changes review first, findings carrying their dispositions", (t) => {
  const { result, plan, read, readJson, scratch } = compose(t, {
    main: verdict([finding("a"), finding("b", { severity: "Low", line: 7 })]),
    mainDecision: decision("request-changes", [{ key: "a", gating: true }, { key: "b", gating: false }]),
  });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(plan.kind, "minos-publication-plan-v1");
  assert.equal(plan.verdict, "request-changes");
  assert.deepEqual(plan.posts.map(({ review, verdict: argument }) => ({ review, verdict: argument })), [
    { review: "main", verdict: "request-changes" },
  ]);
  assert.match(read(plan.posts[0].body), /^\*\*Minos: changes need attention\.\*\*\n/);
  assert.match(read(plan.posts[0].body), /1 blocking finding must be resolved/);
  const comments = readJson(plan.posts[0].comments);
  assert.deepEqual(comments.map(({ path, line }) => ({ path, line })), [
    { path: "internal/review.go", line: 42 },
    { path: "internal/review.go", line: 7 },
  ]);
  assert.match(comments[0].body, /^\*\*Blocking · High: Finding a\*\*/);
  assert.match(comments[1].body, /^\*\*Advisory · Low: Finding b\*\*/);
  assert.doesNotMatch(JSON.stringify(comments), /confidence|proposingLabel|verifyLabel|rawVerifier/);
  assert.deepEqual(readJson(join(scratch, "out", "publication-plan.json")), plan);
});

test("a clean main verdict composes an approving review with the clean warrant", (t) => {
  const { result, plan, read, readJson } = compose(t, {
    main: verdict([]),
    mainDecision: decision("clean", []),
  });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(plan.verdict, "clean");
  assert.deepEqual(plan.posts.map((post) => post.verdict), ["approve"]);
  assert.match(read(plan.posts[0].body), /^\*\*Minos: changes approved\.\*\*\n[\s\S]*\nNo confirmed findings\.\n/);
  assert.deepEqual(readJson(plan.posts[0].comments), []);
});

test("a brief group with no gating finding of its own follows the main review as a comment review, never an approval", (t) => {
  const { result, plan, read } = compose(t, {
    main: verdict([finding("a")]),
    mainDecision: decision("request-changes", [{ key: "a", gating: true }]),
    brief: verdict([finding("money:1", { source: "Money safety", severity: "Medium", line: 90, title: "Float in money path" })], {
      ran: [{ brief: ".review/money-safety.md", title: "Money safety" }],
    }),
    briefDecision: decision("clean", [{ key: "money:1", gating: false }]),
  });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(plan.posts.map(({ review, verdict: argument }) => ({ review, verdict: argument })), [
    { review: "main", verdict: "request-changes" },
    { review: "brief", verdict: "comment" },
  ]);
  const briefBody = read(plan.posts[1].body);
  assert.match(briefBody, /^\*\*Minos repository-brief review: advisory findings\.\*\*\n/);
  assert.match(briefBody, /Briefs applied: `\.review\/money-safety\.md`\./);
  assert.doesNotMatch(briefBody, /approved/);
});

test("a brief group whose own decision gates posts request-changes after the main review", (t) => {
  const { result, plan } = compose(t, {
    main: verdict([]),
    mainDecision: decision("clean", []),
    brief: verdict([finding("money:1", { source: "Money safety", line: 90, title: "Float in money path" })]),
    briefDecision: decision("request-changes", [{ key: "money:1", gating: true }]),
  });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(plan.verdict, "request-changes", "a gating brief group makes the run's verdict request-changes");
  assert.deepEqual(plan.posts.map(({ review, verdict: argument }) => ({ review, verdict: argument })), [
    { review: "main", verdict: "approve" },
    { review: "brief", verdict: "request-changes" },
  ]);
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
  assert.deepEqual(plan.posts.map(({ review, verdict: argument }) => ({ review, verdict: argument })), [
    { review: "main", verdict: "request-changes" },
    { review: "brief", verdict: "comment" },
  ]);
  const mainComments = readJson(plan.posts[0].comments);
  assert.equal(mainComments.length, 1);
  assert.match(mainComments[0].body, /^\*\*Blocking · High: Remainder dropped on split\*\*/);
  assert.match(mainComments[0].body, /Also raised by: Money safety\./);
  const briefComments = readJson(plan.posts[1].comments);
  assert.deepEqual(briefComments.map((comment) => comment.line), [8]);
  assert.match(briefComments[0].body, /^\*\*Advisory · Low: Rounding mode unstated\*\*/);
  assert.match(read(plan.posts[0].body), /1 confirmed finding \(1 High\)/);
});

test("distinct findings on one line cross-reference each other by title", (t) => {
  const { result, plan, readJson } = compose(t, {
    main: verdict([
      finding("a", { title: "Lost update", line: 8 }),
      finding("b", { title: "Unchecked error", severity: "Medium", line: 8 }),
    ]),
    mainDecision: decision("request-changes", [{ key: "a", gating: true }, { key: "b", gating: false }]),
  });
  assert.equal(result.status, 0, result.stderr);
  const comments = readJson(plan.posts[0].comments);
  assert.match(comments[0].body, /See also on this line: "Unchecked error"\./);
  assert.match(comments[1].body, /See also on this line: "Lost update"\./);
});

test("unverified observations and misconfigurations from both groups compose one trailing comment review", (t) => {
  const { result, plan, read, readJson } = compose(t, {
    main: verdict([], {
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
    mainDecision: decision("clean", []),
    brief: verdict([], {
      misconfigurations: [{ brief: ".review/security.md", title: "Security brief", kind: "missing-scope", reason: "declared scope directory does not exist" }],
    }),
    briefDecision: decision("clean", []),
  });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(plan.posts.map(({ review, verdict: argument }) => ({ review, verdict: argument })), [
    { review: "main", verdict: "approve" },
    { review: "observations", verdict: "comment" },
  ]);
  assert.equal(read(plan.posts[1].body), "Unverified observations from the review, for the author's judgement.\n");
  const comments = readJson(plan.posts[1].comments);
  assert.deepEqual(comments.map(({ path, line }) => ({ path, line })), [
    { path: "pkg/server.go", line: 12 },
    { path: ".review/security.md", line: 1 },
  ]);
  assert.match(comments[0].body, /^\*\*Unverified observation: Unrelated nil deref\*\*/);
  assert.match(comments[1].body, /^\*\*Review brief misconfiguration: Security brief\*\*/);
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
