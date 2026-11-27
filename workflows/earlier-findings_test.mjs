import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import test from "node:test";

import { earlierMinosFindings, foldedEntries } from "./earlier-minos-findings.mjs";
import { judgementArgs } from "./judgement-stage.mjs";
import { AUTHOR_REJECTED, DEFECT_GONE, STANDS, stage } from "./judgements/earlier-findings.mjs";
import { authorCommentsAfter, findingsToRead, settleEarlierFindings } from "./run-steps/settle-earlier-findings.mjs";

const head = "2222222222222222222222222222222222222222";
const earlierHead = "3333333333333333333333333333333333333333";
const env = { MINOS_PROVISIONED_ENGINES: "claude" };

const foldedBody =
  "**Minos: changes need attention.**\n\n---\n\nFindings that could not be anchored inline:\n\n" +
  "These have no thread to resolve. The pull request's author can reject one with a comment on the pull request that names it by its title or by its path and line.\n" +
  "\n#### `docs/a.md` line 3\n\n**Blocking · High: Stale claim**\n\nThe page says otherwise.\n" +
  "\n#### `src/b.go` lines 10-14\n\n**Advisory · Low: Long function**\n" +
  "\nReviewed by: `claude-opus-5-5`.\n\n" +
  `<!-- Minos: head=${earlierHead} target=${head} -->\n`;

test("a review body's folded entries are read with the path, line and text each heading gives", () => {
  assert.deepEqual(foldedEntries(foldedBody), [
    { path: "docs/a.md", line: 3, finding: "**Blocking · High: Stale claim**\n\nThe page says otherwise." },
    { path: "src/b.go", line: 10, finding: "**Advisory · Low: Long function**" },
  ]);
  assert.deepEqual(foldedEntries("**Minos: clean.**"), []);
});

function snapshotWith(reviews) {
  return { head_sha: head, authenticated_user: "minos", author: "alice", reviews };
}

const minosReview = { id: 7, user: "minos", commit_id: earlierHead, submitted_at: "2026-11-20T10:00:00Z", body: foldedBody };
const threads = [
  { path: "src/parse.go", line: 9, resolved: false, comments: [
    { user: "minos", body: "**Blocking · High: Trailing input**", commit_id: earlierHead },
    { user: "bob", body: "This is intended." },
    { user: "alice", body: "Intended: the format allows it." },
  ] },
  { path: "src/parse.go", line: 30, resolved: true, comments: [{ user: "minos", body: "**Blocking · High: Resolved one**", commit_id: earlierHead }] },
  { path: "src/parse.go", line: 40, resolved: false, comments: [{ user: "minos", body: "**Advisory · Low: Naming**", commit_id: earlierHead }] },
];
// A blocking finding an earlier run raised on the head this run reviews.
const sameHeadThread = { path: "src/parse.go", line: 50, resolved: false, comments: [{ user: "minos", body: "**Blocking · High: Raised on this head**", commit_id: head }] };

test("only another reviewer's reviews are skipped, and every Minos finding is listed with a unique key", () => {
  const found = earlierMinosFindings(snapshotWith([minosReview, { ...minosReview, id: 8, user: "carol" }]), threads);

  assert.equal(found.length, 5);
  assert.equal(new Set(found.map((finding) => finding.key)).size, 5);
  assert.deepEqual(found.filter((finding) => finding.blocking).map((finding) => finding.line), [9, 30, 3]);
  assert.throws(() => earlierMinosFindings({ reviews: [] }, []), /no authenticated user/);
});

test("the author's comments answer a review-body finding only when written after that review, compared as instants", () => {
  const comments = [
    { user: "alice", body: "before", created_at: "2026-11-20T09:59:59Z" },
    { user: "alice", body: "after, in another offset", created_at: "2026-11-20T11:00:01+01:00" },
    { user: "bob", body: "not the author", created_at: "2026-11-21T00:00:00Z" },
    { user: "alice", body: "later", created_at: "2026-11-21T00:00:00Z" },
  ];
  assert.deepEqual(authorCommentsAfter(comments, "alice", "2026-11-20T10:00:00Z"), ["after, in another offset", "later"]);
  assert.throws(() => authorCommentsAfter(comments, "alice", ""), /not a time/);
});

test("what is settled mechanically is set aside: resolved threads and findings that do not block", () => {
  const comments = [{ user: "alice", body: "The Stale claim finding is wrong.", created_at: "2026-11-21T00:00:00Z" }];
  const findings = findingsToRead({ snapshot: snapshotWith([minosReview]), threads, comments });

  assert.deepEqual(findings, [
    {
      key: "thread-1", path: "src/parse.go", line: 9, head: earlierHead, finding: "**Blocking · High: Trailing input**", where: "thread",
      replies: [{ byAuthor: false, body: "This is intended." }, { byAuthor: true, body: "Intended: the format allows it." }],
    },
    {
      key: "review-7-1", path: "docs/a.md", line: 3, head: earlierHead, finding: "**Blocking · High: Stale claim**\n\nThe page says otherwise.", where: "review-body",
      comments: ["The Stale claim finding is wrong."],
    },
  ]);
  assert.equal(stage.inputProblem({ head, findings }), null);
});

test("a blocking finding raised on this head by an earlier run is read, not taken as this run's own", () => {
  const findings = findingsToRead({ snapshot: snapshotWith([]), threads: [sameHeadThread], comments: [] });

  assert.deepEqual(findings.map((finding) => [finding.key, finding.head]), [["thread-1", head]]);
});

