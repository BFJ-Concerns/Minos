import { appendFileSync, mkdirSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";

import { CLEAN, CONTINUATION } from "../run-endings.mjs";

export async function recordFailure(run, settled, ending) {
  if (ending.outcome === CLEAN || ending.outcome === CONTINUATION) return;
  const { owner, repository, number } = run.pullRequest;
  const path = run.env.MINOS_FAILURE_LOG;
  const cause = (ending.cause ?? settled.terminalCause).replace(/[\r\n]+/g, " ");
  const line = `timestamp=${new Date().toISOString()} pull_request=${owner}/${repository}#${number} head=${run.head} stage=${ending.step ?? "conclude"} cause=${cause}\n`;
  if (path) {
    mkdirSync(dirname(path), { recursive: true });
    appendFileSync(path, line);
    // Crash salvage uses the same marker after it appends a digest.
    writeFileSync(join(run.runDir, ".salvage-recorded"), "driver recorded the failure\n");
  } else {
    // Without a local ledger, keep the evidence for the supervisor/archive.
    process.stderr.write(line);
    appendFileSync(join(run.runDir, "failure.log"), line);
  }
}
