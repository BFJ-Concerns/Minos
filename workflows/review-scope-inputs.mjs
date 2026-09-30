#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import { dirname, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

import { guidanceFromOrientation } from "./orientation-guidance.mjs";
import { routingFromEnvironment } from "./role-routing.mjs";

import { briefEngagement } from "./brief-dispositions.mjs";

const [target, head, ...rest] = process.argv.slice(2);
const occasion = rest.length === 1 ? rest[0] : null;
const argumentError = !target || !head || rest.length > 1;

if (argumentError) {
  process.stderr.write("usage: node workflows/review-scope-inputs.mjs TARGET HEAD [OCCASION]\n");
  process.exitCode = 2;
} else {
  const workflowRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
  const orientationPath = process.env.MINOS_ORIENTATION;
  if (!orientationPath) throw new Error("MINOS_ORIENTATION is required");
  const orientation = JSON.parse(readFileSync(orientationPath, "utf8"));
  if (!orientation || typeof orientation.repository !== "string" || orientation.repository === "")
    throw new Error("orientation omitted its repository path");
  const guidance = guidanceFromOrientation(orientation);
  const routing = routingFromEnvironment();

  const workspace = resolve(process.env.MINOS_WORKSPACE || orientation.repository);

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
  const briefs = markdownPaths.map((absolute) => {
    const path = relative(workspace, absolute).split(sep).join("/");
    const inner = path.replace(/^\.review\//, "");
    const scope = inner.includes("/") ? inner.replace(/\/[^/]*$/, "") : null;
    const scopePath = scope ? resolve(workspace, scope) : null;
    return {
      path,
      content: readFileSync(absolute, "utf8"),
      scope,
      scopeExists: !scopePath || (existsSync(scopePath) && statSync(scopePath).isDirectory()),
    };
  });

  const git = (...args) => execFileSync("git", ["-C", workspace, ...args], {
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
    maxBuffer: Infinity,
  });
  const changedPaths = git("diff", "--name-only", "-z", target, head).split("\0").filter(Boolean);
  const lineCounts = new Map();
  for (const line of git("diff", "--numstat", "-z", target, head).split("\0").filter(Boolean)) {
    const match = line.match(/^(\d+|-)\t(\d+|-)\t([\s\S]+)$/);
    if (!match) continue;
    lineCounts.set(match[3], {
      added: match[1] === "-" ? 0 : Number(match[1]),
      deleted: match[2] === "-" ? 0 : Number(match[2]),
    });
  }
  const changedFiles = changedPaths.map((path) => ({
    path,
    added: lineCounts.get(path) ? lineCounts.get(path).added : 0,
    deleted: lineCounts.get(path) ? lineCounts.get(path).deleted : 0,
  }));

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
}