test("the prompt carries the head and the findings, and the answer can name only findings shown", () => {
  const findings = findingsToRead({ snapshot: snapshotWith([minosReview]), threads, comments: [] });
  const { prompt, schema } = judgementArgs(stage, { head, findings }, { env });

  assert.ok(prompt.includes(`checked out at this head, \`${head}\``));
  assert.ok(prompt.includes(JSON.stringify(findings, null, 2)));
  assert.doesNotMatch(prompt, /\{\{[a-z-]+\}\}/);
  assert.deepEqual(schema.properties.findings.items.properties.key.enum, ["thread-1", "review-7-1"]);
});

test("an answer must settle every finding once, each with a reason", () => {
  const input = { head, findings: findingsToRead({ snapshot: snapshotWith([minosReview]), threads, comments: [] }) };
  const entry = (key, outcome = STANDS, reason = "The parser still stops early at src/parse.go:9.") => ({ key, outcome, reason });

  assert.equal(stage.answerProblem({ findings: [entry("thread-1"), entry("review-7-1")] }, input), null);
  assert.match(stage.answerProblem({ findings: [entry("thread-1")] }, input), /no answer for review-7-1/);
  assert.match(stage.answerProblem({ findings: [entry("thread-1"), entry("thread-1"), entry("review-7-1")] }, input), /answered twice/);
  assert.match(stage.answerProblem({ findings: [entry("thread-1", STANDS, " "), entry("review-7-1")] }, input), /reason for thread-1 is blank/);
  assert.match(stage.inputProblem({ head, findings: [] }), /non-empty/);
  assert.match(stage.inputProblem({ head, findings: [{ ...input.findings[0], where: "email" }] }), /thread or a review body/);
});

test("a same-head finding can stand or be rejected by the author, but cannot have changed", () => {
  const input = { head, findings: findingsToRead({ snapshot: snapshotWith([]), threads: [sameHeadThread], comments: [] }) };
  const answer = (outcome) => ({ findings: [{ key: "thread-1", outcome, reason: "There has been no change since the review." }] });
  assert.equal(stage.answerProblem(answer(STANDS), input), null);
  assert.equal(stage.answerProblem(answer(AUTHOR_REJECTED), input), null);
  assert.match(stage.answerProblem(answer(DEFECT_GONE), input), /was raised on this head/);
});

function fixtureRun({ reviews = [minosReview], forgeThreads = threads, answer } = {}) {
  const asked = [];
  const read = [];
  const comments = [{ user: "alice", body: "The Stale claim finding is wrong.", created_at: "2026-11-21T00:00:00Z" }];
  return {
    asked,
    read,
    run: {
      head,
      forge: async (command) => {
        read.push(command);
        return { snapshot: snapshotWith(reviews), threads: forgeThreads, comments }[command];
      },
      judge: async (judged, given) => {
        asked.push({ judged, given });
        return answer;
      },
    },
  };
}

test("the step keeps the findings the stage says stand, with the stage's reason", async () => {
  const { run, asked, read } = fixtureRun({
    answer: { findings: [
      { key: "thread-1", outcome: AUTHOR_REJECTED, reason: "The author replied that the format allows it." },
      { key: "review-7-1", outcome: STANDS, reason: "docs/a.md:3 still makes the claim." },
    ] },
  });

  const settled = await settleEarlierFindings(run, {});

  assert.equal(asked.length, 1);
  assert.equal(asked[0].judged, stage);
  assert.deepEqual(read, ["snapshot", "threads", "comments"]);
  assert.deepEqual(settled, {
    outstanding: [{ key: "review-7-1", where: "review-body", path: "docs/a.md", line: 3, head: earlierHead, reason: "docs/a.md:3 still makes the claim." }],
  });
});

test("with nothing left to read the step asks no stage and reads no comments", async () => {
  const { run, asked, read } = fixtureRun({ reviews: [], forgeThreads: [threads[1], threads[2]] });

  assert.deepEqual(await settleEarlierFindings(run, {}), { outstanding: [] });
  assert.equal(asked.length, 0);
  assert.deepEqual(read, ["snapshot", "threads"]);
});

test("a review-body finding raised on this head still has the author's comments read", async () => {
  const { run, asked, read } = fixtureRun({
    reviews: [{ ...minosReview, commit_id: head,
      body: foldedBody.replace(/\n#### `src\/b\.go`[\s\S]*?(?=\nReviewed by:)/, "") }],
    forgeThreads: [],
    answer: { findings: [{ key: "review-7-1", outcome: AUTHOR_REJECTED, reason: "The author named the Stale claim finding and rejected it." }] },
  });

  assert.deepEqual(await settleEarlierFindings(run, {}), { outstanding: [] });
  assert.deepEqual(read, ["snapshot", "threads", "comments"]);
  assert.deepEqual(asked[0].given.findings[0].comments, ["The Stale claim finding is wrong."]);
  assert.equal(asked[0].given.findings[0].finding, "**Blocking · High: Stale claim**\n\nThe page says otherwise.");
});

test("a defect read as gone lifts its block", async () => {
  const { run } = fixtureRun({
    reviews: [],
    forgeThreads: [threads[0]],
    answer: { findings: [{ key: "thread-1", outcome: DEFECT_GONE, reason: "src/parse.go:9 now rejects trailing input." }] },
  });

  assert.deepEqual(await settleEarlierFindings(run, {}), { outstanding: [] });
});
