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
import { fileURLToPath, pathToFileURL } from "node:url";
import test from "node:test";

import { resolveRouting } from "./role-routing.mjs";

const workflowsDir = dirname(fileURLToPath(import.meta.url));
const wrapperPath = join(workflowsDir, "adjudicated-review");
const workflowSource = readFileSync(join(workflowsDir, "review-briefs.js"), "utf8");
const workflowBody = workflowSource.replace(/^export const meta =/m, "const meta =");
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
const runBriefWorkflow = new AsyncFunction(
  "agent",
  "parallel",
  "pipeline",
  "phase",
  "log",
  "args",
  workflowBody,
);

const briefPath = ".review/partitioned.md";
const mixedReasons = [
  "PARTITION-INAPPLICABLE-ALPHA-0728",
  "PARTITION-INAPPLICABLE-BETA-0728",
];
const allInapplicableReasons = [
  "ALL-INAPPLICABLE-ALPHA-0728",
  "ALL-INAPPLICABLE-BETA-0728",
];

const preloadSource = String.raw`
import childProcess from "node:child_process";
import { EventEmitter } from "node:events";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { syncBuiltinESMExports } from "node:module";
import { join } from "node:path";
import { PassThrough } from "node:stream";

childProcess.spawn = function recordedSpawn(_command, _arguments, options) {
  const envelope = JSON.parse(readFileSync(process.env.MINOS_PARTITION_ENVELOPE, "utf8"));
  const recordDir = options.env.ENSEMBLE_RUN_RECORD_DIR;
  writeFileSync(process.env.MINOS_PARTITION_RECORD, recordDir);
  const archive = join(recordDir, "runs", "cwd", "partition", "run");
  mkdirSync(join(archive, "agents"), { recursive: true });
  writeFileSync(
    join(archive, "manifest.json"),
    JSON.stringify({ kind: "run_manifest", status: "complete" }),
  );
  envelope.requiredModelEvidence.forEach((leg, index) => {
    const directory = join(archive, "agents", String(index + 1).padStart(6, "0"));
    mkdirSync(directory, { recursive: true });
    writeFileSync(
      join(directory, "agent.json"),
      JSON.stringify({ label: leg.label, status: "complete" }),
    );
  });

  const child = new EventEmitter();
  child.stdout = new PassThrough();
  child.stderr = new PassThrough();
  setImmediate(() => {
    child.stdout.end(JSON.stringify(envelope) + "\n");
    child.stderr.end();
    child.emit("close", 0, null);
  });
  return child;
};
syncBuiltinESMExports();
`;

function operationRoot(t, prefix) {
  const root = mkdtempSync(join(tmpdir(), prefix));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  return root;
}

function workflowInput(files) {
  return {
    target: "target-partition-0728",
    head: "head-partition-0728",
    occasion: "release",
    workspace: "/workspace",
    hasReviewDirectory: true,
    changedPaths: files,
    trackedFiles: files.map((path) => ({ path, bytes: 100 })),
    briefs: [{
      path: briefPath,
      readPath: `/workspace/${briefPath}`,
      content:
        "---\nextent: full\nsweep: per-file\noccasion: release\n---\n" +
        "Judge every partition without losing an inapplicability reason.",
      scope: null,
      scopeExists: true,
    }],
    guidance: [{ repository: null, path: "AGENTS.md", origin: "checked-in", content: "PARTITION-DISPOSITION-GUIDANCE-0728" }],
    routing: resolveRouting({ provisioned: ["claude", "codex"] }),
    instructionBriefs: [
      {
        path: "workflows/review-briefs/repository.md",
        readPath: "/minos/workflows/review-briefs/repository.md",
        content: "PARTITION-REPOSITORY-BRIEF-0728",
      },
      {
        path: "workflows/review-briefs/verifier.md",
        readPath: "/minos/workflows/review-briefs/verifier.md",
        content: "PARTITION-VERIFIER-BRIEF-0728",
      },
    ],
  };
}

function partitionFor(scenario) {
  if (scenario === "mixed") {
    return [
      { id: "applicable", concern: "Applicable partition", files: ["pkg/a.go"] },
      { id: "alpha", concern: "Inapplicable alpha", files: ["pkg/b.go"] },
      { id: "beta", concern: "Inapplicable beta", files: ["pkg/c.go"] },
    ];
  }
  return [
    { id: "alpha", concern: "Partition alpha", files: ["pkg/a.go"] },
    { id: "beta", concern: "Partition beta", files: ["pkg/b.go"] },
  ];
}

function specialistResult(applicable, reason = "the assigned concern applies") {
  return {
    applicability: {
      status: applicable ? "applicable" : "inapplicable",
      reason,
    },
    findings: [],
    outOfScopeObservations: [],
  };
}

