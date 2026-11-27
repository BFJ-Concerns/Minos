#!/usr/bin/env node

// The input of a quick review (quick-review.js): the reviewed change, the
// paths it touches, against which a division into shares is checked, and
// the general reviewers', merge and verifier prompts.

import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { changedPaths } from "./changed-paths.mjs";
import { guidanceFromOrientation } from "./orientation-guidance.mjs";
import { pullRequestFromOrientation } from "./pull-request-description.mjs";
import { descriptionFromOrientation } from "./repository-description.mjs";
import { reviewContracts } from "./review-contracts.mjs";
import { promptSettingsFromEnvironment } from "./prompt-settings.mjs";
import { reviewPrompts } from "./review-prompts.mjs";
import { routingFromEnvironment } from "./role-routing.mjs";

const target = process.argv[2];
const head = process.argv[3];
if (!target || !head || process.argv.length > 4) {
  process.stderr.write("usage: node workflows/quick-review-inputs.mjs TARGET HEAD\n");
  process.exitCode = 2;
} else try {
  const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
  const orientationPath = process.env.MINOS_ORIENTATION;
  if (!orientationPath) throw new Error("MINOS_ORIENTATION is required");
  const orientation = JSON.parse(readFileSync(orientationPath, "utf8"));
  if (!orientation || typeof orientation.repository !== "string" || orientation.repository === "")
    throw new Error("orientation omitted its repository path");
  const workspace = resolve(process.env.MINOS_WORKSPACE || orientation.repository);
  const guidance = guidanceFromOrientation(orientation);
  const routing = routingFromEnvironment();
  const pullRequest = pullRequestFromOrientation(orientation);
  const repositoryDescription = descriptionFromOrientation(orientation);
  const { concerns, prompts } = reviewPrompts(repositoryRoot, promptSettingsFromEnvironment());
  process.stdout.write(JSON.stringify({
    target,
    head,
    changedPaths: changedPaths(workspace, target, head),
    guidance,
    routing,
    ...(pullRequest === undefined ? {} : { pullRequest }),
    ...(repositoryDescription === undefined ? {} : { repositoryDescription }),
    prompts: {
      general: prompts.general,
      generalAssignment: prompts.generalAssignment,
      merge: prompts.merge,
      verifier: prompts.verifier,
    },
    contracts: reviewContracts(concerns.map(({ name }) => name)),
  }) + "\n");
} catch (error) {
  // Every input failure is a usage error: the lifecycle reads exit 2 as
  // "the input could not be built", never as a crash to diagnose.
  process.stderr.write(`${error.message}\n`);
  process.exitCode = 2;
}
