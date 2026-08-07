import assert from "node:assert/strict";
import { execFile, execFileSync, spawnSync } from "node:child_process";
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { promisify } from "node:util";

import { publishBeforeFix } from "./publish-before-fix.mjs";

const execFileAsync = promisify(execFile);
const workflowsDir = dirname(fileURLToPath(import.meta.url));
const repositoryRoot = resolve(workflowsDir, "..");
const HEAD_REF = "feature";
const TARGET_REF = "main";

function git(cwd, ...args) {
  return execFileSync("git", args, { cwd, encoding: "utf8" }).trim();
}

function writeAndCommit(cwd, path, contents, message) {
  writeFileSync(join(cwd, path), contents);
  git(cwd, "add", path);
  git(cwd, "commit", "-m", message);
  return git(cwd, "rev-parse", "HEAD");
}

function isAncestor(gitDir, ancestor, descendant) {
  return spawnSync(
    "git",
    ["--git-dir", gitDir, "merge-base", "--is-ancestor", ancestor, descendant],
    { stdio: "ignore" },
  ).status === 0;
}

function createDivergedRemote(root) {
  const remote = join(root, "remote.git");
  const seed = join(root, "seed");
  mkdirSync(seed);
  git(root, "init", "--bare", remote);
  git(seed, "init", "-b", TARGET_REF);
  git(seed, "config", "user.name", "Fixture");
  git(seed, "config", "user.email", "fixture@example.invalid");
  writeAndCommit(seed, "AGENTS.md", "FIXTURE-GUIDANCE\n", "base");
  git(seed, "remote", "add", "origin", remote);
  git(seed, "push", "-u", "origin", TARGET_REF);

  git(seed, "checkout", "-b", HEAD_REF);
  const head = writeAndCommit(seed, "feature.txt", "feature\n", "feature");
  git(seed, "push", "-u", "origin", HEAD_REF);

  git(seed, "checkout", TARGET_REF);
  const target = writeAndCommit(seed, "target.txt", "target\n", "target");
  git(seed, "push", "origin", TARGET_REF);

  const hook = join(remote, "hooks", "pre-receive");
  writeFileSync(hook, `#!/usr/bin/env sh
set -eu
counter="${remote}/push-count"
count=0
if [ -f "$counter" ]; then count="$(cat "$counter")"; fi
printf '%s\n' "$((count + 1))" >"$counter"
cat >/dev/null
`, { mode: 0o755 });
  return { remote, head, target };
}

async function setupForge(remote, head, target) {
  const server = createServer((request, response) => {
    response.setHeader("Content-Type", "application/json");
    if (request.headers.authorization !== "token fixture-token") {
      response.statusCode = 401;
      response.end('{"message":"unauthorised"}');
      return;
    }
    if (request.url === "/api/v1/repos/owner/subject/pulls/17") {
      response.end(JSON.stringify({
        head: {
          sha: head,
          ref: HEAD_REF,
          repo: { clone_url: remote, full_name: "owner/subject" },
        },
        base: {
          sha: target,
          ref: TARGET_REF,
          repo: { clone_url: remote, full_name: "owner/subject" },
        },
      }));
      return;
    }
    if (request.url === "/api/v1/repos/owner/subject-Annexe") {
      response.statusCode = 404;
      response.end('{"message":"not found"}');
      return;
    }
    response.statusCode = 404;
    response.end('{"message":"unhandled"}');
  });
  await new Promise((resolveListen) => server.listen(0, "127.0.0.1", resolveListen));
  return {
    apiBase: `http://127.0.0.1:${server.address().port}`,
    close: () => new Promise((resolveClose) => server.close(resolveClose)),
  };
}

