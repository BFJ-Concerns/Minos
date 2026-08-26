#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const orientationPath = process.argv[2];
const findingsPath = process.argv[3];
const membersFlag = process.argv[4];
const membersPath = process.argv[5];
if (!orientationPath || !findingsPath || (membersFlag !== undefined && (membersFlag !== "--members" || !membersPath)) || process.argv.length > 6) {
  process.stderr.write("usage: node workflows/publish-overflow.mjs ORIENTATION FINDINGS [--members MEMBERS]\n");
  process.exit(2);
}

const orientation = JSON.parse(readFileSync(orientationPath, "utf8"));
const entries = JSON.parse(readFileSync(findingsPath, "utf8"));
if (!Array.isArray(entries)) throw new Error("findings must be a JSON array");

function normaliseEntry(entry) {
  if (entry.kind === "out-of-scope-observation") {
    return {
      ...entry,
      label: "Out-of-scope observation",
      severity: "out of scope",
      confidence: null,
    };
  }
  if (entry.kind === "review-brief-misconfiguration") {
    return {
      ...entry,
      label: "Review brief misconfiguration",
      path: entry.brief,
      line: 1,
      explanation: entry.reason,
      severity: entry.misconfigurationKind || "misconfiguration",
      confidence: null,
    };
  }
  return {
    ...entry,
    label: entry.kind === "held-diagnosis" ? "Held diagnosis"
      : entry.kind === "fix-attempts-failed" ? "Review finding (automated fixes failed)"
      : "Review finding",
  };
}

const findings = entries.map(normaliseEntry);

function memberGroups() {
  if (!membersPath) {
    if (findings.some((finding) => typeof finding.member === "string" && finding.member !== ""))
      throw new Error("member-attributed overflow requires a members record");
    return [{ member: null, findings }];
  }
  const record = JSON.parse(readFileSync(membersPath, "utf8"));
  const members = Array.isArray(record) ? record : record && record.members;
  if (!Array.isArray(members) || members.length === 0) throw new Error("members record must contain a non-empty members array");
  const byID = new Map();
  for (const member of members) {
    if (!member || typeof member.id !== "string" || member.id === "" ||
        typeof member.owner !== "string" || member.owner === "" ||
        typeof member.repo !== "string" || member.repo === "" || !Number.isInteger(member.number))
      throw new Error("members record has incomplete member coordinates");
    if (byID.has(member.id)) throw new Error(`members record duplicates member ${member.id}`);
    byID.set(member.id, member);
  }
  const grouped = new Map();
  for (const finding of findings) {
    if (typeof finding.member !== "string" || !byID.has(finding.member))
      throw new Error("member-attributed overflow has an absent or unknown member");
    const member = byID.get(finding.member);
    const group = grouped.get(member.id) || { member, findings: [] };
    group.findings.push(finding);
    grouped.set(member.id, group);
  }
  return [...grouped.values()];
}

const groups = memberGroups();

function key(finding) {
  const title = String(finding.title || "").trim().toLowerCase().replace(/\s+/g, " ");
  const identity = finding.kind === "out-of-scope-observation" || finding.kind === "review-brief-misconfiguration"
    ? [finding.kind, finding.path, finding.line, title]
    : [finding.path, finding.line, title];
  return Buffer.from(JSON.stringify(finding.member ? [finding.member, ...identity] : identity)).toString("base64url");
}

function inlineComment(finding) {
  return {
    path: finding.path,
    body: `**${finding.label}: ${finding.title}**\n\n${finding.explanation}\n\nSeverity: ${finding.severity}.${finding.confidence === null ? "" : ` Confidence: ${finding.confidence}.`}`,
    line: finding.line,
  };
}

if (orientation.grounding === "repository") {
  if (membersPath) {
    process.stdout.write(JSON.stringify({
      destination: "pull-request",
      publications: groups.map(({ member, findings: memberFindings }) => ({
        member: member.id,
        body: "Additional review material for project triage.",
        comments: memberFindings.map(inlineComment),
      })),
    }) + "\n");
    process.exit(0);
  }
  process.stdout.write(JSON.stringify({
    destination: "pull-request",
    body: "Additional review material for project triage.",
    comments: findings.map(inlineComment),
  }) + "\n");
  process.exit(0);
}

if (orientation.grounding !== "annexe") throw new Error("orientation has unknown grounding mode");

if (typeof orientation.annexe !== "string" || orientation.annexe === "")
  throw new Error("annexe grounding omitted the annexe path");
const source = orientation.source;
if (!source || [source.owner, source.repo, source.pr, source.date].some((value) => typeof value !== "string" || value === ""))
  throw new Error("annexe grounding omitted its source attribution");
const issuesPath = join(orientation.annexe, "ISSUES.md");
let contents = existsSync(issuesPath) ? readFileSync(issuesPath, "utf8") : "# Issues\n";
if (!contents.endsWith("\n")) contents += "\n";
let written = 0;
for (const group of groups) {
  const memberSource = group.member
    ? { owner: group.member.owner, repo: group.member.repo, pr: String(group.member.number), date: source.date }
    : source;
  const attribution = `Filed by Minos from ${memberSource.owner}/${memberSource.repo}#${memberSource.pr}, ${memberSource.date}`;
  for (const finding of group.findings) {
    const marker = `<!-- review-finding:${key(finding)} -->`;
    if (contents.includes(marker)) continue;
    const location = finding.kind === "review-brief-misconfiguration"
      ? finding.path
      : `${finding.path}:${finding.line}`;
    contents += `\n- ${finding.label}: ${finding.title} (${finding.severity}, ${location}) — ${finding.explanation}. ${attribution}. ${marker}\n`;
    written++;
  }
}

if (written === 0) {
  process.stdout.write(JSON.stringify({ destination: "annexe", written: 0, pushed: false }) + "\n");
  process.exit(0);
}

writeFileSync(issuesPath, contents);
// stdout carries only the JSON envelope; git's own output goes to stderr.
const gitStdio = ["ignore", process.stderr, "inherit"];
execFileSync("git", ["-C", orientation.annexe, "add", "ISSUES.md"], { stdio: gitStdio });
execFileSync("git", ["-C", orientation.annexe, "commit", "-m", "chore: record review findings for triage"], { stdio: gitStdio });
execFileSync("git", ["-C", orientation.annexe, "push", "origin", "HEAD"], { stdio: gitStdio });
process.stdout.write(JSON.stringify({ destination: "annexe", written, pushed: true }) + "\n");
