#!/usr/bin/env node

// Renders the run's unverified side material — out-of-scope observations and
// review-brief misconfigurations — as one pull-request comment-review payload
// the lead posts through the guarded forge review command. The pull request
// is the one delivery surface: Minos files nothing in reviewed projects'
// annexes, and confirmed findings never travel here — they ride the review
// itself (C19). Writing an observation is presentation work; a caller treats
// this script's failure as degradation, never a run outcome.

import { readFileSync } from "node:fs";

const entriesPath = process.argv[2];
if (!entriesPath || process.argv.length > 3) {
  process.stderr.write("usage: node workflows/publish-observations.mjs ENTRIES\n");
  process.exit(2);
}

const entries = JSON.parse(readFileSync(entriesPath, "utf8"));
if (!Array.isArray(entries)) throw new Error("entries must be a JSON array");

function normaliseEntry(entry) {
  if (entry.kind === "out-of-scope-observation") {
    return {
      ...entry,
      label: "Out-of-scope observation (unverified)",
    };
  }
  if (entry.kind === "review-brief-misconfiguration") {
    return {
      ...entry,
      label: "Review brief misconfiguration",
      path: entry.brief,
      line: 1,
      explanation: entry.reason,
    };
  }
  throw new Error(`unknown entry kind ${String(entry.kind)}`);
}

const observations = entries.map(normaliseEntry);

function inlineComment(entry) {
  return {
    path: entry.path,
    body: `**${entry.label}: ${entry.title}**\n\n${entry.explanation}\n\nThis was noticed outside the review's scope and has not been verified.`,
    line: entry.line,
  };
}

process.stdout.write(JSON.stringify({
  destination: "pull-request",
  body: "Unverified observations from the review, for the author's judgement.",
  comments: observations.map(inlineComment),
}) + "\n");
