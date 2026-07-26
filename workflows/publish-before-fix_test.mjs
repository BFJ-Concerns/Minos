import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { chmodSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { prepareFixWave } from "./fix-wave-plan.mjs";
import { publishBeforeFix } from "./publish-before-fix.mjs";

const workflowsDir = dirname(fileURLToPath(import.meta.url));
const repositoryRoot = resolve(workflowsDir, "..");
const fixScriptPath = join(workflowsDir, "fix.js");
const fixSource = readFileSync(fixScriptPath, "utf8");
const fixBody = fixSource.replace(/^export const meta =/m, "const meta =");
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
const fixScript = new AsyncFunction("agent", "parallel", "pipeline", "phase", "log", "args", fixBody);

const HEAD = "2222222222222222222222222222222222222222";
const TARGET = "1111111111111111111111111111111111111111";
const SENTINEL = "ORDERING-SENTINEL-publication-before-fix";

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
    clusterCap: 5,
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
  const state = {
    reviews: [],
    comments: new Map(),
    postCount: 0,
    commentReadCount: 0,
    actualHead: options.actualHead || HEAD,
    storePost: options.storePost !== false,
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
        state.comments.set(id, (payload.comments || []).map((comment, index) => ({
          id: index + 1,
          pull_request_review_id: id,
          path: comment.path,
          body: comment.body,
          position: comment.new_position,
          original_position: 0,
        })));
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
    position: comment.new_position,
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
