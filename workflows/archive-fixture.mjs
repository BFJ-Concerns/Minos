import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { adjudicate } from "./run-record-adjudicator.mjs";
import { operationRoot } from "./temporary-directory-fixture.mjs";

export function fixtureArchive(t, {
  manifestStatus = "complete",
  records = {},
  duplicateRecords = [],
  rawAgentRecords = [],
  manifests = 1,
  agentsDirectory = true,
} = {}) {
  const recordDir = operationRoot(t, "minos-adjudicator-fixture-");
  for (let run = 0; run < manifests; run += 1) {
    const archive = join(recordDir, "runs", "cwd", `namespace-${run}`, `run-${run}`);
    mkdirSync(agentsDirectory ? join(archive, "agents") : archive, { recursive: true });
    writeFileSync(join(archive, "manifest.json"), JSON.stringify({ kind: "run_manifest", status: manifestStatus }));
    const agentRecords = [
      ...[...Object.entries(records), ...duplicateRecords]
        .map(([label, record]) => JSON.stringify({ label, ...record })),
      ...rawAgentRecords,
    ];
    agentRecords.forEach((record, index) => {
      const directory = join(archive, "agents", String(index + 1).padStart(6, "0"));
      mkdirSync(directory, { recursive: true });
      writeFileSync(join(directory, "agent.json"), record);
    });
  }
  return recordDir;
}

export function adjudicateEnvelope(t, envelope) {
  const records = Object.fromEntries(envelope.requiredModelEvidence.map((leg) => [
    leg.label, { status: "complete", resolved_model: leg.pinnedModel },
  ]));
  return adjudicate({ envelope, recordDir: fixtureArchive(t, { records }) });
}
