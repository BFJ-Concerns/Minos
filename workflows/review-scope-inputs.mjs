#!/usr/bin/env node

// Compatibility command for the selectable resident lead. It reports brief
// engagement only, using the same brief rules as scripted push inspection.

import { readFileSync } from "node:fs";

import { briefEngagement } from "./brief-dispositions.mjs";
import { changedPaths } from "./changed-paths.mjs";
import { reviewBriefsFromWorkspace } from "./review-brief-files.mjs";

const [base, head, occasion = null, ...extra] = process.argv.slice(2);
try {
  if (!base || !head || extra.length) {
    throw new Error("usage: node workflows/review-scope-inputs.mjs BASE HEAD [OCCASION]");
  }
  const workspace = process.env.MINOS_WORKSPACE ||
    JSON.parse(readFileSync(process.env.MINOS_ORIENTATION, "utf8")).repository;
  const { briefs } = reviewBriefsFromWorkspace(workspace);
  const paths = changedPaths(workspace, base, head);
  const { briefsEngage } = briefEngagement(briefs, occasion, paths);
  process.stdout.write(`${JSON.stringify({ briefsEngage })}\n`);
} catch (error) {
  process.stderr.write(`${error.message}\n`);
  process.exitCode = 2;
}