test("setup and fix integration operate on one published reconciled lineage", async (t) => {
  const root = mkdtempSync(join(tmpdir(), "minos-reconciliation-fix-wave-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const { remote, head, target } = createDivergedRemote(root);
  const forge = await setupForge(remote, head, target);
  const runDir = join(root, "run");
  const reading = join(runDir, "workspace");
  const orientation = join(runDir, "orientation.json");
  const token = join(root, "forge.token");
  mkdirSync(runDir);
  writeFileSync(token, "fixture-token\n", { mode: 0o600 });

  try {
    await execFileAsync(join(repositoryRoot, "scripts", "run-body", "setup-workspace"), [], {
      cwd: repositoryRoot,
      env: {
        ...process.env,
        MINOS_API_BASE: forge.apiBase,
        MINOS_CREDENTIAL_FILE: token,
        MINOS_OWNER: "owner",
        MINOS_REPO_NAME: "subject",
        MINOS_PR: "17",
        MINOS_HEAD_SHA: head,
        MINOS_TARGET_SHA: target,
        MINOS_BASE_REF: TARGET_REF,
        MINOS_HEAD_BRANCH: HEAD_REF,
        MINOS_RUN_DIR: runDir,
        MINOS_WORKSPACE: reading,
        MINOS_ORIENTATION: orientation,
        MINOS_GIT_AUTHOR_NAME: "Minos",
        MINOS_GIT_AUTHOR_EMAIL: "minos@example.invalid",
      },
    });
  } finally {
    await forge.close();
  }

  const statePath = join(runDir, "reconciliation.json");
  const initial = JSON.parse(readFileSync(statePath, "utf8"));
  assert.equal(initial.outcome, "reconciled");
  assert.equal(initial.target, target);
  assert.equal(initial.baseHead, head);
  assert.equal(initial.publish, true);
  assert.equal(readFileSync(join(remote, "push-count"), "utf8").trim(), "1");
  assert.deepEqual(
    git(reading, "show", "-s", "--format=%P", "HEAD").split(/\s+/),
    [head, target],
  );
  assert.equal(git(reading, "rev-parse", "HEAD"), initial.merge);
  assert.equal(isAncestor(remote, initial.merge, `refs/heads/${HEAD_REF}`), true);

  let dispatchCwd = null;
  const result = await publishBeforeFix({
    input: {
      review: {
        status: "complete",
        reviewed: { target, head },
        confirmedFindings: [{
          title: "Repair sentinel",
          severity: "High",
          confidence: 95,
          path: "feature.txt",
          line: 1,
          explanation: "ROUND-RECONCILIATION-SENTINEL-719",
        }],
      },
      threshold: "High",
      maximumRounds: null,
      runRecord: { round: 0, confirmedUnfixed: [] },
      decision: {
        kind: "minos-sweep-decision-v1",
        classification: "working",
        basis: "one new High finding warrants a wave",
      },
      workspace: reading,
      guidance: {
        grounding: "repository",
        path: join(reading, "AGENTS.md"),
        content: "FIXTURE-GUIDANCE",
      },
      fixerBrief: {
        path: "workflows/review-briefs/fixer.md",
        readPath: join(repositoryRoot, "workflows", "review-briefs", "fixer.md"),
        content: "FIXTURE-FIX-BRIEF",
      },
    },
    head,
    target,
    minosBin: "/unused/minos",
    launcher: "/unused/ensemble.mjs",
    workflowScript: join(repositoryRoot, "workflows", "fix.js"),
    env: { ...process.env, MINOS_RUN_DIR: runDir },
    runForge: async () => ({
      code: 0,
      signal: null,
      stdout: '{"outcome":"applied"}\n',
      stderr: "",
    }),
    runDispatch: async ({ cwd }) => {
      dispatchCwd = cwd;
      const worker = join(root, "fix-worker");
      git(cwd, "worktree", "add", "--detach", worker, "HEAD");
      git(worker, "config", "user.name", "Minos");
      git(worker, "config", "user.email", "minos@example.invalid");
      const commit = writeAndCommit(worker, "repair.txt", "repaired\n", "fix: repair sentinel");
      return {
        code: 0,
        signal: null,
        stdout: `${JSON.stringify({
          status: "complete",
          classification: "working",
          integration: {
            commits: [commit],
            pushCount: 1,
            author: { name: "Minos", email: "minos@example.invalid" },
          },
          rerunReview: true,
        })}\n`,
        stderr: "",
      };
    },
    diagnostics: () => {},
  });

  assert.equal(result.status, "complete");
  assert.equal(result.publication.outcome, "applied");
  assert.equal(dispatchCwd, reading);
  const commitsPath = join(runDir, "fix-commits.json");
  writeFileSync(commitsPath, JSON.stringify(result.integration.commits));
  const integrationOutput = execFileSync(
    join(repositoryRoot, "workflows", "integrate-wave"),
    [reading, commitsPath],
    {
      encoding: "utf8",
      env: {
        ...process.env,
        MINOS_RUN_DIR: runDir,
        MINOS_SETUP_WORKSPACE: join(repositoryRoot, "scripts", "run-body", "setup-workspace"),
        MINOS_BASE_REF: TARGET_REF,
        MINOS_HEAD_BRANCH: HEAD_REF,
        MINOS_GIT_AUTHOR_NAME: "Minos",
        MINOS_GIT_AUTHOR_EMAIL: "minos@example.invalid",
      },
    },
  );
  const integration = JSON.parse(integrationOutput.trim().split(/\r?\n/).at(-1));

  assert.equal(integration.outcome, "integrated");
  assert.equal(readFileSync(join(remote, "push-count"), "utf8").trim(), "2");
  const publishedHead = git(remote, "rev-parse", `refs/heads/${HEAD_REF}`);
  assert.equal(git(reading, "rev-parse", "HEAD"), publishedHead);
  assert.equal(git(remote, "show", `${publishedHead}:repair.txt`), "repaired");
  assert.equal(isAncestor(remote, initial.merge, publishedHead), true);

  const advanced = JSON.parse(readFileSync(statePath, "utf8"));
  assert.equal(advanced.merge, initial.merge);
  assert.equal(advanced.baseHead, head);
  assert.equal(advanced.target, target);
  assert.equal(git(reading, "show", "HEAD:repair.txt"), "repaired");
});