async function producerEnvelope(scenario) {
  const partition = partitionFor(scenario);
  const files = partition.flatMap((unit) => unit.files);
  let specialistIndex = 0;
  const agent = async (_prompt, options = {}) => {
    if (options.label?.startsWith("brief-partition-")) {
      return { units: partition };
    }
    if (!options.label?.startsWith("repository-")) {
      throw new Error(`unexpected agent label ${String(options.label)}`);
    }
    const index = specialistIndex++;
    if (scenario === "mixed" && index === 0) return specialistResult(true);
    if (scenario === "all-ran") return specialistResult(true);
    const reasons = scenario === "mixed" ? mixedReasons : allInapplicableReasons;
    return specialistResult(false, reasons[scenario === "mixed" ? index - 1 : index]);
  };
  const parallel = async (thunks) => Promise.all(
    thunks.map((thunk) => thunk().catch(() => null)),
  );
  return runBriefWorkflow(
    agent,
    parallel,
    async () => [],
    () => {},
    () => {},
    workflowInput(files),
  );
}

function executableVerdict(t, envelope) {
  const root = operationRoot(t, "minos-partition-wrapper-");
  const envelopePath = join(root, "envelope.json");
  const preloadPath = join(root, "record-ensemble.mjs");
  const recordPath = join(root, "record-dir.txt");
  const argsPath = join(root, "args.json");
  const workflowPath = join(root, "review-briefs.js");
  writeFileSync(envelopePath, JSON.stringify(envelope));
  writeFileSync(preloadPath, preloadSource);
  writeFileSync(argsPath, "{}");
  writeFileSync(workflowPath, "return {};\n");

  const result = spawnSync(
    wrapperPath,
    [workflowPath, "--json-args", `@${argsPath}`],
    {
      cwd: workflowsDir,
      encoding: "utf8",
      env: {
        ...process.env,
        NODE_OPTIONS: `--import=${pathToFileURL(preloadPath).href}`,
        MINOS_PARTITION_ENVELOPE: envelopePath,
        MINOS_PARTITION_RECORD: recordPath,
      },
    },
  );
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stderr, /\[workflow\] complete/);
  const recordDir = readFileSync(recordPath, "utf8");
  assert.equal(
    existsSync(recordDir),
    false,
    "the executable wrapper removes the exact archive consumed by adjudication",
  );
  return JSON.parse(result.stdout);
}

async function finalVerdict(t, scenario) {
  const envelope = await producerEnvelope(scenario);
  const verdict = executableVerdict(t, envelope);
  assert.equal(verdict.status, "complete", verdict.incomplete.join("\n"));
  return { envelope, verdict };
}

function entriesFor(entries, path) {
  return entries.filter((entry) => entry.brief === path);
}

test("mixed partition results become one durable run disposition with every inapplicability reason", async (t) => {
  const { verdict } = await finalVerdict(t, "mixed");
  const ran = entriesFor(verdict.ran, briefPath);
  const skipped = entriesFor(verdict.skipped, briefPath);

  assert.equal(ran.length, 1, "the durable verdict records the brief path as run once");
  assert.deepEqual(skipped, [], "a partially reviewed brief is not also recorded as skipped");
  const durableRun = JSON.stringify(ran[0]);
  for (const reason of mixedReasons) {
    assert.match(
      durableRun,
      new RegExp(reason),
      `the durable run disposition preserves ${reason}`,
    );
  }
});

test("all-inapplicable partitions become one durable skipped disposition without losing reasons", async (t) => {
  const { verdict } = await finalVerdict(t, "all-inapplicable");
  const ran = entriesFor(verdict.ran, briefPath);
  const skipped = entriesFor(verdict.skipped, briefPath);

  assert.deepEqual(ran, []);
  assert.equal(skipped.length, 1, "the durable verdict records the brief path as skipped once");
  const durableSkip = JSON.stringify(skipped[0]);
  for (const reason of allInapplicableReasons) {
    assert.match(
      durableSkip,
      new RegExp(reason),
      `the durable skipped disposition preserves ${reason}`,
    );
  }
});

test("all-ran partitions remain one durable run disposition", async (t) => {
  const { verdict } = await finalVerdict(t, "all-ran");

  assert.equal(entriesFor(verdict.ran, briefPath).length, 1);
  assert.deepEqual(entriesFor(verdict.skipped, briefPath), []);
});

for (const malformed of [
  {
    name: "unreadable",
    disposition: null,
    diagnostic: /unreadable disposition/,
  },
  {
    name: "run",
    disposition: { brief: briefPath, status: "run" },
    diagnostic: /malformed run disposition/,
  },
  {
    name: "skipped",
    disposition: {
      brief: briefPath,
      title: "Partitioned",
      status: "skipped",
      skipKind: "inapplicable",
      reason: "",
    },
    diagnostic: /malformed skipped disposition/,
  },
  {
    name: "unknown-status",
    disposition: {
      brief: briefPath,
      title: "Partitioned",
      status: "mystery",
    },
    diagnostic: /unknown status mystery/,
  },
]) {
  test(`the adjudicator still fails closed on a genuinely malformed ${malformed.name} disposition`, async (t) => {
    const envelope = await producerEnvelope("all-ran");
    envelope.briefs = [malformed.disposition];
    const verdict = executableVerdict(t, envelope);

    assert.equal(verdict.status, "incomplete");
    assert.equal(verdict.complete, false);
    assert.deepEqual(verdict.confirmedFindings, []);
    assert.match(verdict.incomplete.join("\n"), malformed.diagnostic);
  });
}
