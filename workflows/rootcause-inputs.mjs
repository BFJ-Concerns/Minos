#!/usr/bin/env node

import { readFileSync } from "node:fs";
import { isAbsolute } from "node:path";

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
  let evidence = null;
  try {
    evidence = evidencePath ? readFileSync(evidencePath, "utf8") : null;
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
    evidence,
  })}\n`);
}
