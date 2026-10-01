import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import test from "node:test";
import { execFileSync, spawn } from "node:child_process";
import { createServer } from "node:http";
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { issueLogEntry } from "./finding-presentation.mjs";

import { appendFilingEntries, deliverFilingEntries, filingMarker, validFilingDestination } from "./filing-destination.mjs";

// The filing drives real git against a bare "origin" the test owns and a
// fixture forge that answers the repository lookup, so what these tests
// prove is the delivered shape: an entry lands in the named file on the
// destination's default branch, once, under the configured identity, and a
// failed delivery leaves the material in the run record with no fallback.

const fileTriageCli = fileURLToPath(new URL("./file-triage.mjs", import.meta.url));
const pushGuard = fileURLToPath(new URL("../scripts/run-body/pre-push-guard", import.meta.url));
const identity = { name: "Review Bot", email: "review-bot@example.invalid" };
const source = { owner: "owner", repo: "repository", pr: "17", date: "2026-09-12" };
const reviewedRepository = "owner/repository";
const protection = { headBranch: "feature", pushGuard };

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
  t.after(() => {
    forge.close();
    rmSync(scratch, { recursive: true, force: true });
  });
  const runDir = join(scratch, "run");
  return {
    scratch, origin, seed, lookups, runDir,
    apiBase: `http://127.0.0.1:${forge.address().port}`,
    originFile: (path = "ISSUES.md") => git(seed, "--git-dir", origin, "show", `${branch}:${path}`),
    deliver: (overrides) => deliverFilingEntries({
      destination: { kind: "file", repository: "owner/plans", path: "ISSUES.md" },
      entries: [advisory],
      source, reviewedRepository, runDir, identity, protection,
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
  const clone = join(fixture.runDir, "filing", "owner", "plans");
  assert.equal(git(clone, "status", "--porcelain"), "", "the filing clone is left clean");
});

test("the file kind without a repository writes to the reviewed repository, at a nested path it creates, under the push guard", async (t) => {
  const fixture = await destinationFixture(t, { repositories: ["owner/repository"] });
  const outcome = await fixture.deliver({ destination: { kind: "file", path: "docs/review/ISSUES.md" } });
  assert.deepEqual(outcome, { kind: "file", outcome: "filed", written: 1, location: "owner/repository:docs/review/ISSUES.md" });
  assert.match(fixture.originFile("docs/review/ISSUES.md"), /^# Issues\n\n- \*\*Advisory/);
  assert.deepEqual(fixture.lookups.map((lookup) => lookup.url), ["/api/v1/repos/owner/repository"]);
  const clone = join(fixture.runDir, "filing", "owner", "repository");
  assert.equal(readFileSync(join(clone, ".git", "minos-protected-ref"), "utf8"), "refs/heads/feature\n");
  assert.equal(readFileSync(join(clone, ".git", "hooks", "pre-push"), "utf8"), readFileSync(pushGuard, "utf8"));
});

test("filing to the reviewed repository never reaches the pull-request branch", async (t) => {
  // The pull request's own branch is the repository's default branch — a
  // release-style pull request — so the default-branch write is refused.
  const fixture = await destinationFixture(t, { branch: "feature", repositories: ["owner/repository"] });
  const outcome = await fixture.deliver({ destination: { kind: "file", path: "ISSUES.md" } });
  assert.equal(outcome.outcome, "unfiled");
  assert.match(outcome.reason, /default branch feature is the pull-request branch, which Minos never writes/);
  assert.match(fixture.originFile(), /^# Issues\n\n- An existing entry\.\n$/);
  assert.deepEqual(
    await fixture.deliver({ destination: { kind: "file", path: "ISSUES.md" }, protection: null }),
    { kind: "file", outcome: "unfiled", reason: "filing to the reviewed repository needs the pull-request branch and the push guard (protection)" },
  );
  // A named secondary repository whose default branch happens to share the
  // name is not the pull-request branch and files normally.
  const other = await destinationFixture(t, { branch: "feature" });
  assert.equal((await other.deliver()).outcome, "filed");
});

test("an entry already at the destination is not filed twice", async (t) => {
  const fixture = await destinationFixture(t, { issues: null });
  assert.equal((await fixture.deliver()).written, 1);
  const again = await fixture.deliver({ entries: [advisory, misconfiguration] });
  assert.deepEqual(again, { kind: "file", outcome: "filed", written: 1, location: "owner/plans:ISSUES.md" });
  const issues = fixture.originFile();
  assert.equal(issues.split(filingMarker(advisory, reviewedRepository)).length - 1, 1, "the advisory finding appears once");
  assert.equal(issues.split(filingMarker(misconfiguration, reviewedRepository)).length - 1, 1);
  assert.deepEqual(
    await fixture.deliver({ entries: [advisory, misconfiguration] }),
    { kind: "file", outcome: "filed", written: 0, location: "owner/plans:ISSUES.md" },
    "nothing new means no commit",
  );
});

test("an unreadable guidance source files once, as a configuration diagnostic keyed by repository and source", async (t) => {
  const guidance = {
    kind: "guidance-source-misconfiguration", repository: reviewedRepository,
    sourceRepository: "owner/guidance", path: "docs/INTENT.md", reason: "is missing or empty",
  };
  const fixture = await destinationFixture(t, { issues: null });
  assert.equal((await fixture.deliver({ entries: [guidance] })).written, 1);
  assert.deepEqual(
    await fixture.deliver({ entries: [{ ...guidance }] }),
    { kind: "file", outcome: "filed", written: 0, location: "owner/plans:ISSUES.md" },
    "a second delivery files nothing",
  );
  const issues = fixture.originFile();
  assert.match(issues, /\n- \*\*Guidance source misconfiguration: owner\/guidance:docs\/INTENT\.md\*\* — configured guidance for /);
  assert.equal(issues.split(filingMarker(guidance, reviewedRepository)).length - 1, 1);
  assert.notEqual(filingMarker(guidance, reviewedRepository), filingMarker(guidance, "other/project"));
  assert.notEqual(filingMarker(guidance, reviewedRepository), filingMarker({ ...guidance, path: "docs/OTHER.md" }, reviewedRepository));
  assert.notEqual(filingMarker(guidance, reviewedRepository), filingMarker({ ...guidance, sourceRepository: null }, reviewedRepository));
});

test("the filing appends to what the destination holds at delivery time", async (t) => {
  const fixture = await destinationFixture(t);
  writeFileSync(join(fixture.seed, "ISSUES.md"), "# Issues\n\n- An existing entry.\n- A newer entry the operator added mid-run.\n");
  git(fixture.seed, "commit", "--quiet", "-am", "operator edit");
  git(fixture.seed, "push", "--quiet", fixture.origin, "main");
  assert.equal((await fixture.deliver()).outcome, "filed");
  assert.match(fixture.originFile(), /A newer entry the operator added mid-run\.\n\n- \*\*Advisory/);
});

test("the marker keys on repository, kind, site and title so one defect files once across heads", () => {
  const marker = (entry, repository = reviewedRepository) => filingMarker(entry, repository);
  assert.equal(marker(advisory), marker({ ...advisory, id: "other:9", explanation: "reworded", title: "  Lost   update on concurrent write " }));
  assert.notEqual(marker(advisory), marker({ ...advisory, kind: "out-of-scope-observation" }));
  assert.notEqual(marker(advisory), marker({ ...advisory, line: 42 }));
  assert.notEqual(marker(advisory), marker(advisory, "other/project"), "two projects sharing a destination file the same site separately");
  assert.equal(marker(misconfiguration), marker({ ...misconfiguration, reason: "other reason" }));
  const { text, written } = appendFilingEntries("", [advisory, advisory], "Filed by Minos from o/r#1, 2026-09-12", reviewedRepository);
  assert.equal(written, 1, "a duplicate within one batch files once");
  assert.match(text, /^# Issues\n\n- \*\*Advisory/);
});

test("the none kind discards, an observation-only batch files nothing, and the unavailable comment kind stays unfiled with the reason", async (t) => {
  const fixture = await destinationFixture(t);
  assert.deepEqual(await fixture.deliver({ destination: { kind: "none" } }), { kind: "none", outcome: "discarded", written: 0 });
  assert.deepEqual(await fixture.deliver({ entries: [observation] }), { kind: "file", outcome: "nothing-to-file", written: 0 });
  assert.deepEqual(await fixture.deliver({ entries: [] }), { kind: "file", outcome: "nothing-to-file", written: 0 });
  for (const destination of [{ kind: "pull-request-comment" }]) {
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
  for (const invalid of [
    null, "file", { kind: "email" }, { kind: "file" }, { kind: "file", path: "" }, { kind: "issue" }, { kind: "none", path: 3 },
    { kind: "none", path: "ISSUES.md" }, { kind: "pull-request-comment", repository: "o/r" }, { kind: "issue", repository: "o/r", path: "x" },
    { kind: "file", path: "../ISSUES.md" }, { kind: "file", path: "/etc/ISSUES.md" }, { kind: "file", repository: "http://x/y", path: "a" },
    { kind: "file", path: "a", branch: "main" },
  ]) assert.equal(validFilingDestination(invalid), false, JSON.stringify(invalid));
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
    MINOS_HEAD_BRANCH: "feature",
    MINOS_SETUP_WORKSPACE: join(dirname(pushGuard), "setup-workspace"),
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
  for (const [name, value] of [["MINOS_FILING_DESTINATION", "not json"], ["MINOS_FILING_DESTINATION", '{"kind":"email"}'], ["MINOS_RUN_DIR", ""], ["MINOS_COMMIT_AUTHOR_EMAIL", ""], ["MINOS_HEAD_BRANCH", ""], ["MINOS_SETUP_WORKSPACE", ""]]) {
    const rejected = await runCli([entriesPath, orientationPath], { ...env, [name]: value });
    assert.equal(rejected.status, 2, `${name}=${JSON.stringify(value)} is a usage error`);
    assert.match(rejected.stderr, new RegExp(name));
  }
});

// Real CLI -> compiled minos -> real Forgejo adaptation -> HTTP fixture.
// The fixture stores only payloads the adaptation POSTs; assertions read
// those writes and the delivery result rather than inventing filed entries.
test("issue destination delivers marked entries through the forge command", async (t) => {
  const scratch = mkdtempSync(join(tmpdir(), "issue-filing-cli-"));
  t.after(() => rmSync(scratch, { recursive: true, force: true }));
  const binary = join(scratch, "minos");
  const root = fileURLToPath(new URL("../", import.meta.url));
  execFileSync("go", ["build", "-o", binary, "./cmd/minos"], { cwd: root });

  async function fixture(t, { existing = [], refusal = false, hideWrites = false, wrongLogin = false, malformed = false, corruptWrite = false, lostResponse = false, refuseAfter = Infinity, repeatPages = false, wrongReadBackAuthor = false } = {}) {
    const directory = mkdtempSync(join(scratch, "case-"));
    const token = join(directory, "token");
    writeFileSync(token, "fixture-token\n");
    const accounts = new Map([["fixture-token", { login: wrongLogin ? "other" : identity.name }]]);
    const posted = [];
    const reads = [];
    const issues = [...existing];
    const forge = createServer(async (request, response) => {
      const url = new URL(request.url, "http://fixture");
      response.setHeader("Content-Type", "application/json");
      const account = accounts.get((request.headers.authorization || "").replace(/^token /, ""));
      if (!account) {
        response.writeHead(401); response.end("{}"); return;
      }
      if (url.pathname === "/api/v1/user") {
        response.end(JSON.stringify(account)); return;
      }
      if (url.pathname === "/api/v1/repos/owner/plans/issues") {
        if (request.method === "GET") {
          reads.push(Object.fromEntries(url.searchParams));
          if (malformed) { response.end('{"message":"not an issue list"}'); return; }
          const state = url.searchParams.get("state") || "open";
          const visible = (hideWrites ? existing : issues)
            .filter((issue) => state === "all" || (issue.state || "open") === state)
            .map((issue) => wrongReadBackAuthor ? { ...issue, user: { login: "another-account" } } : issue);
          // The server caps pages below the requested limit, as a forge may.
          const page = Number(url.searchParams.get("page"));
          response.end(JSON.stringify(repeatPages ? visible.slice(0, 2) : visible.slice((page - 1) * 2, page * 2))); return;
        }
        if (request.method === "POST") {
          let body = "";
          for await (const chunk of request) body += chunk;
          const payload = JSON.parse(body);
          posted.push(payload);
          if (refusal || posted.length > refuseAfter) { response.writeHead(403); response.end('{"message":"forbidden"}'); return; }
          const issue = { ...payload, number: issues.length + 1, state: "open", user: { ...account } };
          if (corruptWrite) issue.body += "Wrong stored content";
          issues.push(issue);
          if (lostResponse) { response.writeHead(500); response.end('{"message":"response lost"}'); return; }
          response.writeHead(201); response.end(JSON.stringify(issue)); return;
        }
      }
      response.writeHead(404); response.end("{}");
    });
    await new Promise((resolve) => forge.listen(0, "127.0.0.1", resolve));
    t.after(() => forge.close());
    const apiBase = `http://127.0.0.1:${forge.address().port}`;
    writeFileSync(join(directory, "service.toml"), `
[service]
bot-login = "Review Bot"
[listener]
bind = "127.0.0.1:0"
[runs]
dir = "${directory}"
[forges.forgejo]
adaptation = "${join(root, "scripts", "adaptations", "forgejo")}"
api-base = "${apiBase}"
credential-file = "${token}"
webhook-secret-file = "${token}"
`);
    const entriesPath = join(directory, "entries.json");
    const orientationPath = join(directory, "orientation.json");
    writeFileSync(orientationPath, JSON.stringify({ source }));
    const env = {
      ...process.env, MINOS_BIN: binary, MINOS_CONFIG: directory, MINOS_FORGE: "forgejo", MINOS_PR: "17",
      MINOS_FILING_DESTINATION: JSON.stringify({ kind: "issue", repository: "owner/plans" }),
      MINOS_API_BASE: apiBase, MINOS_OWNER: "owner", MINOS_REPO_NAME: "repository", MINOS_RUN_DIR: directory,
      MINOS_COMMIT_AUTHOR_NAME: identity.name, MINOS_COMMIT_AUTHOR_EMAIL: identity.email,
      MINOS_HEAD_BRANCH: "feature", MINOS_SETUP_WORKSPACE: join(dirname(pushGuard), "setup-workspace"),
    };
    return { posted, reads, issues, directory, async deliver(entries, head = "first-head") {
      writeFileSync(entriesPath, JSON.stringify(entries));
      const result = await runCli([entriesPath, orientationPath], { ...env, MINOS_HEAD_SHA: head });
      assert.equal(result.status, 0, result.stderr);
      return JSON.parse(result.stdout);
    } };
  }

  await t.test("new entries create separate issues once across heads", async (t) => {
    const f = await fixture(t);
    assert.deepEqual(await f.deliver([advisory, observation, misconfiguration, advisory]),
      { kind: "issue", outcome: "filed", written: 2, location: "owner/plans" });
    assert.deepEqual(f.posted.map((issue) => issue.title), [advisory.title, misconfiguration.title]);
    for (const [index, entry] of [advisory, misconfiguration].entries()) {
      assert.equal(f.posted[index].body, `${issueLogEntry(entry, `Filed by ${identity.name} from owner/repository#17, 2026-09-12`)} ${filingMarker(entry, reviewedRepository)}\n`);
    }
    assert.deepEqual(await f.deliver([advisory, misconfiguration], "later-head"),
      { kind: "issue", outcome: "filed", written: 0, location: "owner/plans" });
    assert.equal(f.posted.length, 2);
    assert.equal(existsSync(join(f.directory, "publication")), false);
  });
  for (const state of ["open", "closed"]) await t.test(`${state} marker on a later capped page suppresses creation`, async (t) => {
    const f = await fixture(t, { existing: [
      { number: 1, body: "unrelated", state: "open" }, { number: 2, body: null, state: "open" },
      { number: 3, body: filingMarker(advisory, reviewedRepository), state },
    ] });
    assert.deepEqual(await f.deliver([advisory]), { kind: "issue", outcome: "filed", written: 0, location: "owner/plans" });
    assert.equal(f.posted.length, 0);
    assert.ok(f.reads.some((read) => read.page === "2" && read.state === "all" && read.type === "issues"));
  });
  await t.test("a failed POST response is reconciled by marker read-back", async (t) => {
    const f = await fixture(t, { lostResponse: true });
    assert.deepEqual(await f.deliver([advisory]), { kind: "issue", outcome: "filed", written: 1, location: "owner/plans" });
    assert.equal(f.posted.length, 1);
  });
  await t.test("a partial batch records confirmed writes and stops at the failure", async (t) => {
    const f = await fixture(t, { refuseAfter: 1 });
    const outcome = await f.deliver([advisory, misconfiguration, { ...advisory, title: "Another defect" }]);
    assert.equal(outcome.outcome, "unfiled");
    assert.equal(outcome.written, 1);
    assert.match(outcome.reason, /issue creation returned HTTP 403/);
    assert.equal(f.posted.length, 2);
    assert.equal(f.issues.length, 1);
  });
  for (const [name, options, reason, writes] of [
    ["refused creation", { refusal: true }, /issue creation returned HTTP 403/, 1],
    ["successful POST missing from read-back", { hideWrites: true }, /issue write could not be discovered/, 1],
    ["wrong service identity", { wrongLogin: true }, /identity does not match/, 0],
    ["malformed marker list", { malformed: true }, /issue markers could not be read/, 0],
    ["stored content mismatch", { corruptWrite: true }, /read-back does not match/, 1],
    ["read-back author differs from authenticated account", { wrongReadBackAuthor: true }, /read-back does not match/, 1],
    ["repeated non-empty page", { repeatPages: true, existing: [{ body: "unrelated" }] }, /issue markers could not be read/, 0],
  ]) await t.test(name, async (t) => {
    const f = await fixture(t, options);
    const outcome = await f.deliver([advisory]);
    assert.equal(outcome.outcome, "unfiled");
    assert.match(outcome.reason, reason);
    assert.equal(f.posted.length, writes);
    assert.equal(existsSync(join(f.directory, "publication")), false);
  });
});
