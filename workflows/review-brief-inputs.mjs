#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { existsSync, lstatSync, readFileSync } from "node:fs";
import { dirname, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

import { reviewBriefsFromWorkspace } from "./review-brief-files.mjs";
import { briefRecords } from "./brief-dispositions.mjs";
import { guidanceFromOrientation } from "./orientation-guidance.mjs";
import { pullRequestFromOrientation } from "./pull-request-description.mjs";
import { reviewContracts } from "./review-contracts.mjs";
import { promptSettingsFromEnvironment } from "./prompt-settings.mjs";
import { reviewPrompts } from "./review-prompts.mjs";
import { routingFromEnvironment } from "./role-routing.mjs";

const [target, head, ...rest] = process.argv.slice(2);
const occasion = rest.length === 1 ? rest[0] : null;
const argumentError = !target || !head || rest.length > 1;

if (argumentError) {
  process.stderr.write("usage: node workflows/review-brief-inputs.mjs TARGET HEAD [OCCASION]\n");
  process.exitCode = 2;
} else try {
  const workflowRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
  const orientationPath = process.env.MINOS_ORIENTATION;
  if (!orientationPath) throw new Error("MINOS_ORIENTATION is required");
  const orientation = JSON.parse(readFileSync(orientationPath, "utf8"));
  if (!orientation || typeof orientation.repository !== "string" || orientation.repository === "")
    throw new Error("orientation omitted its repository path");
  const guidance = guidanceFromOrientation(orientation);
  const routing = routingFromEnvironment();

  const workspace = resolve(process.env.MINOS_WORKSPACE || orientation.repository);

  const { hasReviewDirectory, briefs } = reviewBriefsFromWorkspace(workspace);

  const gitLines = (...args) => execFileSync("git", ["-C", workspace, ...args], {
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
    maxBuffer: Infinity,
  }).split("\0").filter(Boolean);
  // Without rename detection a moved file lists both its old and its new
  // path, so a file moved out of a brief's directory still engages the
  // brief; the scope gate (review-scope-inputs.mjs) reads the diff the same
  // way, so the two agree on which briefs a change engages.
  const changedPaths = gitLines("diff", "--no-renames", "--name-only", "-z", target, head);
  const trackedPaths = gitLines("ls-files", "-z");
  const trackedFiles = [];
  for (const path of trackedPaths) {
    const absolute = resolve(workspace, path);
    if (!absolute.startsWith(workspace + sep) || !existsSync(absolute) || !lstatSync(absolute).isFile()) continue;
    trackedFiles.push({ path });
  }

  const pullRequest = pullRequestFromOrientation(orientation);
  const { concerns, prompts } = reviewPrompts(workflowRoot, promptSettingsFromEnvironment());

  const workflowInput = {
    target,
    head,
    occasion,
    hasReviewDirectory,
    changedPaths,
    trackedFiles,
    briefs: briefRecords(briefs, occasion, changedPaths),
    guidance,
    routing,
    ...(pullRequest === undefined ? {} : { pullRequest }),
    prompts: {
      repository: prompts.repository,
      briefRelevance: prompts.briefRelevance,
      briefPartition: prompts.briefPartition,
      assignment: prompts.assignment,
      merge: prompts.merge,
      verifier: prompts.verifier,
    },
    contracts: reviewContracts(concerns.map(({ name }) => name)),
  };

  process.stdout.write(JSON.stringify(workflowInput) + "\n");
} catch (error) {
  // Every input failure is a usage error: the lifecycle reads exit 2 as
  // "the input could not be built", never as a crash to diagnose.
  process.stderr.write(`${error.message}\n`);
  process.exitCode = 2;
}
