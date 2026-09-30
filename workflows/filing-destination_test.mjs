import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import test from "node:test";
import { execFileSync, spawn } from "node:child_process";
import { createServer } from "node:http";
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import { appendFilingEntries, deliverFilingEntries, filingMarker, validFilingDestination } from "./filing-destination.mjs";

// The filing drives real git against a bare "origin" the test owns and a
// fixture forge that answers the repository lookup, so what these tests
// prove is the delivered shape: an entry lands in the named file on the
// destination's default branch, once, under the configured identity, and a
// failed delivery leaves the material in the run record with no fallback.

const fileTriageCli = fileURLToPath(new URL("./file-triage.mjs", import.meta.url));
const identity = { name: "Review Bot", email: "review-bot@example.invalid" };
const source = { owner: "owner", repo: "repository", pr: "17", date: "2026-09-12" };

function git(directory, ...args) {
  return execFileSync("git", ["-C", directory, ...args], { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
}

// The CLI answers its forge lookup against a server in this process, so it
// runs asynchronously: a synchronous spawn would block the loop the fixture
// forge needs to reply on.
function runCli(args, env) {
  return new Promise((resolve) => {
    const child = spawn(process.execPath, [fileTriageCli, ...args], { env, stdio: ["ignore", "pipe", "pipe"] });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => { stdout += chunk; });
    child.stderr.on("data", (chunk) => { stderr += chunk; });
    child.on("close", (status) => resolve({ status, stdout, stderr }));
  });
}

// A destination repository with a bare origin, plus a fixture forge whose
// repository lookup names that origin as the clone URL.
async function destinationFixture(t, { issues = "# Issues\n\n- An existing entry.\n", branch = "main", repositories = ["owner/plans"] } = {}) {
  const scratch = mkdtempSync(join(tmpdir(), "filing-destination-"));
  const origin = join(scratch, "origin.git");
  const seed = join(scratch, "seed");
  execFileSync("git", ["init", "--quiet", "--bare", `--initial-branch=${branch}`, origin]);
  execFileSync("git", ["init", "--quiet", `--initial-branch=${branch}`, seed]);
  git(seed, "config", "user.name", "Seed");
  git(seed, "config", "user.email", "seed@example.invalid");
  writeFileSync(join(seed, "README.md"), "# Plans\n");
  if (issues !== null) writeFileSync(join(seed, "ISSUES.md"), issues);
  git(seed, "add", ".");
  git(seed, "commit", "--quiet", "-m", "seed");
  git(seed, "push", "--quiet", origin, branch);
  const lookups = [];
  const forge = createServer((request, response) => {
    lookups.push({ url: request.url, authorization: request.headers.authorization || null });
    const repository = repositories.find((name) => request.url === `/api/v1/repos/${name}`);
    if (!repository) {
      response.writeHead(404);
      response.end("not found");
      return;
    }
    response.writeHead(200, { "Content-Type": "application/json" });
    response.end(JSON.stringify({ clone_url: origin, default_branch: branch }));
  });
  await new Promise((resolve) => forge.listen(0, "127.0.0.1", resolve));
  t.after(() => forge.close());
  const runDir = join(scratch, "run");
  return {
    scratch, origin, seed, branch, lookups, runDir,
    apiBase: `http://127.0.0.1:${forge.address().port}`,
    originFile: (path = "ISSUES.md") => git(seed, "--git-dir", origin, "show", `${branch}:${path}`),
    deliver: (overrides) => deliverFilingEntries({
      destination: { kind: "file", repository: "owner/plans", path: "ISSUES.md" },
      entries: [advisory],
      source, reviewedRepository: "owner/repository", runDir, identity,
      apiBase: `http://127.0.0.1:${forge.address().port}`,
      ...overrides,
    }),
  };
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

test("the file kind appends to the named repository's default branch, committed under the configured identity", async (t) => {
  const fixture = await destinationFixture(t, { branch: "trunk" });
  const outcome = await fixture.deliver({ entries: [advisory, observation, misconfiguration] });
  assert.deepEqual(outcome, { kind: "file", outcome: "filed", written: 2, location: "owner/plans:ISSUES.md" });
  const issues = fixture.originFile();
  assert.match(issues, /^# Issues\n\n- An existing entry\.\n/);
  assert.match(issues, /\n- \*\*Advisory · Medium: Lost update on concurrent write\*\* \(`internal\/store\.go:41`\)[^\n]*Filed by Review Bot from owner\/repository#17, 2026-09-12\. <!-- minos:[A-Za-z0-9_-]+ -->\n/);
  assert.doesNotMatch(issues, /Unrelated nil deref|Unverified observation/);
  assert.match(issues, /\n- \*\*Review brief misconfiguration: Security brief\*\* \(`\.review\/security\.md`\)/);
  const log = git(fixture.seed, "--git-dir", fixture.origin, "log", "-1", "--format=%an <%ae> %s", "trunk");
  assert.equal(log.trim(), "Review Bot <review-bot@example.invalid> issues: record review material for triage");
  assert.deepEqual(fixture.lookups.map((lookup) => lookup.url), ["/api/v1/repos/owner/plans"]);
  const clone = join(fixture.runDir, "filing", "owner--plans");
  assert.equal(git(clone, "status", "--porcelain"), "", "the filing clone is left clean");
});

test("the file kind without a repository writes to the reviewed repository, at a nested path it creates", async (t) => {
  const fixture = await destinationFixture(t, { repositories: ["owner/repository"] });
  const outcome = await fixture.deliver({ destination: { kind: "file", path: "docs/review/ISSUES.md" } });
  assert.deepEqual(outcome, { kind: "file", outcome: "filed", written: 1, location: "owner/repository:docs/review/ISSUES.md" });
  assert.match(fixture.originFile("docs/review/ISSUES.md"), /^# Issues\n\n- \*\*Advisory/);
  assert.deepEqual(fixture.lookups.map((lookup) => lookup.url), ["/api/v1/repos/owner/repository"]);
});

test("an entry already at the destination is not filed twice", async (t) => {
  const fixture = await destinationFixture(t, { issues: null });
  assert.equal((await fixture.deliver()).written, 1);
  const again = await fixture.deliver({ entries: [advisory, misconfiguration] });
  assert.deepEqual(again, { kind: "file", outcome: "filed", written: 1, location: "owner/plans:ISSUES.md" });
  const issues = fixture.originFile();
  assert.equal(issues.split(filingMarker(advisory)).length - 1, 1, "the advisory finding appears once");
  assert.equal(issues.split(filingMarker(misconfiguration)).length - 1, 1);
  assert.deepEqual(
    await fixture.deliver({ entries: [advisory, misconfiguration] }),
    { kind: "file", outcome: "filed", written: 0, location: "owner/plans:ISSUES.md" },
    "nothing new means no commit",
  );
});

test("the filing appends to what the destination holds at delivery time", async (t) => {
  const fixture = await destinationFixture(t);
  writeFileSync(join(fixture.seed, "ISSUES.md"), "# Issues\n\n- An existing entry.\n- A newer entry the operator added mid-run.\n");
  git(fixture.seed, "commit", "--quiet", "-am", "operator edit");
  git(fixture.seed, "push", "--quiet", fixture.origin, "main");
  assert.equal((await fixture.deliver()).outcome, "filed");
  assert.match(fixture.originFile(), /A newer entry the operator added mid-run\.\n\n- \*\*Advisory/);
});

test("the marker keys on kind, site and title so one defect files once across heads", () => {
  assert.equal(filingMarker(advisory), filingMarker({ ...advisory, id: "other:9", explanation: "reworded", title: "  Lost   update on concurrent write " }));
  assert.notEqual(filingMarker(advisory), filingMarker({ ...advisory, kind: "out-of-scope-observation" }));
  assert.notEqual(filingMarker(advisory), filingMarker({ ...advisory, line: 42 }));
  assert.equal(filingMarker(misconfiguration), filingMarker({ ...misconfiguration, reason: "other reason" }));
  const { text, written } = appendFilingEntries("", [advisory, advisory], "Filed by Minos from o/r#1, 2026-09-12");
  assert.equal(written, 1, "a duplicate within one batch files once");
  assert.match(text, /^# Issues\n\n- \*\*Advisory/);
});

test("the none kind discards, an observation-only batch files nothing, and the unimplemented kinds stay unfiled with the reason", async (t) => {
  const fixture = await destinationFixture(t);
  assert.deepEqual(await fixture.deliver({ destination: { kind: "none" } }), { kind: "none", outcome: "discarded", written: 0 });
  assert.deepEqual(await fixture.deliver({ entries: [observation] }), { kind: "file", outcome: "nothing-to-file", written: 0 });
  assert.deepEqual(await fixture.deliver({ entries: [] }), { kind: "file", outcome: "nothing-to-file", written: 0 });
  for (const destination of [{ kind: "issue", repository: "owner/plans" }, { kind: "pull-request-comment" }]) {
    const outcome = await fixture.deliver({ destination });
    assert.equal(outcome.kind, destination.kind);
    assert.equal(outcome.outcome, "unfiled");
    assert.match(outcome.reason, /not implemented in this build/);
  }
  assert.equal(fixture.lookups.length, 0, "none of these touches the forge");
  assert.match(fixture.originFile(), /^# Issues\n\n- An existing entry\.\n$/);
});

test("a refused push, an unknown repository, an empty credential or missing attribution leaves the material unfiled", async (t) => {
  const fixture = await destinationFixture(t);
  writeFileSync(join(fixture.origin, "hooks", "pre-receive"), "#!/bin/sh\necho 'refused by policy' >&2\nexit 1\n", { mode: 0o755 });
  const refused = await fixture.deliver();
  assert.equal(refused.outcome, "unfiled");
  assert.match(refused.reason, /^filing to owner\/plans:ISSUES\.md failed: /);
  assert.doesNotMatch(fixture.originFile(), /Lost update/);

  const unknown = await fixture.deliver({ destination: { kind: "file", repository: "owner/absent", path: "ISSUES.md" } });
  assert.deepEqual(unknown, { kind: "file", outcome: "unfiled", reason: "filing to owner/absent:ISSUES.md failed: repository lookup for owner/absent returned HTTP 404" });

  const credential = join(fixture.scratch, "empty.token");
  writeFileSync(credential, "\n");
  assert.deepEqual(await fixture.deliver({ credentialFile: credential }), { kind: "file", outcome: "unfiled", reason: "forge credential file is empty" });
  assert.deepEqual(await fixture.deliver({ source: { owner: "owner" } }), { kind: "file", outcome: "unfiled", reason: "the orientation record carries no source attribution" });
  assert.deepEqual(await fixture.deliver({ apiBase: null }), { kind: "file", outcome: "unfiled", reason: "filing to owner/plans:ISSUES.md failed: MINOS_API_BASE is required to locate the filing repository" });
});

test("a configured credential reaches the forge lookup and the clone", async (t) => {
  const fixture = await destinationFixture(t);
  const credential = join(fixture.scratch, "forge.token");
  writeFileSync(credential, "forge-token-719\n");
  assert.equal((await fixture.deliver({ credentialFile: credential })).outcome, "filed");
  assert.deepEqual(fixture.lookups.map((lookup) => lookup.authorization), ["token forge-token-719"]);
});

test("the destination validator admits exactly the configured shapes", () => {
  for (const valid of [
    { kind: "file", path: "ISSUES.md" }, { kind: "file", repository: "o/r", path: "a/b.md" },
    { kind: "issue", repository: "o/r" }, { kind: "pull-request-comment" }, { kind: "none" },
  ]) assert.equal(validFilingDestination(valid), true, JSON.stringify(valid));
  for (const invalid of [null, "file", { kind: "email" }, { kind: "file" }, { kind: "file", path: "" }, { kind: "issue" }, { kind: "none", path: 3 }])
    assert.equal(validFilingDestination(invalid), false, JSON.stringify(invalid));
});

test("the file-triage CLI delivers to the destination the environment names and never writes a fallback review", async (t) => {
  const fixture = await destinationFixture(t);
  const entriesPath = join(fixture.scratch, "triage-entries.json");
  const orientationPath = join(fixture.scratch, "orientation.json");
  writeFileSync(entriesPath, JSON.stringify([advisory, observation, misconfiguration]));
  writeFileSync(orientationPath, JSON.stringify({ repository: "/workspace", guidance: [], source }));
  const env = {
    ...process.env,
    MINOS_FILING_DESTINATION: JSON.stringify({ kind: "file", repository: "owner/plans", path: "ISSUES.md" }),
    MINOS_API_BASE: fixture.apiBase,
    MINOS_OWNER: "owner",
    MINOS_REPO_NAME: "repository",
    MINOS_RUN_DIR: fixture.runDir,
    MINOS_COMMIT_AUTHOR_NAME: identity.name,
    MINOS_COMMIT_AUTHOR_EMAIL: identity.email,
  };

  const filed = await runCli([entriesPath, orientationPath], env);
  assert.equal(filed.status, 0, filed.stderr);
  assert.deepEqual(JSON.parse(filed.stdout), { kind: "file", outcome: "filed", written: 2, location: "owner/plans:ISSUES.md" });
  assert.equal(existsSync(join(fixture.runDir, "publication")), false, "a filed batch writes no pull-request payload");

  const discarded = await runCli([entriesPath, orientationPath], { ...env, MINOS_FILING_DESTINATION: JSON.stringify({ kind: "none" }) });
  assert.deepEqual(JSON.parse(discarded.stdout), { kind: "none", outcome: "discarded", written: 0 });

  writeFileSync(entriesPath, "[]");
  const empty = await runCli([entriesPath, orientationPath], env);
  assert.deepEqual(JSON.parse(empty.stdout), { kind: "file", outcome: "nothing-to-file", written: 0 });

  const usage = await runCli(["one"], env);
  assert.equal(usage.status, 2);
  assert.match(usage.stderr, /^usage: node workflows\/file-triage\.mjs ENTRIES_FILE ORIENTATION/);
  for (const [name, value] of [["MINOS_FILING_DESTINATION", "not json"], ["MINOS_FILING_DESTINATION", '{"kind":"email"}'], ["MINOS_RUN_DIR", ""], ["MINOS_COMMIT_AUTHOR_EMAIL", ""]]) {
    const rejected = await runCli([entriesPath, orientationPath], { ...env, [name]: value });
    assert.equal(rejected.status, 2, `${name}=${JSON.stringify(value)} is a usage error`);
    assert.match(rejected.stderr, new RegExp(name));
  }
});
