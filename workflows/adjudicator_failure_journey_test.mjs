import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import {
  chmodSync,
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readlinkSync,
  readFileSync,
  realpathSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { basename, dirname, join } from "node:path";
import { pathToFileURL, fileURLToPath } from "node:url";
import test from "node:test";

const workflowsDir = dirname(fileURLToPath(import.meta.url));
const wrapperPath = join(workflowsDir, "adjudicated-review");

const preloadSource = String.raw`
import childProcess from "node:child_process";
import { EventEmitter } from "node:events";
import fs, { chmodSync, mkdirSync, readdirSync, symlinkSync, writeFileSync } from "node:fs";
import { syncBuiltinESMExports } from "node:module";
import { basename, join } from "node:path";
import { PassThrough } from "node:stream";

const scenario = process.env.MINOS_ADJUDICATOR_SCENARIO;
const recorderPath = process.env.MINOS_ADJUDICATOR_RECORDER;
let activeRecordDir = null;
let retentionShapeInjected = false;
const originalMkdirSync = fs.mkdirSync;

fs.mkdirSync = function retentionShapeMkdir(path, options) {
  const result = originalMkdirSync(path, options);
  const evidenceDir = process.env.MINOS_RUN_DIR && join(process.env.MINOS_RUN_DIR, "ensemble-records");
  if (!retentionShapeInjected && activeRecordDir && path === evidenceDir) {
    retentionShapeInjected = true;
    if (scenario === "retention-mode-zero") {
      const unreadable = join(activeRecordDir, "mode-zero");
      originalMkdirSync(unreadable);
      writeFileSync(join(unreadable, "evidence.txt"), "unreadable evidence\n");
      chmodSync(unreadable, 0o000);
      let modeZeroUnreadable = false;
      try {
        readdirSync(unreadable);
      } catch {
        modeZeroUnreadable = true;
      }
      writeFileSync(process.env.MINOS_RETENTION_SHAPE_RECORDER, JSON.stringify({ modeZeroUnreadable }));
    } else if (scenario === "retention-symlink-loop") {
      symlinkSync(".", join(activeRecordDir, "symlink-loop"));
    } else if (scenario === "retention-partial") {
      const collision = join(
        evidenceDir,
        basename(activeRecordDir),
        "runs",
        "cwd",
        "namespace",
        "run",
        "manifest.json",
      );
      originalMkdirSync(collision, { recursive: true });
    }
  }
  return result;
};

function envelope() {
  const result = {
    reviewed: { target: "target-sha", head: "head-sha", occasion: null },
    stage: "present",
    requiredModelEvidence: [
      { label: "specialist", role: "specialist", pinnedModel: "gpt-6-sol" },
      { label: "verifier", role: "verifier", pinnedModel: "claude-opus-5-5" },
    ],
    proposedFindings: [{
      id: "specialist:1",
      source: "Correctness",
      title: "Publishable sentinel",
      severity: "High",
      confidence: 90,
      path: "internal/review.go",
      line: 42,
      explanation: "This finding must disappear when adjudication is incomplete.",
      proposingLabel: "specialist",
      verifyLabel: "verifier",
      rawVerifier: scenario === "missing-verdict"
        ? null
        : { verdict: "upheld", confidence: 92, reason: "The defect is reachable." },
    }],
    briefs: [],
    misconfigurations: [],
    dispatches: [],
    reviewers: [],
  };
  if (scenario === "brief-runs") {
    result.briefs = [
      {
        brief: ".review/missing/scoped.md",
        title: "Scoped",
        status: "run",
        reason: "applicable concern reviewed",
      },
    ];
    result.misconfigurations = [{
      brief: ".review/missing/scoped.md",
      title: "Scoped",
      kind: "misconfigured-scope",
      reason: "brief scope missing/ matches no repository directory",
    }];
  } else if (scenario === "brief-skips") {
    result.briefs = [{
      brief: ".review/missing/scoped.md",
      title: "Scoped",
      status: "skipped",
      skipKind: "misconfigured-scope",
      reason: "brief scope missing/ matches no repository directory",
    }];
    result.misconfigurations = [{
      brief: ".review/missing/scoped.md",
      title: "Scoped",
      kind: "misconfigured-scope",
      reason: "brief scope missing/ matches no repository directory",
    }];
  } else if (scenario === "partition-detail") {
    result.briefs = [{
      brief: ".review/partitioned.md",
      title: "Partitioned",
      status: "run",
      reason: "applicable concern reviewed",
      inapplicableUnits: [
        {
          label: "repository-review-partitioned-md-2-claude",
          concern: "Inapplicable alpha",
          reason: "WRAPPER-PARTITION-ALPHA-0728",
        },
        {
          label: "repository-review-partitioned-md-3-claude",
          concern: "Inapplicable beta",
          reason: "WRAPPER-PARTITION-BETA-0728",
        },
      ],
    }];
  } else if (scenario === "observation") {
    result.outOfScopeObservations = [{
      id: "specialist:observation:1",
      source: "Correctness",
      title: "Unchanged adjacent defect",
      path: "internal/adjacent.go",
      line: 73,
      explanation: "OBSERVATION-SENTINEL-719 remains unverified.",
      observingLabel: "specialist",
      verified: false,
      severity: "Critical",
      confidence: 100,
      verdict: "confirmed",
    }];
  }
  return result;
}

function writeArchive(recordDir) {
  const runDir = join(recordDir, "runs", "cwd", "namespace", "run");
  mkdirSync(join(runDir, "agents", "000001"), { recursive: true });
  mkdirSync(join(runDir, "agents", "000002"), { recursive: true });
  writeFileSync(
    join(runDir, "manifest.json"),
    scenario === "corrupt-manifest"
      ? "{not-json"
      : JSON.stringify({ kind: "run_manifest", status: "complete" }),
  );
  writeFileSync(
    join(runDir, "agents", "000001", "agent.json"),
    JSON.stringify({
      label: "specialist",
      status: scenario === "failed-leg" ? "failed" : "complete",
      resolved_model: "gpt-6-sol",
    }),
  );
  writeFileSync(
    join(runDir, "agents", "000002", "agent.json"),
    JSON.stringify({ label: "verifier", status: "complete", resolved_model: "claude-opus-5-5" }),
  );
  writeFileSync(
    join(runDir, "agents", "000002", "attempt-001-claude.jsonl"),
    "RETAINED-VERIFIER-TRANSCRIPT-0731\n",
  );
}

childProcess.spawn = function recordedSpawn(command, args, options) {
  const recordDir = options.env.ENSEMBLE_RUN_RECORD_DIR;
  activeRecordDir = recordDir;
  writeFileSync(recorderPath, JSON.stringify({
    command,
    args,
    cwd: options.cwd,
    record: options.env.ENSEMBLE_RUN_RECORD,
    recordDir,
  }));
  writeArchive(recordDir);

  const child = new EventEmitter();
  child.stdout = new PassThrough();
  child.stderr = new PassThrough();
  setImmediate(() => {
    child.stdout.end(
      scenario === "malformed-envelope"
        ? "{not-json\n"
        : JSON.stringify(envelope()) + "\n",
    );
    child.stderr.end();
    child.emit("close", 0, null);
  });
  return child;
};
syncBuiltinESMExports();
`;

function runWrapper(t, scenario, { retention = "unset" } = {}) {
  const operationRoot = mkdtempSync(join(tmpdir(), "minos-adjudicator-wrapper-"));
  t.after(() => rmSync(operationRoot, { recursive: true, force: true }));
  const effectiveTmpDir = join(operationRoot, "tmp");
  mkdirSync(effectiveTmpDir);
  const preloadPath = join(operationRoot, "record-ensemble.mjs");
  const recorderPath = join(operationRoot, "ensemble-call.json");
  const retentionShapePath = join(operationRoot, "retention-shape.json");
  const argsPath = join(operationRoot, "review-args.json");
  const workflowPath = join(operationRoot, "review.js");
  writeFileSync(preloadPath, preloadSource);
  writeFileSync(argsPath, "{}");
  writeFileSync(workflowPath, "export default {};\n");
  let minosRunDir;
  if (retention === "success") {
    minosRunDir = join(operationRoot, "durable-run");
    mkdirSync(minosRunDir);
  } else if (retention === "failure") {
    minosRunDir = join(operationRoot, "not-a-directory");
    writeFileSync(minosRunDir, "blocks retained evidence\n");
  }

  const result = spawnSync(
    wrapperPath,
    [workflowPath, "--json-args", `@${argsPath}`],
    {
      cwd: workflowsDir,
      encoding: "utf8",
      env: {
        ...process.env,
        TMPDIR: effectiveTmpDir,
        NODE_OPTIONS: `--import=${pathToFileURL(preloadPath).href}`,
        MINOS_ADJUDICATOR_SCENARIO: scenario,
        MINOS_ADJUDICATOR_RECORDER: recorderPath,
        MINOS_RETENTION_SHAPE_RECORDER: retentionShapePath,
        ...(minosRunDir ? { MINOS_RUN_DIR: minosRunDir } : {}),
      },
    },
  );

  const launch = JSON.parse(readFileSync(recorderPath, "utf8"));
  t.after(() => {
    if (!existsSync(launch.recordDir)) return;
    try { chmodSync(join(launch.recordDir, "mode-zero"), 0o700); } catch {}
    rmSync(launch.recordDir, { recursive: true, force: true });
  });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.signal, null);
  const verdict = JSON.parse(result.stdout);
  assert.equal(
    existsSync(launch.recordDir),
    false,
    "the executable wrapper removes the exact run-record directory supplied to Ensemble",
  );
  const retentionShape = existsSync(retentionShapePath)
    ? JSON.parse(readFileSync(retentionShapePath, "utf8"))
    : null;
  return {
    verdict,
    launch,
    result,
    argsPath,
    workflowPath,
    effectiveTmpDir,
    minosRunDir,
    retentionShape,
  };
}

