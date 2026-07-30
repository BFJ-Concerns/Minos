#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { pathToFileURL } from "node:url";

const TEST_DIRECTORIES = new Set(["test", "tests", "spec", "specs", "__tests__", "testdata"]);

export function isTestPath(path) {
  const parts = String(path).split("/").filter(Boolean);
  const basename = parts.at(-1) || "";
  return parts.some((part) => TEST_DIRECTORIES.has(part)) ||
    /[._-](test|spec)\.[^.]+$/i.test(basename) ||
    /_test\.[^.]+$/i.test(basename);
}

export function classifyPaths(paths) {
  if (!Array.isArray(paths) || paths.length === 0)
    return { classification: "empty", paths: [] };
  return {
    classification: paths.every(isTestPath) ? "tests-only" : "review-required",
    paths,
  };
}

function changedPaths(workspace, before, after) {
  for (const revision of [before, after])
    execFileSync("git", ["-C", workspace, "cat-file", "-e", `${revision}^{commit}`]);
  const output = execFileSync(
    "git",
    ["-C", workspace, "diff", "--name-only", "-z", before, after],
    { encoding: "buffer" },
  );
  return output.toString("utf8").split("\0").filter(Boolean);
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  const [workspace, before, after] = process.argv.slice(2);
  if (!workspace || !before || !after) {
    process.stderr.write("usage: classify-finishing-change.mjs WORKSPACE BEFORE AFTER\n");
    process.exit(2);
  }
  process.stdout.write(`${JSON.stringify(classifyPaths(changedPaths(workspace, before, after)))}\n`);
}
