#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { existsSync, readFileSync, statSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const target = process.argv[2];
const head = process.argv[3];
let recordPath;
let recordContent;
let membersPath;
let membersContent;
let argumentError = null;
for (let index = 4; index < process.argv.length; index += 1) {
  const argument = process.argv[index];
  if (argument === "--loop-record") {
    if (recordPath !== undefined) argumentError = "review loop record was supplied more than once";
    const value = process.argv[index + 1];
    if (!value || value.startsWith("--")) argumentError = "--loop-record requires a file";
    else {
      recordPath = value;
      index += 1;
    }
  } else if (argument === "--members") {
    if (membersPath !== undefined) argumentError = "review members were supplied more than once";
    const value = process.argv[index + 1];
    if (!value || value.startsWith("--")) argumentError = "--members requires a file";
    else {
      membersPath = value;
      index += 1;
    }
  } else argumentError = `unexpected review input argument ${argument}`;
}
if (!argumentError && recordPath !== undefined && existsSync(recordPath)) {
  try {
    recordContent = readFileSync(recordPath, "utf8");
  } catch {
    argumentError = `review loop record is unreadable: ${recordPath}`;
  }
}
if (!argumentError && membersPath !== undefined) {
  try {
    membersContent = readFileSync(membersPath, "utf8");
  } catch {
    argumentError = `review members are unreadable: ${membersPath}`;
  }
}
if (!argumentError && membersContent !== undefined) {
  try {
    const record = JSON.parse(membersContent);
    const entries = Array.isArray(record) ? record : record && Array.isArray(record.members) ? record.members : null;
    if (!entries || entries.length === 0 || entries.some((member) =>
      !member || typeof member !== "object" || Array.isArray(member) ||
      typeof member.id !== "string" || member.id === "" ||
      typeof member.owner !== "string" || member.owner === "" ||
      typeof member.repo !== "string" || member.repo === "" ||
      !Number.isInteger(member.number) || member.number < 1
    )) argumentError = "review members need non-empty ids and forge coordinates";
  } catch {
    argumentError = "review members are not JSON";
  }
}
if (!target || !head || argumentError) {
  if (argumentError) process.stderr.write(`${argumentError}\n`);
  process.stderr.write("usage: node workflows/review-inputs.mjs TARGET HEAD [--loop-record FILE] [--members FILE]\n");
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
  let priorFindings = { confirmedFixed: [], confirmedUnfixed: [] };
  if (recordContent !== undefined) {
    try {
      const record = JSON.parse(recordContent);
      priorFindings = record && typeof record === "object" && !Array.isArray(record)
        ? {
          confirmedFixed: record.confirmedFixed === undefined ? [] : record.confirmedFixed,
          confirmedUnfixed: record.confirmedUnfixed,
        }
        : null;
    } catch {
      priorFindings = null;
    }
  }
  let members;
  if (membersContent !== undefined) {
    const record = JSON.parse(membersContent);
    const entries = Array.isArray(record) ? record : record.members;
    const minosBin = process.env.MINOS_BIN;
    if (!minosBin) throw new Error("MINOS_BIN is required for grouped review members");
    members = entries.map((member) => {
      const snapshot = JSON.parse(execFileSync(
        minosBin,
        ["forge", "--member", member.owner, member.repo, String(member.number), "snapshot"],
        { encoding: "utf8" },
      ));
      if (typeof snapshot.head_sha !== "string" || snapshot.head_sha === "" ||
          typeof snapshot.target_sha !== "string" || snapshot.target_sha === "")
        throw new Error(`member ${member.id} snapshot is unusable`);
      const diff = execFileSync(
        "git", ["-C", orientation.repository || process.cwd(), "diff", "--no-ext-diff", "--no-color", snapshot.target_sha, snapshot.head_sha],
        { encoding: "utf8" },
      );
      return { ...member, head: snapshot.head_sha, target: snapshot.target_sha, diff };
    });
  }
  const primaryNumber = Number(process.env.MINOS_PR);
  process.stdout.write(JSON.stringify({
    target,
    head,
    guidance,
    ...(pullRequest === undefined ? {} : { pullRequest }),
    instructionBriefs,
    priorFindings,
    ...(membersContent === undefined
      ? { members: [{
        id: "primary",
        owner: process.env.MINOS_OWNER,
        repo: process.env.MINOS_REPO_NAME,
        number: Number.isInteger(primaryNumber) ? primaryNumber : null,
        target,
        head,
        ...(pullRequest === undefined ? {} : pullRequest),
      }] }
      : { members }),
  }) + "\n");
}
