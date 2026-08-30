#!/usr/bin/env node

import { readFileSync, statSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const target = process.argv[2];
const head = process.argv[3];
if (!target || !head || process.argv.length > 4) {
  process.stderr.write("usage: node workflows/review-inputs.mjs TARGET HEAD\n");
  process.exitCode = 2;
} else {
  const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
  const orientationPath = process.env.MINOS_ORIENTATION;
  if (!orientationPath) throw new Error("MINOS_ORIENTATION is required");
  const orientation = JSON.parse(readFileSync(orientationPath, "utf8"));
  if (!orientation || typeof orientation.guidance !== "string" || orientation.guidance === "")
    throw new Error("orientation omitted its guidance path");
  const guidancePath = resolve(orientation.guidance);
  const guidanceStat = statSync(guidancePath);
  const maximumGuidanceBytes = 262_144;
  if (!guidanceStat.isFile()) throw new Error("guidance path is not a regular file");
  if (guidanceStat.size > maximumGuidanceBytes)
    throw new Error(`guidance exceeds ${maximumGuidanceBytes} bytes`);
  const guidanceContent = readFileSync(guidancePath, "utf8");
  if (guidanceContent.trim() === "") throw new Error("guidance document is empty");
  const guidance = {
    grounding: typeof orientation.grounding === "string" ? orientation.grounding : "repository",
    path: guidancePath,
    content: guidanceContent,
  };
  let pullRequest;
  if (orientation.pullRequest !== undefined) {
    if (typeof orientation.pullRequest !== "string" || orientation.pullRequest === "")
      throw new Error("orientation's pull-request record path is unusable");
    let record;
    try {
      record = JSON.parse(readFileSync(resolve(orientation.pullRequest), "utf8"));
    } catch {
      throw new Error("orientation names a missing or non-JSON pull-request record");
    }
    if (!record || typeof record !== "object" || Array.isArray(record) ||
        typeof record.title !== "string" || typeof record.body !== "string")
      throw new Error("pull-request record needs string title and body fields");
    const maximumBodyCharacters = 65_536;
    const body = record.body.length > maximumBodyCharacters
      ? `${record.body.slice(0, maximumBodyCharacters)}\n[pull-request description truncated]`
      : record.body;
    if (record.title.trim() !== "" || body.trim() !== "")
      pullRequest = { title: record.title, body };
  }
  const paths = [
    "workflows/review-briefs/exploration.md",
    "workflows/review-briefs/correctness.md",
    "workflows/review-briefs/security.md",
    "workflows/review-briefs/testing.md",
    "workflows/review-briefs/design.md",
    "workflows/review-briefs/verifier.md",
  ];
  const instructionBriefs = paths.map((path) => ({
    path,
    readPath: resolve(repositoryRoot, path),
    content: readFileSync(resolve(repositoryRoot, path), "utf8"),
  }));
  process.stdout.write(JSON.stringify({
    target,
    head,
    guidance,
    ...(pullRequest === undefined ? {} : { pullRequest }),
    instructionBriefs,
  }) + "\n");
}
