#!/usr/bin/env node

import { existsSync, readFileSync, statSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { prepareFixWave } from "./fix-wave-plan.mjs";

const usage = "usage: node workflows/fix-inputs.mjs REVIEW_RESULT [RUN_RECORD] [--single-wave] [--grouping FILE]\n";
const positionals = [];
let singleWave = false;
let groupingPath = null;
let groupingRequested = false;
let argumentError = null;
for (let index = 2; index < process.argv.length; index += 1) {
  const value = process.argv[index];
  if (value === "--single-wave") singleWave = true;
  else if (value === "--grouping") {
    groupingRequested = true;
    const candidate = process.argv[index + 1];
    if (candidate && !candidate.startsWith("--")) {
      groupingPath = candidate;
      index += 1;
    } else {
      argumentError = "fix input option --grouping requires a file";
      break;
    }
  } else if (value.startsWith("--")) {
    argumentError = `unknown fix input option ${value}`;
    break;
  } else positionals.push(value);
}
let grouping;
if (!argumentError && groupingRequested) {
  let groupingContent;
  try {
    groupingContent = readFileSync(groupingPath, "utf8");
  } catch {
    argumentError = `fix grouping file is unreadable: ${groupingPath}`;
  }
  if (!argumentError) {
    try {
      grouping = JSON.parse(groupingContent);
    } catch {
      grouping = null;
    }
  }
}
const [reviewPath, recordPath] = positionals;
if (argumentError || !reviewPath) {
  if (argumentError) process.stderr.write(`${argumentError}\n`);
  process.stderr.write(usage);
  process.exitCode = 2;
} else {
  const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
  const orientationPath = process.env.MINOS_ORIENTATION;
  if (!orientationPath) throw new Error("MINOS_ORIENTATION is required");
  const orientation = JSON.parse(readFileSync(orientationPath, "utf8"));
  if (!orientation.guidance) throw new Error("orientation omitted its guidance path");
  const guidancePath = resolve(orientation.guidance);
  const guidanceStat = statSync(guidancePath);
  if (!guidanceStat.isFile() || guidanceStat.size > 262_144)
    throw new Error("guidance must be one regular README-sized document");
  const guidanceContent = readFileSync(guidancePath, "utf8");
  if (guidanceContent.trim() === "") throw new Error("guidance document is empty");
  const briefPath = "workflows/review-briefs/fixer.md";
  const maximumRounds = process.env.MINOS_MAX_ROUNDS
    ? Number.parseInt(process.env.MINOS_MAX_ROUNDS, 10)
    : null;
  const runRecord = recordPath && existsSync(recordPath)
    ? JSON.parse(readFileSync(recordPath, "utf8"))
    : { round: 0, confirmedUnfixed: [] };
  const result = {
    review: JSON.parse(readFileSync(reviewPath, "utf8")),
    threshold: process.env.MINOS_REVIEW_THRESHOLD || "High",
    maximumRounds,
    runRecord,
    singleWave,
    ...(groupingRequested ? { grouping } : {}),
    workspace: process.env.MINOS_WORKSPACE || orientation.repository,
    verification: {
      build: process.env.MINOS_BUILD_CMD || "",
      tests: process.env.MINOS_TEST_CMD || "",
    },
    guidance: {
      grounding: orientation.grounding || "repository",
      path: guidancePath,
      content: guidanceContent,
    },
    fixerBrief: {
      path: briefPath,
      readPath: resolve(repositoryRoot, briefPath),
      content: readFileSync(resolve(repositoryRoot, briefPath), "utf8"),
    },
  };
  process.stdout.write(JSON.stringify(singleWave ? prepareFixWave(result) : result) + "\n");
}
