import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { pathToFileURL, fileURLToPath } from "node:url";
import test from "node:test";

const workflowsDir = dirname(fileURLToPath(import.meta.url));
const wrapperPath = join(workflowsDir, "publish-before-fix");
const HEAD = "2222222222222222222222222222222222222222";
const TARGET = "1111111111111111111111111111111111111111";
const SENTINEL = "EXECUTABLE-PUBLICATION-SENTINEL-719";

const preloadSource = String.raw`
import childProcess from "node:child_process";
import { EventEmitter } from "node:events";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { syncBuiltinESMExports } from "node:module";
import { PassThrough } from "node:stream";

const recorderPath = process.env.MINOS_PUBLICATION_RECORDER;
const scenario = process.env.MINOS_PUBLICATION_SCENARIO;

function recordedCalls() {
  return existsSync(recorderPath) ? JSON.parse(readFileSync(recorderPath, "utf8")) : [];
}

childProcess.spawn = function recordedSpawn(command, args, options) {
  const calls = recordedCalls();
  const call = { command, args, cwd: options.cwd };
  const isForge = args[0] === "forge";
  if (isForge) {
    call.bodyPath = args[5];
    call.commentsPath = args[6];
    call.body = readFileSync(call.bodyPath, "utf8");
    call.comments = JSON.parse(readFileSync(call.commentsPath, "utf8"));
  } else {
    call.planPath = args[2].replace(/^@/, "");
    call.plan = JSON.parse(readFileSync(call.planPath, "utf8"));
  }
  calls.push(call);
  writeFileSync(recorderPath, JSON.stringify(calls));

  const child = new EventEmitter();
  child.stdout = new PassThrough();
  child.stderr = new PassThrough();
  setImmediate(() => {
    if (isForge) {
      child.stdout.end(JSON.stringify(
        scenario === "uncertain"
          ? { outcome: "uncertain", reason: "durable review missing" }
          : { outcome: "applied" },
      ) + "\n");
    } else {
      child.stdout.end(JSON.stringify({
        status: "complete",
        classification: "working",
        dispatches: [{ label: "fix-dispatch-1" }],
        integration: { commits: [], pushCount: 0 },
        rerunReview: true,
        dispatchedSentinel: "EXECUTABLE-PUBLICATION-SENTINEL-719",
      }) + "\n");
    }
    child.stderr.end();
    child.emit("close", 0, null);
  });
  return child;
};
syncBuiltinESMExports();
`;

function input() {
  return {
    review: {
      status: "complete",
      reviewed: { target: TARGET, head: HEAD },
      confirmedFindings: [{
        title: "unsafe transition",
        severity: "High",
        confidence: 97,
        path: "internal/state.go",
        line: 41,
        explanation: `${SENTINEL} crosses publication before dispatch`,
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
  };
}

function runWrapper(t, scenario) {
  const operationRoot = mkdtempSync(join(tmpdir(), "minos-publication-wrapper-"));
  t.after(() => rmSync(operationRoot, { recursive: true, force: true }));
  const publication = join(operationRoot, "publication");
  const inputPath = join(operationRoot, "fix-input.json");
  const preloadPath = join(operationRoot, "record-children.mjs");
  const recorderPath = join(operationRoot, "child-calls.json");
  mkdirSync(publication);
  writeFileSync(
    join(operationRoot, "reconciliation.json"),
    JSON.stringify({ publication }),
  );
  writeFileSync(inputPath, JSON.stringify(input()));
  writeFileSync(preloadPath, preloadSource);

  const processResult = spawnSync(wrapperPath, [inputPath, HEAD, TARGET], {
    cwd: workflowsDir,
    encoding: "utf8",
    env: {
      ...process.env,
      NODE_OPTIONS: `--import=${pathToFileURL(preloadPath).href}`,
      MINOS_BIN: "/recorded/minos",
      MINOS_ENSEMBLE_LAUNCHER: "/recorded/ensemble.mjs",
      MINOS_PUBLICATION_RECORDER: recorderPath,
      MINOS_PUBLICATION_SCENARIO: scenario,
      MINOS_RUN_DIR: operationRoot,
    },
  });

  assert.equal(processResult.status, 0, processResult.stderr);
  assert.equal(processResult.signal, null);
  assert.equal(processResult.stdout.trim().split(/\r?\n/).length, 1);
  return {
    result: JSON.parse(processResult.stdout),
    calls: JSON.parse(readFileSync(recorderPath, "utf8")),
    publication,
  };
}

test("the publication executable carries the finding through forge confirmation before dispatch", (t) => {
  const { result, calls, publication } = runWrapper(t, "applied");

  assert.equal(calls.length, 2);
  assert.equal(calls[0].command, "/recorded/minos");
  assert.deepEqual(calls[0].args.slice(0, 5), [
    "forge",
    "review",
    HEAD,
    TARGET,
    "comment",
  ]);
  assert.equal(calls[0].comments.length, 1);
  assert.match(calls[0].comments[0].body, new RegExp(SENTINEL));
  assert.equal(calls[0].comments[0].path, "internal/state.go");
  assert.equal(calls[0].comments[0].line, 41);

  assert.equal(calls[1].command, process.execPath);
  assert.deepEqual(calls[1].args.slice(0, 3), [
    "/recorded/ensemble.mjs",
    "--json-args",
    `@${calls[1].planPath}`,
  ]);
  assert.equal(calls[1].args[3], join(workflowsDir, "fix.js"));
  assert.equal(calls[1].cwd, "/run/workspace");
  assert.match(JSON.stringify(calls[1].plan), new RegExp(SENTINEL));
  assert.equal(
    [calls[0].bodyPath, calls[0].commentsPath, calls[1].planPath].every((path) => !existsSync(path)),
    true,
    "the executable removes its materialised publication and dispatch inputs after both consumers read them",
  );
  assert.equal(result.status, "complete");
  assert.equal(result.publication.outcome, "applied");
  assert.equal(result.dispatchedSentinel, SENTINEL);
});

test("the publication executable starts no dispatcher when durable publication is uncertain", (t) => {
  const { result, calls } = runWrapper(t, "uncertain");

  assert.equal(calls.length, 1);
  assert.equal(calls[0].command, "/recorded/minos");
  assert.equal(result.status, "incomplete");
  assert.equal(result.publication.outcome, "uncertain");
  assert.match(result.reason, /publication returned uncertain/);
  assert.deepEqual(result.dispatches, []);
});
