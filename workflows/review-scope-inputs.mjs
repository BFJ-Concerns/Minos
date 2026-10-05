#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { guidanceFromOrientation } from "./orientation-guidance.mjs";
import { routingFromEnvironment } from "./role-routing.mjs";
import { reviewBriefsFromWorkspace } from "./review-brief-files.mjs";
import { briefEngagement } from "./brief-dispositions.mjs";

const [target, head, ...rest] = process.argv.slice(2);
const occasion = rest.length === 1 ? rest[0] : null;
const argumentError = !target || !head || rest.length > 1;

if (argumentError) {
  process.stderr.write("usage: node workflows/review-scope-inputs.mjs TARGET HEAD [OCCASION]\n");
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

  const git = (...args) => execFileSync("git", ["-C", workspace, ...args], {
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
    maxBuffer: Infinity,
  });
  const changedPaths = git("diff", "--no-renames", "--name-only", "-z", target, head).split("\0").filter(Boolean);
  const lineCounts = new Map();
  for (const line of git("diff", "--no-renames", "--numstat", "-z", target, head).split("\0").filter(Boolean)) {
    const match = line.match(/^(\d+|-)\t(\d+|-)\t([\s\S]+)$/);
    if (!match) throw new Error("unparseable git numstat record");
    lineCounts.set(match[3], {
      added: match[1] === "-" ? 0 : Number(match[1]),
      deleted: match[2] === "-" ? 0 : Number(match[2]),
    });
  }
  const changedFiles = changedPaths.map((path) => {
    const counts = lineCounts.get(path);
    if (!counts) throw new Error(`git numstat omitted counts for ${path}`);
    return { path, ...counts };
  });

  const engagement = briefEngagement(briefs, occasion, changedPaths);

  const scopeBriefPath = "workflows/review-briefs/scope.md";
  const workflowInput = {
    target,
    head,
    occasion,
    guidance,
    routing,
    instructionBriefs: [{
      path: scopeBriefPath,
      readPath: resolve(workflowRoot, scopeBriefPath),
      content: readFileSync(resolve(workflowRoot, scopeBriefPath), "utf8"),
    }],
    changedFiles,
    hasReviewDirectory,
    briefsEngage: engagement.briefsEngage,
    briefDispositions: engagement.dispositions,
    briefMisconfigurations: engagement.misconfigurations,
  };

  process.stdout.write(JSON.stringify(workflowInput) + "\n");
} catch (error) {
  // Every input failure is a usage error: the lifecycle reads exit 2 as
  // "the input could not be built", never as a crash to diagnose.
  process.stderr.write(`${error.message}\n`);
  process.exitCode = 2;
}
