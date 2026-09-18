#!/usr/bin/env node

// Files verified advisory findings and brief configuration diagnostics to
// the annexe. An unavailable log leaves the entries in the run record;
// filing never creates a pull-request review or changes the verdict.

import { readFileSync } from "node:fs";
import { fileIssueLogEntries } from "./issue-log-filing.mjs";

const argv = process.argv.slice(2);
if (argv.length !== 3 && argv.length !== 4) {
  process.stderr.write("usage: node workflows/file-triage.mjs OUTPUT_DIR ENTRIES_FILE ORIENTATION [CREDENTIAL_FILE]\n");
  process.exit(2);
}
const [_outputDir, entriesPath, orientationPath, credentialFile = null] = argv;

function readJson(path, label) {
  try {
    return JSON.parse(readFileSync(path, "utf8"));
  } catch (error) {
    process.stderr.write(`${label} is missing or not valid JSON: ${path} (${error.message})\n`);
    process.exit(2);
  }
}

const entries = readJson(entriesPath, "triage entries");
if (!Array.isArray(entries)) {
  process.stderr.write(`triage entries must be a JSON array: ${entriesPath}\n`);
  process.exit(2);
}
const orientation = readJson(orientationPath, "orientation record");

const outcome = fileIssueLogEntries({ orientation, entries, credentialFile });
process.stdout.write(`${JSON.stringify(outcome)}\n`);
