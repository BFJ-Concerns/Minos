import test from "node:test";
import assert from "node:assert/strict";

import {
  findingComment,
  issueLogEntry,
  reviewBody,
} from "./finding-presentation.mjs";

// The presentation module is the single authority for review and triage
// text. These tests pin the author-facing contract: a payload carries the
// finding — whether it blocks, its severity, title, explanation, location —
// and none of the run's internals.

const confirmed = {
  id: "specialist-2-gpt:1",
  source: "Does the change handle concurrent writers safely?",
  title: "Lost update on concurrent write",
  explanation: "Two writers read the same revision and the later write wins silently.",
  severity: "High",
  confidence: 62,
  verifierConfidence: 55,
  combinedConfidence: 58,
  path: "internal/store.go",
  line: 41,
  verifyLabel: "verify-1-claude",
  proposingModel: { pinnedModel: "gpt-5.6-sol", resolvedModel: "gpt-5.6-sol-served" },
  verifyingModel: { pinnedModel: "claude-opus-5", resolvedModel: "claude-opus-5" },
};

const LEAKS = ["Does the change handle", "62", "55", "onfidence", "specialist-2-gpt", "verify-1-claude"];

test("a blocking finding comment leads with its disposition and severity, then one quiet provenance line", () => {
  const comment = findingComment(confirmed, { key: confirmed.id, gating: true });
  assert.equal(comment.path, "internal/store.go");
  assert.equal(comment.line, 41);
  assert.equal(
    comment.body,
    "**Blocking · High: Lost update on concurrent write**\n\n" +
      "Two writers read the same revision and the later write wins silently.\n\n" +
      "Proposed by `gpt-5.6-sol-served` (pinned `gpt-5.6-sol`); verified by `claude-opus-5`.",
  );
  // The exact-body assertion above is the contract; these name the leaks
  // it exists to prevent, so a loosened rendering fails with the leak's
  // own name.
  for (const leaked of LEAKS) assert.ok(!comment.body.includes(leaked), `payload leaked ${leaked}`);
});

test("a finding with no gating disposition renders as advisory, and no model evidence means no provenance line", () => {
  const comment = findingComment({
    title: "Lost update on concurrent write",
    explanation: "Two writers read the same revision and the later write wins silently.",
    severity: "Low",
    path: "internal/store.go",
    line: 41,
  });
  assert.equal(
    comment.body,
    "**Advisory · Low: Lost update on concurrent write**\n\n" +
      "Two writers read the same revision and the later write wins silently.",
  );
  const declassified = findingComment(confirmed, { key: confirmed.id, gating: false, class: "speculative-hardening" });
  assert.match(declassified.body, /^\*\*Advisory · High: /);
});

test("merged and neighbouring findings are named on the comment, never posted twice", () => {
  const comment = findingComment(
    {
      ...confirmed,
      proposingModel: undefined,
      verifyingModel: undefined,
      alsoRaisedBy: ["Money safety"],
      crossReferences: ["Rounding drops the remainder"],
    },
    { key: confirmed.id, gating: true },
  );
  assert.equal(
    comment.body,
    "**Blocking · High: Lost update on concurrent write**\n\n" +
      "Two writers read the same revision and the later write wins silently.\n\n" +
      "Also raised by: Money safety.\n\n" +
      'See also on this line: "Rounding drops the remainder".',
  );
});

test("a finding about a range carries its end line, a single-line finding does not", () => {
  const range = findingComment({
    title: "Duplicated decision",
    explanation: "The three branches repeat one decision.",
    severity: "Medium",
    path: "internal/store.go",
    line: 41,
    endLine: 44,
  });
  assert.equal(range.line, 41);
  assert.equal(range.end_line, 44);

  const single = findingComment({
    title: "Lost update",
    explanation: "The later write wins silently.",
    severity: "High",
    path: "internal/store.go",
    line: 41,
  });
  assert.ok(!("end_line" in single), "a single-line finding invented a range");
});

const reviewed = { head: "2c518981234abcd", target: "0981206c9876fed", occasion: null };

test("a blocking review body carries its warrant: verdict, what blocks, the advisory count, and the re-review contract", () => {
  const findings = [
    { id: "a", title: "Lost update", severity: "High", path: "internal/store.go", line: 41 },
    { id: "c", title: "Remainder dropped", severity: "Critical", path: "internal/review.go", line: 60 },
  ];
  assert.equal(
    reviewBody({ reviewed, findings, advisory: 2 }),
    "**Minos: changes need attention.**\n" +
      "\n" +
      "Reviewed head `2c51898` against target `0981206`.\n" +
      "2 blocking findings (1 Critical, 1 High) in `internal/review.go`, `internal/store.go`, each anchored inline where the diff allows; they must be resolved before this review approves.\n" +
      "2 advisory findings included for the same review round.\n" +
      "\n" +
      "This review stands for head `2c51898` only: push a new commit and Minos reviews the new head afresh.",
  );
});

test("the review body names the briefs applied", () => {
  const findings = [{ id: "x", title: "Float in money path", severity: "High", path: "pkg/money.go", line: 3 }];
  const body = reviewBody({
    reviewed,
    findings,
    briefsRan: [".review/money-safety.md"],
  });
  assert.match(body, /^\*\*Minos: changes need attention\.\*\*\n/);
  assert.match(body, /\nBriefs applied: `\.review\/money-safety\.md`\.\n/);
  assert.match(body, /1 blocking finding \(1 High\) in `pkg\/money\.go`/);
  assert.doesNotMatch(body, /approved/);
});

const attribution = "Filed by Minos from owner/repository#17, 2026-09-12";

test("an issue-log entry is one list item carrying the finding, its site, provenance and source", () => {
  const entry = issueLogEntry({ ...confirmed, kind: "advisory-finding" }, attribution);
  assert.equal(
    entry,
    "- **Advisory · High: Lost update on concurrent write** (`internal/store.go:41`) — " +
      "Two writers read the same revision and the later write wins silently. " +
      "Proposed by `gpt-5.6-sol-served` (pinned `gpt-5.6-sol`); verified by `claude-opus-5`. " +
      "Filed by Minos from owner/repository#17, 2026-09-12.",
  );
  for (const leaked of LEAKS) assert.ok(!entry.includes(leaked), `entry leaked ${leaked}`);
  assert.throws(() => issueLogEntry({ kind: "out-of-scope-observation" }, attribution), /unknown entry kind/);
  assert.equal(
    issueLogEntry({
      kind: "review-brief-misconfiguration",
      title: "Security brief",
      brief: ".review/security.md",
      reason: "declared scope directory does not exist",
    }, attribution),
    "- **Review brief misconfiguration: Security brief** (`.review/security.md`) — declared scope directory does not exist. " +
      "Filed by Minos from owner/repository#17, 2026-09-12.",
  );
  // A multi-line explanation stays inside its list item.
  assert.match(
    issueLogEntry({ kind: "advisory-finding", severity: "Low", title: "T", path: "p", line: 1, explanation: "first\nsecond" }, attribution),
    /first\n  second/,
  );
  assert.throws(() => issueLogEntry({ kind: "review-finding", title: "x" }, attribution), /unknown entry kind review-finding/);
});