function assertWithheld(verdict) {
  assert.equal(verdict.status, "incomplete");
  assert.equal(verdict.complete, false);
  assert.deepEqual(verdict.confirmedFindings, []);
}

function machineReadableBriefStatus(verdict, brief) {
  const disposition = Array.isArray(verdict.briefs)
    ? verdict.briefs.find((entry) => entry && entry.brief === brief)
    : null;
  if (disposition && ["run", "skipped"].includes(disposition.status)) return disposition.status;
  if (Array.isArray(verdict.ran) && verdict.ran.some((entry) =>
    entry === brief || (entry && entry.brief === brief))) return "run";
  if (Array.isArray(verdict.skipped) && verdict.skipped.some((entry) =>
    entry === brief || (entry && entry.brief === brief))) return "skipped";
  return null;
}

test("the executable adjudicator carries the launch contract through to a publishable verdict", (t) => {
  const { verdict, launch, result, argsPath, workflowPath, effectiveTmpDir } = runWrapper(t, "complete");

  assert.equal(launch.command, process.execPath);
  assert.deepEqual(launch.args, [
    "/opt/minos/runtime/ensemble.mjs",
    "--json-args",
    `@${argsPath}`,
    workflowPath,
  ]);
  assert.equal(launch.cwd, workflowsDir);
  assert.equal(launch.record, "on");
  assert.equal(
    realpathSync(dirname(launch.recordDir)),
    realpathSync(effectiveTmpDir),
    "the executable wrapper creates Ensemble records directly in Node's effective temporary directory",
  );
  assert.match(basename(launch.recordDir), /^minos-ensemble-record-[^/]+$/);
  assert.equal(verdict.status, "complete");
  assert.deepEqual(verdict.confirmedFindings.map((finding) => finding.id), ["specialist:1"]);
  assert.deepEqual(verdict.confirmedFindings.map((finding) => ({
    path: finding.path,
    line: finding.line,
  })), [{ path: "internal/review.go", line: 42 }]);
  assert.match(result.stderr, /\[workflow\] complete/);
});

