import "./isolate-from-live-run.mjs";
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
  proposingModel: { pinnedModel: "gpt-6-sol", resolvedModel: "gpt-6-sol-served" },
  verifyingModel: { pinnedModel: "claude-opus-5-5", resolvedModel: "claude-opus-5-5" },
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
      "Proposed by `gpt-6-sol-served` (pinned `gpt-6-sol`); verified by `claude-opus-5-5`.",
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

test("a comment renders duplicate-source attribution and neighbouring-finding references", () => {
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
      "Proposed by `gpt-6-sol-served` (pinned `gpt-6-sol`); verified by `claude-opus-5-5`. " +
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

// Every prose field must obey the same anchor rule; expected text is literal,
// independent of the renderer, including punctuation and Markdown boundaries.
for (const surface of ["inline", "filing"]) for (const ranged of [false, true]) {
  test(`${surface} prose citations cannot leak unrelated lines through inline comments or filing entries (${ranged ? "range" : "single-line"} anchor)`, () => {
    const finding = {
      ...confirmed,
      title: "Lost update in internal/store.go:99",
      explanation: "Same file `internal/store.go:99`; other file [consumer](cmd/minos/main.go:12); " +
        "own anchor internal/store.go:41; unrelated range internal/store.go:90-94; " +
        "partial range internal/store.go:41-42; exact range internal/store.go:41-44; " +
        "overlapping range internal/store.go:40-44; root file README.md:7; " +
        "extensionless scripts/run:8; URL https://host:3000/src/code.go:9; " +
        "port host:3000; time 10:30.",
      ...(ranged ? { endLine: 44 } : {}),
      proposingModel: undefined,
      verifyingModel: undefined,
      crossReferences: ["Neighbour at internal/store.go:99"],
      alsoRaisedBy: ["Criterion at docs/rules.md:12"],
    };
    const explanation = "Same file `internal/store.go`; other file [consumer](cmd/minos/main.go); " +
      "own anchor internal/store.go:41; unrelated range internal/store.go; " +
      "partial range internal/store.go; exact range " + (ranged ? "internal/store.go:41-44" : "internal/store.go") + "; " +
      "overlapping range internal/store.go; root file README.md; " +
      "extensionless scripts/run; URL https://host:3000/src/code.go:9; " +
      "port host:3000; time 10:30.";
    const comment = findingComment(finding);
    if (surface === "inline") assert.equal(comment.body,
      "**Advisory · High: Lost update in internal/store.go**\n\n" + explanation +
      "\n\nAlso raised by: Criterion at docs/rules.md." +
      '\n\nSee also on this line: "Neighbour at internal/store.go".',
      "inline prose leaked an unrelated citation, lost an exact anchor, or rewrote a URL, port or time");
    assert.equal(comment.path, "internal/store.go", "normalisation changed the mechanical path");
    assert.equal(comment.line, 41, "normalisation changed the mechanical line");
    assert.equal(comment.end_line, ranged ? 44 : undefined, "normalisation changed the mechanical range");
    if (surface === "filing") assert.equal(issueLogEntry({ ...finding, kind: "advisory-finding" }, attribution),
      "- **Advisory · High: Lost update in internal/store.go** (`internal/store.go:41`; also raised by Criterion at docs/rules.md) — " +
      explanation + " " + attribution + ".",
      "filing prose leaked an unrelated citation, lost an exact anchor, or rewrote a URL, port or time");
  });
}

for (const surface of ["inline", "filing"]) test(`${surface}: an extensionless anchored filename distinguishes its citations from a host port`, () => {
  const finding = {
    title: "Check Makefile:7", severity: "Low", path: "Makefile", line: 3,
    explanation: "Makefile:7 differs from Makefile:3; host:3000 and 10:30 stay intact.",
  };
  if (surface === "inline") assert.equal(findingComment(finding).body,
    "**Advisory · Low: Check Makefile**\n\nMakefile differs from Makefile:3; host:3000 and 10:30 stay intact.",
    "an extensionless file leaked an unrelated line or a non-path colon was rewritten");
  if (surface === "filing") assert.equal(issueLogEntry({ ...finding, kind: "advisory-finding" }, attribution),
    "- **Advisory · Low: Check Makefile** (`Makefile:3`) — Makefile differs from Makefile:3; host:3000 and 10:30 stay intact. " + attribution + ".",
    "filing an extensionless file leaked an unrelated line or a non-path colon was rewritten");
});

for (const surface of ["inline", "filing"]) test(`${surface}: diagnostic columns cannot become invented line citations`, () => {
  const finding = {
    title: "Diagnostic location", severity: "High", path: "ledger/ledger.go", line: 110, endLine: 112,
    explanation: "go vet: ledger/ledger.go:94:5: unreachable code; " +
      "other cmd/minos/main.go:110:3; own ledger/ledger.go:110:3; " +
      "nested ledger/ledger.go:94:5:2; wrong range ledger/ledger.go:110-111:3; " +
      "own range ledger/ledger.go:110-112:3.",
  };
  const explanation = "go vet: ledger/ledger.go: unreachable code; " +
    "other cmd/minos/main.go; own ledger/ledger.go:110:3; " +
    "nested ledger/ledger.go; wrong range ledger/ledger.go; " +
    "own range ledger/ledger.go:110-112:3.";
  if (surface === "inline") assert.equal(findingComment(finding).body,
    "**Advisory · High: Diagnostic location**\n\n" + explanation,
    "stripping a diagnostic location invented a line from its column suffix");
  if (surface === "filing") assert.equal(issueLogEntry({ ...finding, kind: "advisory-finding" }, attribution),
    "- **Advisory · High: Diagnostic location** (`ledger/ledger.go:110`) — " + explanation + " " + attribution + ".",
    "filing a diagnostic location invented a line from its column suffix");
});
