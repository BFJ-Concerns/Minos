import { test } from "node:test";
import assert from "node:assert/strict";
import { execFile, execFileSync } from "node:child_process";
import { chmodSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

import { prepareFixWave } from "./fix-wave-plan.mjs";
import { publishBeforeFix } from "./publish-before-fix.mjs";

const workflowsDir = dirname(fileURLToPath(import.meta.url));
const repositoryRoot = resolve(workflowsDir, "..");
const fixScriptPath = join(workflowsDir, "fix.js");
const fixSource = readFileSync(fixScriptPath, "utf8");
const fixBody = fixSource.replace(/^export const meta =/m, "const meta =");
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
const fixScript = new AsyncFunction("agent", "parallel", "pipeline", "phase", "log", "args", fixBody);
const execFileAsync = promisify(execFile);

const JOURNEY_REPOSITORY = createAnchoringRepository();
const HEAD = JOURNEY_REPOSITORY.head;
const TARGET = JOURNEY_REPOSITORY.target;
const SENTINEL = "ORDERING-SENTINEL-publication-before-fix";

function createAnchoringRepository() {
  const workspace = mkdtempSync(join(tmpdir(), "minos-publication-workspace-"));
  execFileSync("git", ["init", "-q"], { cwd: workspace });
  execFileSync("git", ["config", "user.name", "Minos Test"], { cwd: workspace });
  execFileSync("git", ["config", "user.email", "minos@example.invalid"], { cwd: workspace });

  const files = new Map([
    ["internal/state.go", Array.from({ length: 50 }, (_, index) => `state line ${index + 1}`)],
    ["src/large-file.rs", Array.from({ length: 7430 }, (_, index) => `large line ${index + 1}`)],
  ]);
  for (const [path, lines] of files) {
    const fullPath = join(workspace, path);
    execFileSync("mkdir", ["-p", dirname(fullPath)]);
    writeFileSync(fullPath, `${lines.join("\n")}\n`);
  }
  execFileSync("git", ["add", "."], { cwd: workspace });
  execFileSync("git", ["commit", "-q", "-m", "target"], { cwd: workspace });
  const target = execFileSync("git", ["rev-parse", "HEAD"], { cwd: workspace, encoding: "utf8" }).trim();

  files.get("internal/state.go")[40] = "changed state line 41";
  files.get("src/large-file.rs")[7399] = "changed large line 7400";
  for (const [path, lines] of files) {
    writeFileSync(join(workspace, path), `${lines.join("\n")}\n`);
  }
  execFileSync("git", ["add", "."], { cwd: workspace });
  execFileSync("git", ["commit", "-q", "-m", "head"], { cwd: workspace });
  const head = execFileSync("git", ["rev-parse", "HEAD"], { cwd: workspace, encoding: "utf8" }).trim();
  const diff = execFileSync("git", [
    "diff", "--no-ext-diff", "--no-color", "--unified=3", "-M", target, head,
  ], { cwd: workspace, encoding: "utf8" });
  return { workspace, target, head, diffNewSide: parseNewSideIntervals(diff) };
}

function parseNewSideIntervals(diff) {
  const intervals = new Map();
  let path = "";
  for (const line of diff.split("\n")) {
    if (line.startsWith("+++ ")) {
      const label = line.slice(4);
      path = label === "/dev/null" ? "" : label.replace(/^b\//, "");
      continue;
    }
    const match = line.match(/^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@/);
    if (!path || !match) continue;
    const start = Number(match[1]);
    const count = match[2] === undefined ? 1 : Number(match[2]);
    if (count > 0) {
      const current = intervals.get(path) || [];
      current.push([start, start + count - 1]);
      intervals.set(path, current);
    }
  }
  return intervals;
}

function isAnchored(intervals, path, line) {
  return (intervals.get(path) || []).some(([start, end]) => line >= start && line <= end);
}

function finding(overrides = {}) {
  return {
    title: "unsafe transition",
    severity: "High",
    confidence: 97,
    path: "internal/state.go",
    line: 41,
    explanation: `${SENTINEL} proves both final consumers received this finding`,
    ...overrides,
  };
}

function input(findings = [finding()], overrides = {}) {
  return {
    review: {
      status: "complete",
      reviewed: { target: TARGET, head: HEAD },
      confirmedFindings: findings,
    },
    threshold: "High",
    maximumRounds: null,
    runRecord: { round: 0, confirmedUnfixed: [] },
    workspace: "/run/workspace",
    guidance: {
      grounding: "annexe",
      path: "/run/subject-Annexe/README.md",
      content: "COMMISSION_VIOLET_719",
    },
    fixerBrief: {
      path: "workflows/review-briefs/fixer.md",
      readPath: "/opt/minos/workflows/review-briefs/fixer.md",
      content: "MINOS_FIX_EVIDENCE_V1",
    },
    ...overrides,
  };
}

function assignedFindings(prompt) {
  const match = prompt.match(/Confirmed findings: (\[[^\n]+\])/);
  assert.ok(match, "fix prompt carries finding data");
  return JSON.parse(match[1]);
}

function dispatchRecorder(events, prompts) {
  return async ({ plan }) => {
    const calls = [];
    const agent = async (prompt, options) => {
      const assigned = assignedFindings(prompt);
      events.push(`fix-agent-called:${assigned[0].key}`);
      prompts.push(prompt);
      calls.push(options.label);
      return {
        commit: `${options.label}-commit`,
        fixes: assigned.map((item) => ({
          findingKey: item.key,
          status: "fixed",
          writeUp: `Repaired ${item.title}.`,
        })),
      };
    };
    const parallel = async (thunks) => Promise.all(thunks.map((thunk) => thunk().catch(() => null)));
    const result = await fixScript(agent, parallel, async () => [], () => {}, () => {}, plan);
    assert.ok(calls.length > 0, "positive path reaches the injected agent boundary");
    return { code: 0, signal: null, stdout: `${JSON.stringify(result)}\n`, stderr: "" };
  };
}

async function requestBody(request) {
  let body = "";
  for await (const chunk of request) body += chunk;
  return body;
}

async function forgeFixture(options = {}) {
  const storedPositions = new Map(options.storedPositions || []);
  const state = {
    reviews: [],
    comments: new Map(),
    postCount: 0,
    commentReadCount: 0,
    actualHead: options.actualHead || HEAD,
    storePost: options.storePost !== false,
    diffNewSide: options.diffNewSide || JOURNEY_REPOSITORY.diffNewSide,
  };
  const server = createServer(async (request, response) => {
    response.setHeader("Content-Type", "application/json");
    const url = new URL(request.url, "http://fixture.invalid");
    const pullPath = "/api/v1/repos/minos-e2e-owner/subject/pulls/1";
    if (request.method === "GET" && url.pathname === "/api/v1/user") {
      response.end(JSON.stringify({ login: "Minos" }));
      return;
    }
    if (request.method === "GET" && url.pathname === pullPath) {
      response.end(JSON.stringify({
        number: 1,
        state: "open",
        merged: false,
        head: { sha: state.actualHead },
        base: {
          ref: "main",
          repo: { full_name: "minos-e2e-owner/subject" },
        },
      }));
      return;
    }
    if (request.method === "GET" && url.pathname === "/api/v1/repos/minos-e2e-owner/subject/branches/main") {
      response.end(JSON.stringify({ commit: { id: TARGET } }));
      return;
    }
    if (request.method === "GET" && url.pathname === `${pullPath}/reviews`) {
      response.end(JSON.stringify(state.reviews));
      return;
    }
    if (request.method === "POST" && url.pathname === `${pullPath}/reviews`) {
      state.postCount += 1;
      const payload = JSON.parse(await requestBody(request));
      const id = state.reviews.length + 1;
      const review = {
        id,
        state: payload.event,
        commit_id: payload.commit_id,
        body: payload.body,
        user: { login: "Minos" },
      };
      if (state.storePost) {
        state.reviews.push(review);
        state.comments.set(id, (payload.comments || []).map((comment, index) => {
          const anchored = isAnchored(state.diffNewSide, comment.path, comment.new_position);
          return {
            id: index + 1,
            pull_request_review_id: id,
            path: comment.path,
            body: comment.body,
            position: anchored
              ? (storedPositions.get(`${comment.path}:${comment.new_position}`) ?? comment.new_position)
              : 3691,
            original_position: 0,
            diff_hunk: anchored ? `@@ -${comment.new_position},3 +${comment.new_position},3 @@` : "",
          };
        }));
      }
      response.end(JSON.stringify(review));
      return;
    }
    const commentsMatch = url.pathname.match(new RegExp(`^${pullPath}/reviews/(\\d+)/comments$`));
    if (request.method === "GET" && commentsMatch) {
      state.commentReadCount += 1;
      response.end(JSON.stringify(state.comments.get(Number(commentsMatch[1])) || []));
      return;
    }
    response.statusCode = 404;
    response.end(JSON.stringify({ message: `unhandled ${request.method} ${url.pathname}` }));
  });
  await new Promise((resolveListen) => server.listen(0, "127.0.0.1", resolveListen));

  const root = mkdtempSync(join(tmpdir(), "minos-publication-forge-"));
  const tokenPath = join(root, "forge.token");
  writeFileSync(tokenPath, "fixture-token\n", { mode: 0o600 });
  const address = server.address();
  const apiBase = `http://127.0.0.1:${address.port}`;
  writeFileSync(join(root, "service.toml"), `
[service]
bot-login = "Minos"

[listener]
bind = ":0"

[runs]
dir = "${root}"

[forges.forgejo]
adaptation = "${join(repositoryRoot, "scripts", "adaptations", "forgejo")}"
api-base = "${apiBase}"
webhook-secret-file = "${tokenPath}"
credential-file = "${tokenPath}"
`);
  return {
    state,
    env: {
      ...process.env,
      MINOS_CONFIG: root,
      MINOS_FORGE: "forgejo",
      MINOS_OWNER: "minos-e2e-owner",
      MINOS_REPO_NAME: "subject",
      MINOS_PR: "1",
      MINOS_WORKSPACE: JOURNEY_REPOSITORY.workspace,
    },
    close: () => new Promise((resolveClose) => server.close(resolveClose)),
  };
}

function seedExactReview(fixture, plan, comments = plan.sweepReview.comments) {
  fixture.state.reviews = [{
    id: 41,
    state: "COMMENT",
    commit_id: HEAD,
    body: `${plan.sweepReview.body}\n\n<!-- Minos: head=${HEAD} target=${TARGET} -->`,
    user: { login: "Minos" },
  }];
  fixture.state.comments.set(41, comments.map((comment, index) => ({
    id: index + 1,
    pull_request_review_id: 41,
    path: comment.path,
    body: comment.body,
    diff_hunk: `@@ -${comment.line},3 +${comment.line},3 @@`,
    position: comment.line,
    original_position: 0,
  })));
}

async function runCase({ minosBin, fixture, preparedInput = input(), events = [], prompts = [] }) {
  const result = await publishBeforeFix({
    input: preparedInput,
    head: HEAD,
    target: TARGET,
    minosBin,
    launcher: "/unused/ensemble.mjs",
    workflowScript: fixScriptPath,
    env: fixture.env,
    runDispatch: dispatchRecorder(events, prompts),
    onEvent: (event) => events.push(event),
    diagnostics: () => {},
  });
  return { result, events, prompts };
}

test("publication-before-fix owns the real publication barrier and fix dispatch", async (t) => {
  const buildRoot = mkdtempSync(join(tmpdir(), "minos-publication-binary-"));
  const minosBin = join(buildRoot, "minos");
  execFileSync("go", ["build", "-o", minosBin, "./cmd/minos"], { cwd: repositoryRoot });

  await t.test("new exact review is read back before dispatcher start and agent call", async () => {
    const fixture = await forgeFixture();
    try {
      const { result, events, prompts } = await runCase({ minosBin, fixture });
      assert.equal(result.status, "complete");
      assert.equal(result.publication.outcome, "applied");
      assert.equal(fixture.state.postCount, 1);
      assert.ok(fixture.state.commentReadCount > 0, "guarded adaptation read comments back");
      assert.match(fixture.state.comments.get(1)[0].body, new RegExp(SENTINEL));
      assert.match(prompts[0], new RegExp(SENTINEL));
      assert.match(events[0], /^fix-plan-created:/);
      assert.match(events[1], /^forge-review-confirmed:/);
      assert.match(events[2], /^fix-dispatch-started:/);
      assert.match(events[3], /^fix-agent-called:/);
      assert.equal(events.filter((event) => event.startsWith("forge-review-confirmed:")).length, 1);
      assert.equal(events.filter((event) => event.startsWith("fix-dispatch-started:")).length, 1);
      assert.equal(events.filter((event) => event.startsWith("fix-agent-called:")).length, 1);
      assert.equal(events[0].split(":")[1], events[1].split(":")[1]);
      assert.equal(events[1].split(":")[1], events[2].split(":")[1]);
    } finally {
      await fixture.close();
    }
  });

  await t.test("an outside-hunk confirmed finding reaches a durable fallback before dispatch", async () => {
    const sourceLine = 7423;
    const preparedInput = input([finding({
      path: "src/large-file.rs",
      line: sourceLine,
    })]);
    const fixture = await forgeFixture();
    try {
      const { result, events, prompts } = await runCase({ minosBin, fixture, preparedInput });
      const review = fixture.state.reviews[0];
      const comments = fixture.state.comments.get(review.id) || [];
      const findingIsAnchored = comments.some((comment) =>
        comment.path === "src/large-file.rs" &&
        typeof comment.diff_hunk === "string" &&
        comment.diff_hunk !== "" &&
        comment.body.includes(SENTINEL));
      const findingIsInReviewBody =
        review.body.includes(SENTINEL) &&
        review.body.includes("src/large-file.rs") &&
        review.body.includes(String(sourceLine));

      assert.equal(
        findingIsAnchored || findingIsInReviewBody,
        true,
        "the author-visible review must retain the finding either at a real inline anchor or in the review body with its source location",
      );
      assert.equal(result.status, "complete");
      assert.equal(result.publication.outcome, "applied");
      assert.match(prompts[0], new RegExp(SENTINEL));
      assert.deepEqual(
        events.slice(1).map((event) => event.split(":")[0]),
        ["forge-review-confirmed", "fix-dispatch-started", "fix-agent-called"],
      );
    } finally {
      await fixture.close();
    }
  });

  await t.test("a blame-rewritten real inline anchor remains confirmed before dispatch", async () => {
    const submittedFileLine = 41;
    const preparedInput = input([finding({
      path: "internal/state.go",
      line: submittedFileLine,
    })]);
    const fixture = await forgeFixture({
      storedPositions: [[`internal/state.go:${submittedFileLine}`, 3691]],
    });
    try {
      const { result, events, prompts } = await runCase({ minosBin, fixture, preparedInput });
      const review = fixture.state.reviews[0];
      const comments = fixture.state.comments.get(review.id) || [];
      const findingReachedAnchoredComment = comments.some((comment) =>
        comment.path === "internal/state.go" &&
        typeof comment.diff_hunk === "string" &&
        comment.diff_hunk !== "" &&
        comment.body.includes(SENTINEL));

      assert.equal(
        findingReachedAnchoredComment,
        true,
        "Forgejo's durable comment visibly carries the finding regardless of its stored blame coordinate",
      );
      assert.equal(result.status, "complete");
      assert.equal(result.publication.outcome, "applied");
      assert.match(prompts[0], new RegExp(SENTINEL));
      assert.deepEqual(
        events.slice(1).map((event) => event.split(":")[0]),
        ["forge-review-confirmed", "fix-dispatch-started", "fix-agent-called"],
      );
    } finally {
      await fixture.close();
    }
  });

  await t.test("a single-wave fixer write-up reaches the durable review surface", async () => {
    const writeUp = "BRIEF-FIX-WRITE-UP-SENTINEL-719";
    const preparedInput = input([finding({
      path: "src/brief-scope.rs",
      line: 73,
    })], { singleWave: true });
    const plan = prepareFixWave(preparedInput);
    const agent = async (prompt, options) => ({
      commit: `${options.label}-commit`,
      fixes: assignedFindings(prompt).map((item) => ({
        findingKey: item.key,
        status: "fixed",
        writeUp,
      })),
    });
    const parallel = async (thunks) => Promise.all(thunks.map((thunk) => thunk()));
    const fixResult = await fixScript(agent, parallel, async () => [], () => {}, () => {}, plan);
    assert.ok(
      fixResult.fixReview,
      "the single-wave result must expose the fixer write-up to the lifecycle publication consumer",
    );

    const materialised = mkdtempSync(join(tmpdir(), "minos-brief-fix-review-"));
    const bodyPath = join(materialised, "body.md");
    const commentsPath = join(materialised, "comments.json");
    writeFileSync(bodyPath, fixResult.fixReview.body, { mode: 0o600 });
    writeFileSync(commentsPath, JSON.stringify(fixResult.fixReview.comments), { mode: 0o600 });

    const fixture = await forgeFixture();
    try {
      const { stdout } = await execFileAsync(
        minosBin,
        ["forge", "review", HEAD, TARGET, "comment", bodyPath, commentsPath],
        { encoding: "utf8", env: fixture.env },
      );
      const publication = JSON.parse(stdout);
      const durableReview = fixture.state.reviews[0];
      const durableComments = fixture.state.comments.get(1) || [];
      assert.equal(publication.outcome, "applied");
      assert.equal(fixture.state.postCount, 1);
      assert.ok(fixture.state.commentReadCount > 0);
      assert.deepEqual(durableComments, []);
      assert.match(durableReview.body, new RegExp(writeUp));
      assert.match(durableReview.body, /src\/brief-scope\.rs/);
      assert.match(durableReview.body, /\b73\b/);
    } finally {
      await fixture.close();
    }
  });

  await t.test("an exact pre-existing review crosses the barrier without another POST", async () => {
    const preparedInput = input();
    const plan = prepareFixWave(preparedInput);
    const fixture = await forgeFixture();
    seedExactReview(fixture, plan);
    try {
      const { result, events } = await runCase({ minosBin, fixture, preparedInput });
      assert.equal(result.publication.outcome, "applied");
      assert.equal(result.publication.reason, "review already present");
      assert.equal(fixture.state.postCount, 0);
      assert.ok(fixture.state.commentReadCount > 0);
      assert.ok(events.some((event) => event.startsWith("fix-agent-called:")));
    } finally {
      await fixture.close();
    }
  });

  await t.test("an accepted POST omitted from read-back starts no dispatcher or agent", async () => {
    const fixture = await forgeFixture({ storePost: false });
    try {
      const { result, events, prompts } = await runCase({ minosBin, fixture });
      assert.equal(result.status, "incomplete");
      assert.equal(result.publication.outcome, "uncertain");
      assert.equal(fixture.state.postCount, 1);
      assert.equal(events.filter((event) => event.startsWith("fix-dispatch-started:")).length, 0);
      assert.equal(prompts.length, 0);
    } finally {
      await fixture.close();
    }
  });

  await t.test("a matching review with different inline comments starts no dispatcher or agent", async () => {
    const preparedInput = input();
    const plan = prepareFixWave(preparedInput);
    const fixture = await forgeFixture();
    seedExactReview(fixture, plan, [{
      ...plan.sweepReview.comments[0],
      body: "different inline comment",
    }]);
    try {
      const { result, events, prompts } = await runCase({ minosBin, fixture, preparedInput });
      assert.equal(result.status, "incomplete");
      assert.equal(result.publication.outcome, "uncertain");
      assert.equal(result.publication.reason, "matching review has different inline comments");
      assert.equal(fixture.state.postCount, 0);
      assert.equal(events.filter((event) => event.startsWith("fix-dispatch-started:")).length, 0);
      assert.equal(prompts.length, 0);
    } finally {
      await fixture.close();
    }
  });

  await t.test("a moved head rejection starts no dispatcher or agent", async () => {
    const fixture = await forgeFixture({ actualHead: "3333333333333333333333333333333333333333" });
    try {
      const { result, events, prompts } = await runCase({ minosBin, fixture });
      assert.equal(result.status, "incomplete");
      assert.equal(result.publication.outcome, "rejected");
      assert.equal(result.publication.reason, "head moved");
      assert.equal(fixture.state.postCount, 0);
      assert.equal(events.filter((event) => event.startsWith("fix-dispatch-started:")).length, 0);
      assert.equal(prompts.length, 0);
    } finally {
      await fixture.close();
    }
  });

  await t.test("malformed forge output starts no dispatcher or agent", async () => {
    const malformedBin = join(buildRoot, "malformed-forge");
    writeFileSync(malformedBin, "#!/bin/sh\nprintf 'not-json\\n'\n", { mode: 0o755 });
    chmodSync(malformedBin, 0o755);
    const fixture = await forgeFixture();
    try {
      const { result, events, prompts } = await runCase({ minosBin: malformedBin, fixture });
      assert.equal(result.status, "incomplete");
      assert.match(result.reason, /Unexpected token|JSON/);
      assert.equal(events.filter((event) => event.startsWith("fix-dispatch-started:")).length, 0);
      assert.equal(prompts.length, 0);
    } finally {
      await fixture.close();
    }
  });

  await t.test("fix dispatch runs from the recorded publication worktree", async () => {
    const runDir = mkdtempSync(join(tmpdir(), "minos-publication-cwd-"));
    const publication = join(runDir, "publication");
    writeFileSync(
      join(runDir, "reconciliation.json"),
      JSON.stringify({ publication }),
      { mode: 0o600 },
    );
    let dispatchedFrom = null;
    const result = await publishBeforeFix({
      input: input(),
      head: HEAD,
      target: TARGET,
      minosBin,
      launcher: "/unused/ensemble.mjs",
      workflowScript: fixScriptPath,
      cwd: "/run/workspace",
      env: { ...process.env, MINOS_RUN_DIR: runDir },
      runForge: async () => ({
        code: 0,
        signal: null,
        stdout: '{"outcome":"applied"}\n',
        stderr: "",
      }),
      runDispatch: async ({ cwd }) => {
        dispatchedFrom = cwd;
        return {
          code: 0,
          signal: null,
          stdout: `${JSON.stringify({
            status: "complete",
            classification: "working",
            integration: { commits: [], pushCount: 0 },
          })}\n`,
          stderr: "",
        };
      },
      diagnostics: () => {},
    });
    assert.equal(result.status, "complete");
    assert.equal(dispatchedFrom, publication);
  });

  await t.test("a missing reconciliation record after publication returns a structured failure", async () => {
    const runDir = mkdtempSync(join(tmpdir(), "minos-publication-missing-cwd-"));
    const events = [];
    let dispatchCalls = 0;
    const result = await publishBeforeFix({
      input: input(),
      head: HEAD,
      target: TARGET,
      minosBin,
      launcher: "/unused/ensemble.mjs",
      workflowScript: fixScriptPath,
      env: { ...process.env, MINOS_RUN_DIR: runDir },
      runForge: async () => ({
        code: 0,
        signal: null,
        stdout: '{"outcome":"applied"}\n',
        stderr: "",
      }),
      runDispatch: async () => {
        dispatchCalls += 1;
        throw new Error("must not dispatch");
      },
      onEvent: (event) => events.push(event),
      diagnostics: () => {},
    });
    assert.equal(result.status, "incomplete");
    assert.equal(result.publication.outcome, "applied");
    assert.match(result.reason, /fix dispatch workspace resolution failed/);
    assert.match(result.reason, /reconciliation\.json/);
    assert.equal(dispatchCalls, 0);
    assert.equal(events.filter((event) => event.startsWith("fix-dispatch-started:")).length, 0);
  });

  await t.test("terminal preparation refuses publication and dispatch", async () => {
    const events = [];
    let forgeCalls = 0;
    let dispatchCalls = 0;
    const result = await publishBeforeFix({
      input: input([finding({ severity: "Low" })]),
      head: HEAD,
      target: TARGET,
      minosBin,
      launcher: "/unused/ensemble.mjs",
      workflowScript: fixScriptPath,
      runForge: async () => { forgeCalls += 1; throw new Error("must not publish"); },
      runDispatch: async () => { dispatchCalls += 1; throw new Error("must not dispatch"); },
      onEvent: (event) => events.push(event),
      diagnostics: () => {},
    });
    assert.equal(result.classification, "terminal");
    assert.equal(result.sweepReview, null);
    assert.equal(forgeCalls, 0);
    assert.equal(dispatchCalls, 0);
    assert.equal(events.filter((event) => event.startsWith("fix-dispatch-started:")).length, 0);
  });
});