test("the executable adjudicator retains the complete record directory for archival", (t) => {
  const { verdict, launch, minosRunDir } = runWrapper(t, "complete", { retention: "success" });
  const retained = join(minosRunDir, "ensemble-records", basename(launch.recordDir));

  assert.equal(verdict.status, "complete");
  assert.equal(existsSync(launch.recordDir), false, "the temporary record is still removed");
  assert.equal(existsSync(join(retained, "runs", "cwd", "namespace", "run", "manifest.json")), true);
  assert.equal(
    readFileSync(
      join(retained, "runs", "cwd", "namespace", "run", "agents", "000002", "attempt-001-claude.jsonl"),
      "utf8",
    ),
    "RETAINED-VERIFIER-TRANSCRIPT-0731\n",
  );
});

test("record retention failure emits one diagnostic without changing the verdict", (t) => {
  const { verdict, result } = runWrapper(t, "complete", { retention: "failure" });
  const diagnostics = result.stderr.match(/adjudicated-review: could not retain run record for archival:[^\n]*/g) || [];

  assert.equal(verdict.status, "complete");
  assert.deepEqual(verdict.confirmedFindings.map((finding) => finding.id), ["specialist:1"]);
  assert.equal(diagnostics.length, 1);
  assert.match(result.stderr, /\[workflow\] complete/);
});

test("record retention survives a mode-zero subdirectory", (t) => {
  const { verdict, result, retentionShape } = runWrapper(t, "retention-mode-zero", { retention: "success" });
  if (!retentionShape.modeZeroUnreadable)
    return t.skip("the current user can read mode-zero directories");
  assert.equal(verdict.status, "complete");
  assert.match(result.stderr, /could not retain run-record entry "mode-zero"/);
});

test("record retention copies a symlink loop without following it", (t) => {
  const { verdict, launch, minosRunDir } = runWrapper(t, "retention-symlink-loop", { retention: "success" });
  const retainedLink = join(minosRunDir, "ensemble-records", basename(launch.recordDir), "symlink-loop");
  assert.equal(verdict.status, "complete");
  assert.equal(lstatSync(retainedLink).isSymbolicLink(), true);
  assert.equal(readlinkSync(retainedLink), ".");
});

