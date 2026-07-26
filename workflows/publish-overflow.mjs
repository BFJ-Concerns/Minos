#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const orientationPath = process.argv[2];
const findingsPath = process.argv[3];
if (!orientationPath || !findingsPath) {
  process.stderr.write("usage: node workflows/publish-overflow.mjs ORIENTATION FINDINGS\n");
  process.exit(2);
}

const orientation = JSON.parse(readFileSync(orientationPath, "utf8"));
const findings = JSON.parse(readFileSync(findingsPath, "utf8"));
if (!Array.isArray(findings)) throw new Error("findings must be a JSON array");

function key(finding) {
  const title = String(finding.title || "").trim().toLowerCase().replace(/\s+/g, " ");
  return Buffer.from(JSON.stringify([finding.path, finding.line, title])).toString("base64url");
}

function inlineComment(finding) {
  return {
    path: finding.path,
    body: `**${finding.title}**\n\n${finding.explanation}\n\nSeverity: ${finding.severity}. Confidence: ${finding.confidence}.`,
    line: finding.line,
  };
}

if (orientation.grounding === "repository") {
  process.stdout.write(JSON.stringify({
    destination: "pull-request",
    body: "Additional code findings for project triage.",
    comments: findings.map(inlineComment),
  }) + "\n");
  process.exit(0);
}

if (orientation.grounding !== "annexe") throw new Error("orientation has unknown grounding mode");

if (typeof orientation.annexe !== "string" || orientation.annexe === "")
  throw new Error("annexe grounding omitted the annexe path");
const issuesPath = join(orientation.annexe, "ISSUES.md");
let contents = existsSync(issuesPath) ? readFileSync(issuesPath, "utf8") : "# Issues\n";
if (!contents.endsWith("\n")) contents += "\n";
let written = 0;
for (const finding of findings) {
  const marker = `<!-- review-finding:${key(finding)} -->`;
  if (contents.includes(marker)) continue;
  contents += `\n- Review finding: ${finding.title} (${finding.severity}, ${finding.path}:${finding.line}) — ${finding.explanation} ${marker}\n`;
  written++;
}

if (written === 0) {
  process.stdout.write(JSON.stringify({ destination: "annexe", written: 0, pushed: false }) + "\n");
  process.exit(0);
}

writeFileSync(issuesPath, contents);
execFileSync("git", ["-C", orientation.annexe, "add", "ISSUES.md"], { stdio: "inherit" });
execFileSync("git", ["-C", orientation.annexe, "commit", "-m", "chore: record review findings for triage"], { stdio: "inherit" });
execFileSync("git", ["-C", orientation.annexe, "push", "origin", "HEAD"], { stdio: "inherit" });
process.stdout.write(JSON.stringify({ destination: "annexe", written, pushed: true }) + "\n");
