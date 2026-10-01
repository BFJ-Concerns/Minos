import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { operationRoot } from "./temporary-directory-fixture.mjs";

const workflowsDir = dirname(fileURLToPath(import.meta.url));
const wrapperPath = join(workflowsDir, "adjudicated-review");

const preloadSource = String.raw`
import childProcess from "node:child_process";
import { EventEmitter } from "node:events";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { syncBuiltinESMExports } from "node:module";
import { join } from "node:path";
import { PassThrough } from "node:stream";

childProcess.spawn = function recordedSpawn(_command, _arguments, options) {
  const envelope = JSON.parse(readFileSync(process.env.MINOS_FIXTURE_ENVELOPE, "utf8"));
  const recordDir = options.env.ENSEMBLE_RUN_RECORD_DIR;
  writeFileSync(process.env.MINOS_FIXTURE_RECORD, recordDir);
  const archive = join(recordDir, "runs", "cwd", process.env.MINOS_FIXTURE_NAMESPACE, "run");
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

export function executableVerdict(t, envelope, { namespace, workflow, requireCompletion = false }) {
  const root = operationRoot(t, `minos-${namespace}-wrapper-`);
  const envelopePath = join(root, "envelope.json");
  const preloadPath = join(root, "record-ensemble.mjs");
  const recordPath = join(root, "record-dir.txt");
  const argsPath = join(root, "args.json");
  const workflowPath = join(root, workflow);
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
        MINOS_FIXTURE_NAMESPACE: namespace,
        MINOS_FIXTURE_ENVELOPE: envelopePath,
        MINOS_FIXTURE_RECORD: recordPath,
      },
    },
  );
  assert.equal(result.status, 0, result.stderr);
  if (requireCompletion) assert.match(result.stderr, /\[workflow\] complete/);
  const recordDir = readFileSync(recordPath, "utf8");
  assert.equal(
    existsSync(recordDir),
    false,
    "the executable wrapper removes the exact archive consumed by adjudication",
  );
  return JSON.parse(result.stdout);
}

