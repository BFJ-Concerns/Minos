#!/usr/bin/env node

import { readFileSync, writeFileSync } from "node:fs";
import { isAbsolute } from "node:path";

// Evidence at or below this size is handed over whole; anything larger is
// excerpted to the failure-relevant lines so the repair agent is not fed an
// entire green test run's output.
const WHOLE_EVIDENCE_LIMIT = 16 * 1024;
const FAILURE_LINE = /fail|flaky|error|panic|retry|\btry\b|abort|timed?[ -]?out|assert|cancel/i;
const CONTEXT_LINES = 2;
const SUMMARY_TAIL_LINES = 50;

function excerptText(text) {
  if (text.length <= WHOLE_EVIDENCE_LIMIT) return text;
  const lines = text.split("\n");
  const keep = new Array(lines.length).fill(false);
  for (let index = 0; index < lines.length; index += 1) {
    if (!FAILURE_LINE.test(lines[index])) continue;
    for (
      let context = Math.max(0, index - CONTEXT_LINES);
      context <= Math.min(lines.length - 1, index + CONTEXT_LINES);
      context += 1
    ) keep[context] = true;
  }
  for (let index = Math.max(0, lines.length - SUMMARY_TAIL_LINES); index < lines.length; index += 1)
    keep[index] = true;

  const excerpt = [];
  let elided = 0;
  for (let index = 0; index < lines.length; index += 1) {
    if (keep[index]) {
      if (elided > 0) excerpt.push(`[… ${elided} lines elided …]`);
      elided = 0;
      excerpt.push(lines[index]);
    } else elided += 1;
  }
  if (elided > 0) excerpt.push(`[… ${elided} lines elided …]`);
  return excerpt.join("\n");
}

// Forge check evidence arrives as {head_sha, target_sha, runs:[{…, jobs:[{…,
// log}]}]}; excerpt each job log in place so the surrounding structure —
// run and job names, statuses — survives intact.
function excerptEvidence(content) {
  let parsed;
  try {
    parsed = JSON.parse(content);
  } catch {
    return excerptText(content);
  }
  if (!parsed || !Array.isArray(parsed.runs)) return excerptText(content);
  const runs = parsed.runs.map((run) =>
    run && Array.isArray(run.jobs)
      ? {
        ...run,
        jobs: run.jobs.map((job) =>
          job && typeof job.log === "string" ? { ...job, log: excerptText(job.log) } : job,
        ),
      }
      : run,
  );
  return JSON.stringify({ ...parsed, runs }, null, 1);
}

const argv = process.argv.slice(2);
const values = new Map();
const recognised = new Set(["--skill", "--command", "--exit-status", "--evidence"]);
let invalid = argv.length === 0;
for (let index = 0; index < argv.length && !invalid; index += 2) {
  const option = argv[index];
  const value = argv[index + 1];
  if (!recognised.has(option) || value === undefined || values.has(option)) invalid = true;
  else values.set(option, value);
}

const skillPath = values.get("--skill");
const command = values.get("--command") ?? "";
const statusText = values.get("--exit-status");
const exitStatus = statusText === undefined ? null : Number(statusText);
const evidencePath = values.get("--evidence");

if (
  invalid ||
  !skillPath ||
  !isAbsolute(skillPath) ||
  (command === "" && statusText !== undefined) ||
  (command !== "" && (!Number.isInteger(exitStatus) || exitStatus < 0)) ||
  (command === "" && !evidencePath) ||
  (evidencePath && !isAbsolute(evidencePath))
) {
  process.stderr.write(
    "usage: node workflows/rootcause-inputs.mjs --skill ABSOLUTE_PATH [--command COMMAND --exit-status STATUS] [--evidence ABSOLUTE_FILE]\n",
  );
  process.exitCode = 2;
} else {
  let excerptPath = null;
  try {
    if (evidencePath) {
      const content = readFileSync(evidencePath, "utf8");
      const excerpt = excerptEvidence(content);
      if (excerpt === content) excerptPath = evidencePath;
      else {
        excerptPath = `${evidencePath}.excerpt`;
        writeFileSync(excerptPath, excerpt);
      }
    }
  } catch {
    process.stderr.write(
      "usage: node workflows/rootcause-inputs.mjs --skill ABSOLUTE_PATH [--command COMMAND --exit-status STATUS] [--evidence ABSOLUTE_FILE]\n",
    );
    process.exit(2);
  }
  process.stdout.write(`${JSON.stringify({
    skillPath: skillPath.length > 1 ? skillPath.replace(/\/$/, "") : skillPath,
    command,
    exitStatus,
    evidencePath: evidencePath ?? null,
    excerptPath,
  })}\n`);
}
