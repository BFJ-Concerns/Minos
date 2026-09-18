import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import test from "node:test";
import { execFileSync, spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import { appendIssueLogEntries, fileIssueLogEntries, issueLogMarker } from "./issue-log-filing.mjs";

// The filing drives real git against a bare "origin" the test owns, so
// what these tests prove is the delivered shape: an entry lands in the
// annexe's ISSUES.md on origin, once, under the bot identity, and failed
// filing leaves the material in the run record.

const fileTriageCli = fileURLToPath(new URL("./file-triage.mjs", import.meta.url));

function git(directory, ...args) {
  return execFileSync("git", ["-C", directory, ...args], { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
}

// An annexe with a bare origin, cloned the way setup-workspace leaves it.
function annexeFixture({ issues = "# Issues\n\n- An existing entry.\n" } = {}) {
  const scratch = mkdtempSync(join(tmpdir(), "issue-log-filing-"));
  const origin = join(scratch, "origin.git");
  const seed = join(scratch, "seed");
  execFileSync("git", ["init", "--quiet", "--bare", "--initial-branch=main", origin]);
  execFileSync("git", ["init", "--quiet", "--initial-branch=main", seed]);
  git(seed, "config", "user.name", "Seed");
  git(seed, "config", "user.email", "seed@example.invalid");
  writeFileSync(join(seed, "README.md"), "# Commission\n");
  if (issues !== null) writeFileSync(join(seed, "ISSUES.md"), issues);
  git(seed, "add", ".");
  git(seed, "commit", "--quiet", "-m", "seed");
  git(seed, "push", "--quiet", origin, "main");
  const annexe = join(scratch, "repository-Annexe");
  execFileSync("git", ["clone", "--quiet", origin, annexe]);
  const orientation = {
    grounding: "annexe",
    annexe,
    source: { owner: "owner", repo: "repository", pr: "17", date: "2026-09-12" },
  };
  return { scratch, origin, seed, annexe, orientation };
}

function originIssues(fixture) {
  return git(fixture.seed, "--git-dir", join(fixture.origin), "show", "main:ISSUES.md");
}

const advisory = {
  kind: "advisory-finding",
  id: "specialist-2:1",
  title: "Lost update on concurrent write",
  severity: "Medium",
  path: "internal/store.go",
  line: 41,
  explanation: "Two writers read the same revision and the later write wins silently.",
};
const observation = {
  kind: "out-of-scope-observation",
  id: "specialist-1:observation:1",
  title: "Unrelated nil deref",
  path: "pkg/server.go",
  line: 12,
  explanation: "A nil map write outside the reviewed range.",
};
const misconfiguration = {
  kind: "review-brief-misconfiguration",
  brief: ".review/security.md",
  title: "Security brief",
  reason: "declared scope directory does not exist",
};

test("entries are appended to the annexe's ISSUES.md, committed as Minos and pushed to origin", () => {
  const fixture = annexeFixture();
  const outcome = fileIssueLogEntries({ orientation: fixture.orientation, entries: [advisory, observation, misconfiguration] });
  assert.deepEqual(outcome, { destination: "annexe", written: 2, location: "repository-Annexe/ISSUES.md" });
  const issues = originIssues(fixture);
  assert.match(issues, /^# Issues\n\n- An existing entry\.\n/);
  assert.match(issues, /\n- \*\*Advisory · Medium: Lost update on concurrent write\*\* \(`internal\/store\.go:41`\)[^\n]*Filed by Minos from owner\/repository#17, 2026-09-12\. <!-- minos:[A-Za-z0-9_-]+ -->\n/);
  assert.doesNotMatch(issues, /Unrelated nil deref|Unverified observation/);
  assert.match(issues, /\n- \*\*Review brief misconfiguration: Security brief\*\* \(`\.review\/security\.md`\)/);
  const log = git(fixture.seed, "--git-dir", fixture.origin, "log", "-1", "--format=%an <%ae> %s", "main");
  assert.equal(log.trim(), "Minos <minos@example.invalid> issues: record review material for triage");
  assert.equal(git(fixture.annexe, "status", "--porcelain"), "", "the clone is left clean");
});

test("an entry already in the log is not filed twice, and a log that does not exist is created", () => {
  const fixture = annexeFixture({ issues: null });
  assert.deepEqual(
    fileIssueLogEntries({ orientation: fixture.orientation, entries: [advisory] }),
    { destination: "annexe", written: 1, location: "repository-Annexe/ISSUES.md" },
  );
  assert.match(originIssues(fixture), /^# Issues\n\n- \*\*Advisory/);
  const again = fileIssueLogEntries({ orientation: fixture.orientation, entries: [advisory, { ...misconfiguration }] });
  assert.deepEqual(again, { destination: "annexe", written: 1, location: "repository-Annexe/ISSUES.md" });
  const issues = originIssues(fixture);
  assert.equal(issues.split(issueLogMarker(advisory)).length - 1, 1, "the advisory finding appears once");
  assert.equal(issues.split(issueLogMarker(misconfiguration)).length - 1, 1);
  assert.deepEqual(
    fileIssueLogEntries({ orientation: fixture.orientation, entries: [advisory, misconfiguration] }),
    { destination: "annexe", written: 0, location: "repository-Annexe/ISSUES.md" },
    "nothing new means no commit",
  );
});

test("the filing appends to what origin holds now, not to what the run cloned", () => {
  const fixture = annexeFixture();
  writeFileSync(join(fixture.seed, "ISSUES.md"), "# Issues\n\n- An existing entry.\n- A newer entry the operator added mid-run.\n");
  git(fixture.seed, "commit", "--quiet", "-am", "operator edit");
  git(fixture.seed, "push", "--quiet", fixture.origin, "main");
  const outcome = fileIssueLogEntries({ orientation: fixture.orientation, entries: [advisory] });
  assert.equal(outcome.destination, "annexe");
  const issues = originIssues(fixture);
  assert.match(issues, /A newer entry the operator added mid-run\.\n\n- \*\*Advisory/);
});

test("the marker keys on kind, site and title so one defect files once across heads", () => {
  assert.equal(issueLogMarker(advisory), issueLogMarker({ ...advisory, id: "other:9", explanation: "reworded", title: "  Lost   update on concurrent write " }));
  assert.notEqual(issueLogMarker(advisory), issueLogMarker({ ...advisory, kind: "out-of-scope-observation" }));
  assert.notEqual(issueLogMarker(advisory), issueLogMarker({ ...advisory, line: 42 }));
  assert.equal(issueLogMarker(misconfiguration), issueLogMarker({ ...misconfiguration, reason: "other reason" }));
  const { text, written } = appendIssueLogEntries("", [advisory, advisory], "Filed by Minos from o/r#1, 2026-09-12");
  assert.equal(written, 1, "a duplicate within one batch files once");
  assert.match(text, /^# Issues\n\n- \*\*Advisory/);
});

test("with no annexe, a missing clone, or no attribution the material remains unfiled", () => {
  assert.deepEqual(
    fileIssueLogEntries({ orientation: { grounding: "repository", guidance: "/x/AGENTS.md" }, entries: [advisory] }),
    { destination: "unfiled", reason: "the project has no annexe" },
  );
  const fixture = annexeFixture();
  assert.deepEqual(
    fileIssueLogEntries({ orientation: { ...fixture.orientation, annexe: join(fixture.scratch, "gone") }, entries: [advisory] }),
    { destination: "unfiled", reason: "the annexe clone is missing from the run" },
  );
  assert.deepEqual(
    fileIssueLogEntries({ orientation: { ...fixture.orientation, source: { owner: "owner" } }, entries: [advisory] }),
    { destination: "unfiled", reason: "the orientation record carries no source attribution" },
  );
  assert.deepEqual(fileIssueLogEntries({ orientation: fixture.orientation, entries: [] }), { destination: "none", written: 0 });
});

test("a push the remote refuses leaves the material unfiled", () => {
  const fixture = annexeFixture();
  writeFileSync(join(fixture.origin, "hooks", "pre-receive"), "#!/bin/sh\necho 'refused by policy' >&2\nexit 1\n", { mode: 0o755 });
  const outcome = fileIssueLogEntries({ orientation: fixture.orientation, entries: [advisory] });
  assert.equal(outcome.destination, "unfiled");
  assert.match(outcome.reason, /^the annexe push failed: /);
  assert.doesNotMatch(originIssues(fixture), /Lost update/);
  const credential = join(fixture.scratch, "empty.token");
  writeFileSync(credential, "\n");
  assert.deepEqual(
    fileIssueLogEntries({ orientation: fixture.orientation, entries: [advisory], credentialFile: credential }),
    { destination: "unfiled", reason: "forge credential file is empty" },
  );
});

test("the file-triage CLI files to the annexe, never writes a fallback review", () => {
  const fixture = annexeFixture();
  const entriesPath = join(fixture.scratch, "triage-entries.json");
  const orientationPath = join(fixture.scratch, "orientation.json");
  writeFileSync(entriesPath, JSON.stringify([advisory, observation, misconfiguration]));
  writeFileSync(orientationPath, JSON.stringify(fixture.orientation));
  const outputDir = join(fixture.scratch, "publication");

  const filed = spawnSync(process.execPath, [fileTriageCli, outputDir, entriesPath, orientationPath], { encoding: "utf8" });
  assert.equal(filed.status, 0, filed.stderr);
  assert.deepEqual(JSON.parse(filed.stdout), { destination: "annexe", written: 2, location: "repository-Annexe/ISSUES.md" });
  assert.equal(existsSync(join(outputDir, "triage-review.md")), false, "a filed batch writes no fallback review");

  writeFileSync(orientationPath, JSON.stringify({ grounding: "repository", guidance: "/x/AGENTS.md" }));
  const fallback = spawnSync(process.execPath, [fileTriageCli, outputDir, entriesPath, orientationPath], { encoding: "utf8" });
  assert.equal(fallback.status, 0, fallback.stderr);
  const outcome = JSON.parse(fallback.stdout);
  assert.equal(outcome.destination, "unfiled");
  assert.equal(outcome.reason, "the project has no annexe");
  assert.equal(outcome.review, undefined);
  assert.equal(existsSync(outputDir), false, "no pull-request payload is written");

  writeFileSync(entriesPath, "[]");
  const empty = spawnSync(process.execPath, [fileTriageCli, outputDir, entriesPath, orientationPath], { encoding: "utf8" });
  assert.deepEqual(JSON.parse(empty.stdout), { destination: "none", written: 0 });

  const usage = spawnSync(process.execPath, [fileTriageCli, "one"], { encoding: "utf8" });
  assert.equal(usage.status, 2);
  assert.match(usage.stderr, /^usage: node workflows\/file-triage\.mjs OUTPUT_DIR ENTRIES_FILE ORIENTATION/);
});

test("an observation-only batch is never filed, even when an annexe is available", () => {
  const fixture = annexeFixture();
  const before = originIssues(fixture);
  assert.deepEqual(fileIssueLogEntries({ orientation: fixture.orientation, entries: [observation] }), { destination: "none", written: 0 });
  assert.equal(originIssues(fixture), before);
});
