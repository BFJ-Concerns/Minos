import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import { chmodSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import test from "node:test";

import { writeReport } from "./run-steps/write-report.mjs";
import { operationRoot } from "./temporary-directory-fixture.mjs";

// A run whose collect-timings writes the given timing record.
function reportedRun(t, record, extra = {}) {
  const runDir = operationRoot(t, "minos-report-");
  const tools = join(runDir, "tools");
  mkdirSync(tools);
  writeFileSync(join(runDir, "timing-record.fixture.json"), JSON.stringify(record));
  const collect = join(tools, "collect-timings");
  writeFileSync(collect, `#!/usr/bin/env sh\ncp "$1/timing-record.fixture.json" "$2"\n`);
  chmodSync(collect, 0o755);
  writeFileSync(join(runDir, "orientation.json"), JSON.stringify({ misconfigurations: [] }));
  return {
    runDir, workspace: runDir, orientation: join(runDir, "orientation.json"), head: "head-sha",
    pullRequest: { owner: "owner", repository: "repo", number: "7" },
    env: { ...process.env, MINOS_SETUP_WORKSPACE: join(tools, "setup-workspace") },
    judgementAnswers: new Map(), forgeNotes: [], ...extra,
  };
}

const record = {
  commands: [{ name: "review", attempt: 1, duration_ms: 600000 }, { name: "push-inspection", attempt: 2, duration_ms: 30000 }],
  phases: [
    { record: "ensemble-records/r1", phase: "propose", arms: 2, wall_ms: 400000, straggler: true, longest_arm: { label: "correctness", ms: 390000 } },
    { record: "ensemble-records/r2", phase: null, arms: 1, wall_ms: 5000, straggler: false, longest_arm: { label: "judge", ms: 5000 } },
    { record: "ensemble-records/r1", phase: "verify", arms: 2, wall_ms: 90000, straggler: false, longest_arm: { label: "verifier", ms: 90000 } },
  ],
  workers: [
    { record: "ensemble-records/r1", phase: "propose", label: "correctness", engine: "codex", model: "gpt-6.1-sol", status: "completed", execution_ms: 390000 },
    { record: "ensemble-records/r1", phase: "propose", label: "security", engine: "codex", model: "gpt-6.1-sol", status: "completed", started_at: "2026-11-27T08:00:00Z", ended_at: "2026-11-27T08:01:30Z" },
    { record: "ensemble-records/r1", phase: "verify", label: "verifier", engine: "claude", status: "failed", execution_ms: 90000 },
    { record: "ensemble-records/r2", phase: null, label: "judge", engine: "claude", model: "claude-opus-5-5", status: "completed", execution_ms: 5000 },
    { record: "ensemble-records/r1", phase: "verify", id: "agent-4", status: null, execution_ms: null },
  ],
};

test("the report gives every stage's and every dispatched worker's timing", async (t) => {
  const run = reportedRun(t, record);
  await writeReport(run, {}, { outcome: "clean" });
  const report = readFileSync(join(run.runDir, "report.md"), "utf8");
  for (const line of [
    "- review — 600.0 s",
    "- push-inspection@2 — 30.0 s",
    "- ensemble-records/r1 / propose — 2 workers, 400.0 s wall; waited on correctness (390.0 s)",
    "  - correctness (codex gpt-6.1-sol) — completed, 390.0 s",
    "  - security (codex gpt-6.1-sol) — completed, 90.0 s",
    "- ensemble-records/r1 / verify — 2 workers, 90.0 s wall",
    "  - verifier (claude) — failed, 90.0 s",
    "  - agent-4 — status unknown, time unknown",
    "- ensemble-records/r2 / no phase — 1 worker, 5.0 s wall",
    "  - judge (claude claude-opus-5-5) — completed, 5.0 s",
  ]) assert.ok(report.split("\n").includes(line), `report lacks ${JSON.stringify(line)}:\n${report}`);
});

test("the report names the main review's mode from its verdict, or the settled mode where none came back", async (t) => {
  const empty = { commands: [], phases: [], workers: [] };
  const read = async (settled) => {
    const run = reportedRun(t, empty);
    await writeReport(run, settled, { outcome: "clean" });
    return readFileSync(join(run.runDir, "report.md"), "utf8").split("\n");
  };
  assert.ok((await read({ reviewMode: { mode: "specialist" }, review: { mode: "quick" } })).includes("- Main review in the **quick** mode."));
  assert.ok((await read({ reviewMode: { mode: "quick" } })).includes("- Main review in the **quick** mode."));
  assert.ok((await read({ reviewMode: { mode: "quick" }, review: { mode: "specialist" } })).includes("- Main review in the **specialist** mode."));
  assert.ok((await read({ reviewMode: { mode: "specialist" } })).includes("- Main review in the **specialist** mode."));
  assert.ok(!(await read({})).some((line) => line.startsWith("- Main review in")));
});

test("under automatic the report says how the head came by its mode", async (t) => {
  const empty = { commands: [], phases: [], workers: [] };
  const read = async (reviewMode) => {
    const run = reportedRun(t, empty);
    await writeReport(run, { reviewMode }, { outcome: "clean" });
    return readFileSync(join(run.runDir, "report.md"), "utf8");
  };
  const chosen = await read({ mode: "quick", setting: "automatic", chosen: "A typo in a comment." });
  assert.ok(chosen.includes("- Main review in the **quick** mode.\n  - The mode was left to each head and the review-mode stage chose it. A typo in a comment.\n"), chosen);
  assert.match(await read({ mode: "quick", setting: "automatic", carried: true }), /reused review settled it/);
  const failed = await read({ mode: "specialist", setting: "automatic", problem: "stage review-mode ended without a result" });
  assert.match(failed, /no choice was made; the specialist review stood in/);
  assert.match(failed, /## Anomalies\n\n- The review-mode stage gave no usable answer, so the head got the specialist review: stage review-mode ended without a result/);
  assert.doesNotMatch(await read({ mode: "quick", setting: "quick" }), /left to each head/);
});

test("the report lists what a successful forge command said", async (t) => {
  const note = "minos forge review: forge review: inline comments folded into the body: no diff";
  const run = reportedRun(t, { commands: [], phases: [], workers: [] }, { forgeNotes: [note] });
  await writeReport(run, {}, { outcome: "changes-requested" });
  assert.match(readFileSync(join(run.runDir, "report.md"), "utf8"), new RegExp(`## Forge notes\n\n- ${note}\n`));
});