test("a partial retained record carries an incompleteness marker", (t) => {
  const { verdict, launch, minosRunDir, result } = runWrapper(t, "retention-partial", { retention: "success" });
  const retained = join(minosRunDir, "ensemble-records", basename(launch.recordDir));
  const marker = readFileSync(join(retained, "RETENTION-INCOMPLETE"), "utf8");
  assert.equal(verdict.status, "complete");
  assert.match(marker, /runs\/cwd\/namespace\/run\/manifest\.json/);
  assert.match(result.stderr, /could not retain run-record entry/);
});

test("the adjudicated verdict distinguishes a misconfigured brief that ran from one that skipped", (t) => {
  const brief = ".review/missing/scoped.md";
  const ran = runWrapper(t, "brief-runs").verdict;
  const skipped = runWrapper(t, "brief-skips").verdict;

  assert.equal(ran.status, "complete");
  assert.equal(skipped.status, "complete");
  assert.equal(machineReadableBriefStatus(ran, brief), "run");
  assert.equal(machineReadableBriefStatus(skipped, brief), "skipped");
  assert.equal(ran.skipped.some((entry) => entry.brief === brief), false);
  assert.equal(skipped.ran.some((entry) => entry.brief === brief), false);
  const expectedMisconfigurations = [{
    brief,
    title: "Scoped",
    kind: "misconfigured-scope",
    reason: "brief scope missing/ matches no repository directory",
  }];
  assert.deepEqual(ran.misconfigurations, expectedMisconfigurations);
  assert.deepEqual(skipped.misconfigurations, expectedMisconfigurations);
});

test("the executable verdict preserves every inapplicable partition through the wrapper", (t) => {
  const verdict = runWrapper(t, "partition-detail").verdict;

  assert.equal(verdict.status, "complete");
  assert.deepEqual(verdict.ran, [{
    brief: ".review/partitioned.md",
    title: "Partitioned",
    inapplicableUnits: [
      {
        label: "repository-review-partitioned-md-2-claude",
        concern: "Inapplicable alpha",
        reason: "WRAPPER-PARTITION-ALPHA-0728",
      },
      {
        label: "repository-review-partitioned-md-3-claude",
        concern: "Inapplicable beta",
        reason: "WRAPPER-PARTITION-BETA-0728",
      },
    ],
  }]);
  assert.deepEqual(verdict.skipped, []);
});

test("the executable verdict carries observations without admitting them as findings", (t) => {
  const verdict = runWrapper(t, "observation").verdict;

  assert.equal(verdict.status, "complete");
  assert.deepEqual(verdict.confirmedFindings.map((finding) => finding.id), ["specialist:1"]);
  assert.equal(verdict.confirmedFindings.some((finding) =>
    finding.id === "specialist:observation:1"), false);
  assert.deepEqual(verdict.outOfScopeObservations, [{
    id: "specialist:observation:1",
    source: "Correctness",
    title: "Unchanged adjacent defect",
    path: "internal/adjacent.go",
    line: 73,
    explanation: "OBSERVATION-SENTINEL-719 remains unverified.",
    observingLabel: "specialist",
    verified: false,
  }]);
});

test("the executable verdict accepts an omitted optional observation array", (t) => {
  const verdict = runWrapper(t, "observation-omitted").verdict;

  assert.equal(verdict.status, "complete");
  assert.deepEqual(verdict.outOfScopeObservations, []);
  assert.deepEqual(verdict.confirmedFindings.map((finding) => finding.id), ["specialist:1"]);
});

for (const failure of [
  {
    scenario: "failed-leg",
    condition: /agent specialist has status failed; expected complete/,
  },
  {
    scenario: "missing-verdict",
    condition: /finding "Publishable sentinel" has no complete verdict \(verifier returned no valid result\)/,
  },
  {
    scenario: "malformed-envelope",
    condition: /Expected property name|Unexpected token/,
  },
  {
    scenario: "corrupt-manifest",
    condition: /^run manifest is unreadable:/,
  },
]) {
  test(`the executable adjudicator fails closed on ${failure.scenario}`, (t) => {
    const { verdict } = runWrapper(t, failure.scenario);

    assertWithheld(verdict);
    assert.match(verdict.incomplete.join("\n"), failure.condition);
  });
}

test("the executable adjudicator fails closed on invalid invocation", () => {
  const result = spawnSync(wrapperPath, [], {
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  });

  assert.equal(result.status, 0);
  assert.equal(result.signal, null);
  const verdict = JSON.parse(result.stdout);
  assertWithheld(verdict);
  assert.deepEqual(verdict.incomplete, [
    "usage: adjudicated-review <workflow-script-abs-path> --json-args @<argsfile>",
  ]);
  assert.match(result.stderr, /adjudicated-review: usage:/);
  assert.match(result.stderr, /\[workflow\] complete/);
});
