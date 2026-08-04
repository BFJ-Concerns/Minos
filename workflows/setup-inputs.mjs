#!/usr/bin/env node

import { readFileSync, statSync } from "node:fs";
import { dirname, isAbsolute, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const argv = process.argv.slice(2);
const head = argv[0];
const optionArgv = argv.slice(1);
const reconcileOnly = optionArgv.includes("--reconcile-only");
const recognised = new Set(["--reconcile-only", "--objections"]);
let objectionsPath = null;
let invalid = false;
for (let index = 0; index < optionArgv.length; index += 1) {
  const argument = optionArgv[index];
  if (!recognised.has(argument)) {
    invalid = true;
    break;
  }
  if (argument === "--objections") {
    objectionsPath = optionArgv[index + 1] || null;
    index += 1;
    if (!objectionsPath) invalid = true;
  }
}

if (!head || head.startsWith("--") || invalid) {
  process.stderr.write("usage: node workflows/setup-inputs.mjs HEAD [--reconcile-only] [--objections FILE]\n");
  process.exitCode = 2;
} else {
  const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
  const runDir = process.env.MINOS_RUN_DIR;
  const orientationPath = process.env.MINOS_ORIENTATION;
  if (!runDir) throw new Error("MINOS_RUN_DIR is required");
  if (!orientationPath) throw new Error("MINOS_ORIENTATION is required");

  const reconciliationPath = resolve(runDir, "reconciliation.json");
  const reconciliation = JSON.parse(readFileSync(reconciliationPath, "utf8"));
  if (!Array.isArray(reconciliation.conflicts) ||
      !reconciliation.conflicts.every((path) => typeof path === "string" && path !== ""))
    throw new Error("reconciliation state has an invalid conflicts list");

  const orientation = JSON.parse(readFileSync(orientationPath, "utf8"));
  if (!orientation || typeof orientation.guidance !== "string" || orientation.guidance === "")
    throw new Error("orientation omitted its guidance path");
  const guidancePath = resolve(orientation.guidance);
  const guidanceStat = statSync(guidancePath);
  if (!guidanceStat.isFile() || guidanceStat.size > 262_144)
    throw new Error("guidance must be one regular README-sized document");
  const guidanceContent = readFileSync(guidancePath, "utf8");
  if (guidanceContent.trim() === "") throw new Error("guidance document is empty");

  const briefPath = "workflows/setup-briefs/setup-agent.md";
  const briefReadPath = resolve(repositoryRoot, briefPath);
  const workspace = process.env.MINOS_WORKSPACE || orientation.repository;
  if (typeof workspace !== "string" || workspace === "")
    throw new Error("setup input omitted the workspace");

  let objections = null;
  if (objectionsPath) {
    if (!isAbsolute(objectionsPath)) throw new Error("objections path must be absolute");
    objections = JSON.parse(readFileSync(objectionsPath, "utf8"));
    if (!Array.isArray(objections) || !objections.every((entry) =>
      entry && typeof entry.path === "string" && entry.path !== "" &&
      typeof entry.objection === "string" && entry.objection.trim() !== ""))
      throw new Error("objections must be an array of path and objection strings");
  }

  process.stdout.write(`${JSON.stringify({
    mode: reconcileOnly ? "reconcile-only" : "full",
    head,
    workspace,
    conflicts: reconciliation.conflicts,
    preimageDir: reconciliation.preimageDir,
    buildCommand: process.env.MINOS_BUILD_CMD || "",
    testCommand: process.env.MINOS_TEST_CMD || "",
    guidance: {
      grounding: orientation.grounding || "repository",
      path: guidancePath,
      content: guidanceContent,
    },
    setupBrief: {
      path: briefPath,
      readPath: briefReadPath,
      content: readFileSync(briefReadPath, "utf8"),
    },
    objections,
  })}\n`);
}
