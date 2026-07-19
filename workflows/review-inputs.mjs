#!/usr/bin/env node

import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const target = process.argv[2];
const head = process.argv[3];
if (!target || !head) {
  process.stderr.write("usage: node workflows/review-inputs.mjs TARGET HEAD\n");
  process.exitCode = 2;
} else {
  const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
  const paths = [
    "workflows/review-briefs/exploration.md",
    "workflows/review-briefs/correctness.md",
    "workflows/review-briefs/security.md",
    "workflows/review-briefs/testing.md",
    "workflows/review-briefs/design.md",
    "workflows/review-briefs/repository.md",
    "workflows/review-briefs/verifier.md",
  ];
  const instructionBriefs = paths.map((path) => ({
    path,
    readPath: resolve(repositoryRoot, path),
    content: readFileSync(resolve(repositoryRoot, path), "utf8"),
  }));
  process.stdout.write(JSON.stringify({ target, head, instructionBriefs }) + "\n");
}
