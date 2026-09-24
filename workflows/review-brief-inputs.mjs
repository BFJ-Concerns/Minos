#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { existsSync, lstatSync, readFileSync, readdirSync, statSync } from "node:fs";
import { dirname, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

const [target, head, ...rest] = process.argv.slice(2);
const occasion = rest.length === 1 ? rest[0] : null;
const argumentError = !target || !head || rest.length > 1;

if (argumentError) {
  process.stderr.write("usage: node workflows/review-brief-inputs.mjs TARGET HEAD [OCCASION]\n");
  process.exitCode = 2;
} else {
  const workflowRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
  const orientationPath = process.env.MINOS_ORIENTATION;
  if (!orientationPath) throw new Error("MINOS_ORIENTATION is required");
  const orientation = JSON.parse(readFileSync(orientationPath, "utf8"));
  if (!orientation || typeof orientation.repository !== "string" || orientation.repository === "")
    throw new Error("orientation omitted its repository path");
  if (typeof orientation.guidance !== "string" || orientation.guidance === "")
    throw new Error("orientation omitted its guidance path");

  const workspace = resolve(process.env.MINOS_WORKSPACE || orientation.repository);
  const guidancePath = resolve(orientation.guidance);
  const guidanceStat = statSync(guidancePath);
  if (!guidanceStat.isFile() || guidanceStat.size > 262_144)
    throw new Error("guidance must be one regular README-sized document");
  const guidanceContent = readFileSync(guidancePath, "utf8");
  if (guidanceContent.trim() === "") throw new Error("guidance document is empty");

  const reviewDirectory = resolve(workspace, ".review");
  const hasReviewDirectory = existsSync(reviewDirectory) && statSync(reviewDirectory).isDirectory();
  const markdownPaths = [];
  if (hasReviewDirectory) {
    const visit = (directory) => {
      for (const entry of readdirSync(directory, { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name))) {
        const path = resolve(directory, entry.name);
        if (entry.isDirectory()) visit(path);
        else if (entry.isFile() && entry.name.endsWith(".md")) markdownPaths.push(path);
      }
    };
    visit(reviewDirectory);
  }

  const gitLines = (...args) => execFileSync("git", ["-C", workspace, ...args], {
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
    maxBuffer: Infinity,
  }).split("\0").filter(Boolean);
  const changedPaths = gitLines("diff", "--name-only", "-z", target, head);
  const trackedPaths = gitLines("ls-files", "-z");
  const trackedFiles = [];
  for (const path of trackedPaths) {
    const absolute = resolve(workspace, path);
    if (!absolute.startsWith(workspace + sep) || !existsSync(absolute) || !lstatSync(absolute).isFile()) continue;
    const content = readFileSync(absolute);
    trackedFiles.push({ path, bytes: content.includes(0) ? 0 : content.byteLength });
  }

  const briefs = markdownPaths.map((absolute) => {
    const path = relative(workspace, absolute).split(sep).join("/");
    const inner = path.replace(/^\.review\//, "");
    const scope = inner.includes("/") ? inner.replace(/\/[^/]*$/, "") : null;
    const scopePath = scope ? resolve(workspace, scope) : null;
    return {
      path,
      readPath: absolute,
      content: readFileSync(absolute, "utf8"),
      scope,
      scopeExists: !scopePath || (existsSync(scopePath) && statSync(scopePath).isDirectory()),
    };
  });

  const instructionPaths = [
    "workflows/review-briefs/repository.md",
    "workflows/review-briefs/verifier.md",
  ];
  const instructionBriefs = instructionPaths.map((path) => ({
    path,
    readPath: resolve(workflowRoot, path),
    content: readFileSync(resolve(workflowRoot, path), "utf8"),
  }));

  const workflowInput = {
    target,
    head,
    occasion,
    workspace,
    hasReviewDirectory,
    changedPaths,
    trackedFiles,
    briefs,
    guidance: {
      grounding: typeof orientation.grounding === "string" ? orientation.grounding : "repository",
      path: guidancePath,
      content: guidanceContent,
    },
    instructionBriefs,
  };

  process.stdout.write(JSON.stringify(workflowInput) + "\n");
}
