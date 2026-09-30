#!/usr/bin/env node

// Delivers the publication plan's triage entries to the repository's
// configured filing destination. The destination, the run's coordinates and
// the commit identity arrive in the environment the service exported; an
// unavailable destination leaves the entries in the run record, and filing
// never creates a pull-request review or changes the verdict.

import { readFileSync } from "node:fs";
import { deliverFilingEntries, validFilingDestination } from "./filing-destination.mjs";

const argv = process.argv.slice(2);
if (argv.length !== 2 && argv.length !== 3) {
  process.stderr.write("usage: node workflows/file-triage.mjs ENTRIES_FILE ORIENTATION [CREDENTIAL_FILE]\n");
  process.exit(2);
}
const [entriesPath, orientationPath, credentialFile = null] = argv;

function usageError(message) {
  process.stderr.write(`${message}\n`);
  process.exit(2);
}

function readJson(path, label) {
  try {
    return JSON.parse(readFileSync(path, "utf8"));
  } catch (error) {
    usageError(`${label} is missing or not valid JSON: ${path} (${error.message})`);
  }
}

function requiredEnvironment(name) {
  const value = process.env[name];
  if (!value) usageError(`${name} is required`);
  return value;
}

const entries = readJson(entriesPath, "triage entries");
if (!Array.isArray(entries)) usageError(`triage entries must be a JSON array: ${entriesPath}`);
const orientation = readJson(orientationPath, "orientation record");

let destination;
try {
  destination = JSON.parse(requiredEnvironment("MINOS_FILING_DESTINATION"));
} catch (error) {
  usageError(`MINOS_FILING_DESTINATION is not valid JSON (${error.message})`);
}
if (!validFilingDestination(destination)) usageError("MINOS_FILING_DESTINATION must name a kind of file, issue, pull-request-comment or none with the fields that kind takes");

const outcome = await deliverFilingEntries({
  destination,
  entries,
  source: orientation && orientation.source,
  reviewedRepository: `${requiredEnvironment("MINOS_OWNER")}/${requiredEnvironment("MINOS_REPO_NAME")}`,
  apiBase: process.env.MINOS_API_BASE || null,
  credentialFile,
  runDir: requiredEnvironment("MINOS_RUN_DIR"),
  identity: { name: requiredEnvironment("MINOS_COMMIT_AUTHOR_NAME"), email: requiredEnvironment("MINOS_COMMIT_AUTHOR_EMAIL") },
});
process.stdout.write(`${JSON.stringify(outcome)}\n`);
