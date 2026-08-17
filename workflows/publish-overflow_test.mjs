import { after, test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const scriptPath = fileURLToPath(new URL("./publish-overflow.mjs", import.meta.url));

const scratchDirs = [];
after(() => {
  for (const dir of scratchDirs) rmSync(dir, { recursive: true, force: true });
});

function finding(title, overrides = {}) {
  return {
    title,
    severity: "Low",
    confidence: 88,
    path: "internal/example.go",
    line: 14,
    explanation: `${title} narrows the guard`,
    ...overrides,
  };
}

function annexeOrientation(annexe, date = "2026-08-17") {
  return {
    grounding: "annexe",
    annexe,
    source: { owner: "exact-owner", repo: "exact-repo", pr: "37", date },
  };
}

function fixture(orientation, findings) {
  const dir = mkdtempSync(join(tmpdir(), "publish-overflow-"));
  scratchDirs.push(dir);
  const orientationPath = join(dir, "orientation.json");
  const findingsPath = join(dir, "findings.json");
  writeFileSync(orientationPath, JSON.stringify(orientation));
  writeFileSync(findingsPath, JSON.stringify(findings));
  return { dir, orientationPath, findingsPath };
}

function run(args) {
  return spawnSync(process.execPath, [scriptPath, ...args], { encoding: "utf8" });
}

// The annexe cases drive the real git effect: a bare origin, a working
// clone the orientation points at, and assertions on what was pushed.
function annexeFixture() {
  const root = mkdtempSync(join(tmpdir(), "publish-overflow-annexe-"));
  scratchDirs.push(root);
  const origin = join(root, "origin.git");
  const clone = join(root, "annexe");
  const git = (cwd, ...args) => execFileSync("git", ["-C", cwd, ...args], { encoding: "utf8" });
  execFileSync("git", ["init", "--bare", "-q", "--initial-branch=main", origin]);
  execFileSync("git", ["clone", "-q", origin, clone]);
  git(clone, "checkout", "-q", "-B", "main");
  git(clone, "config", "user.name", "Minos Test");
  git(clone, "config", "user.email", "minos-test@example.invalid");
  git(clone, "config", "commit.gpgsign", "false");
  writeFileSync(join(clone, "README.md"), "# Annexe fixture\n");
  git(clone, "add", "README.md");
  git(clone, "commit", "-q", "-m", "seed");
  git(clone, "push", "-q", "origin", "HEAD:main");
  return { root, origin, clone, git };
}

test("missing arguments exit with usage", () => {
  const result = run([]);
  assert.equal(result.status, 2);
  assert.match(result.stderr, /usage: node workflows\/publish-overflow\.mjs ORIENTATION FINDINGS/);
});

test("repository grounding returns one pull-request payload", () => {
  const { orientationPath, findingsPath } = fixture({ grounding: "repository" }, [
    finding("stale cursor"),
    finding("orphan sweep", { path: "scripts/run-body/archive-run", line: 3, severity: "Medium" }),
  ]);
  const result = run([orientationPath, findingsPath]);
  assert.equal(result.status, 0);
  const payload = JSON.parse(result.stdout);
  assert.equal(payload.destination, "pull-request");
  assert.equal(payload.body, "Additional code findings for project triage.");
  assert.equal(payload.comments.length, 2);
  assert.deepEqual(
    payload.comments.map((comment) => ({ path: comment.path, line: comment.line })),
    [
      { path: "internal/example.go", line: 14 },
      { path: "scripts/run-body/archive-run", line: 3 },
    ],
  );
  assert.match(payload.comments[0].body, /\*\*stale cursor\*\*/);
  assert.match(payload.comments[0].body, /Severity: Low\. Confidence: 88\./);
});

test("unknown grounding and a missing annexe path both refuse", () => {
  const unknown = fixture({ grounding: "timeline" }, [finding("any")]);
  assert.notEqual(run([unknown.orientationPath, unknown.findingsPath]).status, 0);
  const pathless = fixture({ grounding: "annexe" }, [finding("any")]);
  assert.notEqual(run([pathless.orientationPath, pathless.findingsPath]).status, 0);
});

test("non-array findings refuse", () => {
  const { orientationPath, findingsPath } = fixture({ grounding: "repository" }, [finding("shape")]);
  writeFileSync(findingsPath, JSON.stringify({ not: "an array" }));
  assert.notEqual(run([orientationPath, findingsPath]).status, 0);
});

test("annexe grounding appends marked entries, commits, and pushes to origin", () => {
  const annexe = annexeFixture();
  const { orientationPath, findingsPath } = fixture(annexeOrientation(annexe.clone), [
    finding("stale cursor"),
    finding("orphan sweep", { severity: "Medium", line: 9 }),
  ]);
  const result = run([orientationPath, findingsPath]);
  assert.equal(result.status, 0);
  assert.deepEqual(JSON.parse(result.stdout), { destination: "annexe", written: 2, pushed: true });

  const contents = readFileSync(join(annexe.clone, "ISSUES.md"), "utf8");
  assert.match(contents, /^# Issues\n/);
  assert.match(
    contents,
    /- Review finding: stale cursor \(Low, internal\/example\.go:14\) — stale cursor narrows the guard\. Filed by Minos from exact-owner\/exact-repo#37, 2026-08-17\. <!-- review-finding:[A-Za-z0-9_-]+ -->/,
  );
  assert.match(contents, /- Review finding: orphan sweep \(Medium, internal\/example\.go:9\)/);

  const pushed = annexe.git(annexe.origin, "log", "-1", "--format=%s");
  assert.match(pushed, /record review findings for triage/);
  const pushedTree = annexe.git(annexe.origin, "show", "main:ISSUES.md");
  assert.equal(pushedTree, contents);
});

test("an entry already carrying its marker is not rewritten, even retitled in case or spacing", () => {
  const annexe = annexeFixture();
  const first = fixture(annexeOrientation(annexe.clone), [finding("Stale  Cursor")]);
  assert.equal(run([first.orientationPath, first.findingsPath]).status, 0);
  const before = readFileSync(join(annexe.clone, "ISSUES.md"), "utf8");
  const headBefore = annexe.git(annexe.clone, "rev-parse", "HEAD");

  // The idempotency key normalises title case and whitespace, so a retry
  // that re-renders the same finding writes nothing and pushes nothing.
  const retry = fixture(annexeOrientation(annexe.clone, "2026-08-18"), [finding("stale cursor")]);
  const result = run([retry.orientationPath, retry.findingsPath]);
  assert.equal(result.status, 0);
  assert.deepEqual(JSON.parse(result.stdout), { destination: "annexe", written: 0, pushed: false });
  assert.equal(readFileSync(join(annexe.clone, "ISSUES.md"), "utf8"), before);
  assert.equal(annexe.git(annexe.clone, "rev-parse", "HEAD"), headBefore);
});

test("annexe grounding preserves an existing issues log and only appends", () => {
  const annexe = annexeFixture();
  const seeded = "# Issues\n\n- Existing entry the build already triaged\n";
  writeFileSync(join(annexe.clone, "ISSUES.md"), seeded);
  annexe.git(annexe.clone, "add", "ISSUES.md");
  annexe.git(annexe.clone, "commit", "-q", "-m", "seed issues log");
  annexe.git(annexe.clone, "push", "-q", "origin", "HEAD:main");

  const { orientationPath, findingsPath } = fixture(annexeOrientation(annexe.clone), [
    finding("orphan sweep"),
  ]);
  assert.equal(run([orientationPath, findingsPath]).status, 0);
  const contents = readFileSync(join(annexe.clone, "ISSUES.md"), "utf8");
  assert.ok(contents.startsWith(seeded));
  assert.match(contents, /- Review finding: orphan sweep /);
});

test("held diagnosis keeps finding details under its distinct attributed label", () => {
  const annexe = annexeFixture();
  const held = finding("target cache invalidation", {
    kind: "held-diagnosis",
    severity: "target-side",
    path: "internal/cache.go",
    line: 52,
    explanation: "target commits abc123 and def456 leave stale state through the cache refresh mechanism",
  });
  const { orientationPath, findingsPath } = fixture(annexeOrientation(annexe.clone), [held]);

  const result = run([orientationPath, findingsPath]);
  assert.equal(result.status, 0);
  assert.deepEqual(JSON.parse(result.stdout), { destination: "annexe", written: 1, pushed: true });
  assert.equal(result.stdout.trim(), JSON.stringify({ destination: "annexe", written: 1, pushed: true }));

  const contents = annexe.git(annexe.origin, "show", "main:ISSUES.md");
  assert.match(
    contents,
    /- Held diagnosis: target cache invalidation \(target-side, internal\/cache\.go:52\) — target commits abc123 and def456 leave stale state through the cache refresh mechanism\. Filed by Minos from exact-owner\/exact-repo#37, 2026-08-17\./,
  );
});
