#!/usr/bin/env node

import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { guidanceFromOrientation } from "./orientation-guidance.mjs";
import { pullRequestFromOrientation } from "./pull-request-description.mjs";
import { reviewContracts } from "./review-contracts.mjs";
import { reviewPrompts } from "./review-prompts.mjs";
import { routingFromEnvironment } from "./role-routing.mjs";
import { concernRoutingFromEnvironment } from "./concern-routing.mjs";

const target = process.argv[2];
const head = process.argv[3];
if (!target || !head || process.argv.length > 4) {
  process.stderr.write("usage: node workflows/review-inputs.mjs TARGET HEAD\n");
  process.exitCode = 2;
} else try {
  const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
  const orientationPath = process.env.MINOS_ORIENTATION;
  if (!orientationPath) throw new Error("MINOS_ORIENTATION is required");
  const orientation = JSON.parse(readFileSync(orientationPath, "utf8"));
  const guidance = guidanceFromOrientation(orientation);
  const routing = routingFromEnvironment();
  const pullRequest = pullRequestFromOrientation(orientation);
  const { concerns, prompts } = reviewPrompts(repositoryRoot);
  const concernRouting = concernRoutingFromEnvironment({
    concerns: concerns.map(({ name }) => name),
    proposer: routing.proposer,
  });
  process.stdout.write(JSON.stringify({
    target,
    head,
    guidance,
    routing,
    concernRouting,
    ...(pullRequest === undefined ? {} : { pullRequest }),
    concerns,
    prompts,
    contracts: reviewContracts(concerns.map(({ name }) => name)),
  }) + "\n");
} catch (error) {
  // Every input failure is a usage error: the lifecycle reads exit 2 as
  // "the input could not be built", never as a crash to diagnose.
  process.stderr.write(`${error.message}\n`);
  process.exitCode = 2;
}
