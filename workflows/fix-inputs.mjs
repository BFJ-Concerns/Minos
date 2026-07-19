#!/usr/bin/env node

import { existsSync, readFileSync, statSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const reviewPath = process.argv[2];
const recordPath = process.argv[3];
const singleWave = process.argv.includes("--single-wave");
if (!reviewPath) {
  process.stderr.write("usage: node workflows/fix-inputs.mjs REVIEW_RESULT [RUN_RECORD] [--single-wave]\n");
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
    clusterCap: Number.parseInt(process.env.MINOS_FIX_CLUSTER_CAP || "5", 10),
    maximumRounds,
    runRecord,
    singleWave,
    workspace: process.env.MINOS_WORKSPACE || orientation.repository,
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
  process.stdout.write(JSON.stringify(result) + "\n");
}
