#!/usr/bin/env node

// Delivers the run's triage material — the advisory findings, unverified
// observations and review-brief misconfigurations the publication composer
// set aside — to the reviewed project. The project's annexe issue log is
// the destination; the pull request takes the same material as one comment
// review only where no log can (no annexe, or a filing the forge refused).
//
// Usage:
//   file-triage.mjs OUTPUT_DIR ENTRIES_FILE ORIENTATION [CREDENTIAL_FILE]
//
// Prints one JSON line. `destination` is `annexe` when the log took the
// entries (`written` counts the ones newly appended; `location` names the
// log), `pull-request` when the caller must post the fallback review the
// composer's body and comments files describe (`reason` says why), or
// `none` when there was nothing to deliver. Delivery is presentation work:
// a failure here degrades and never fails the run, so the script exits 0
// on every outcome it can describe and reserves non-zero for arguments it
// cannot read at all.

import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import { TRIAGE_BODY, triageComment } from "./finding-presentation.mjs";
import { fileIssueLogEntries } from "./issue-log-filing.mjs";

const argv = process.argv.slice(2);
if (argv.length !== 3 && argv.length !== 4) {
  process.stderr.write("usage: node workflows/file-triage.mjs OUTPUT_DIR ENTRIES_FILE ORIENTATION [CREDENTIAL_FILE]\n");
  process.exit(2);
}
const [outputDir, entriesPath, orientationPath, credentialFile = null] = argv;

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
if (outcome.destination === "pull-request") {
  mkdirSync(outputDir, { recursive: true });
  const bodyPath = join(outputDir, "triage-review.md");
  const commentsPath = join(outputDir, "triage-review-comments.json");
  writeFileSync(bodyPath, `${TRIAGE_BODY}\n`);
  writeFileSync(commentsPath, `${JSON.stringify(entries.map(triageComment))}\n`);
  outcome.review = { verdict: "comment", body: bodyPath, comments: commentsPath };
}
process.stdout.write(`${JSON.stringify(outcome)}\n`);
