// Files verified advisory findings and brief configuration diagnostics to
// the reviewed project's annexe. Unavailable filing stays in the run record
// without changing the verdict or generating pull-request comments.

import { execFileSync } from "node:child_process";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { basename, join } from "node:path";

import { issueLogEntry } from "./finding-presentation.mjs";

// The service's commit identity: the same bot the sweep signs the failure
// ledger with, so an annexe's history names one author for everything
// Minos wrote there.
const AUTHOR_NAME = "Minos";
const AUTHOR_EMAIL = "minos@example.invalid";
const ISSUE_LOG = "ISSUES.md";
const PUSH_ATTEMPTS = 2;

// One entry is filed once: a repeat run over a later head that raises the
// same defect at the same site finds its marker already in the log and
// skips it, so the log never fills with one defect per push.
export function issueLogMarker(entry) {
  const title = String(entry.title || "").trim().toLowerCase().replace(/\s+/g, " ");
  const identity = entry.kind === "review-brief-misconfiguration"
    ? [entry.kind, entry.brief, title]
    : [entry.kind, entry.path, entry.line, title];
  return `<!-- minos:${Buffer.from(JSON.stringify(identity)).toString("base64url")} -->`;
}

// Appends the entries missing from the log's text; returns the new text and
// how many were added.
export function appendIssueLogEntries(contents, entries, attribution) {
  let text = contents.length === 0 ? "# Issues\n" : contents;
  if (!text.endsWith("\n")) text += "\n";
  let written = 0;
  for (const entry of entries) {
    const marker = issueLogMarker(entry);
    if (text.includes(marker)) continue;
    text += `\n${issueLogEntry(entry, attribution)} ${marker}\n`;
    written += 1;
  }
  return { text, written };
}

function gitEnvironment(credentialFile) {
  const env = { ...process.env, GIT_TERMINAL_PROMPT: "0" };
  if (credentialFile) {
    const token = readFileSync(credentialFile, "utf8").split("\n")[0].trim();
    if (token === "") throw new Error("forge credential file is empty");
    env.GIT_CONFIG_COUNT = "1";
    env.GIT_CONFIG_KEY_0 = "http.extraHeader";
    env.GIT_CONFIG_VALUE_0 = `Authorization: token ${token}`;
  }
  return env;
}

function git(annexe, env, ...args) {
  return execFileSync("git", ["-C", annexe, ...args], { env, stdio: ["ignore", "pipe", "pipe"], encoding: "utf8", maxBuffer: Infinity });
}

function describe(error) {
  const stderr = error && typeof error.stderr === "string" ? error.stderr.trim() : "";
  return stderr !== "" ? stderr.split("\n").pop() : String(error && error.message ? error.message : error);
}

// Files `entries` into the annexe named by the run's orientation record.
// `credentialFile` holds the forge token that authenticates the push; a
// path-local remote (the tests' bare repositories) needs none.
export function fileIssueLogEntries({ orientation, entries, credentialFile = null }) {
  entries = entries.filter((entry) => entry.kind !== "out-of-scope-observation");
  if (entries.length === 0) return { destination: "none", written: 0 };
  if (!orientation || orientation.grounding !== "annexe")
    return { destination: "unfiled", reason: "the project has no annexe" };
  const annexe = orientation.annexe;
  const source = orientation.source || {};
  if (typeof annexe !== "string" || annexe === "" || !existsSync(join(annexe, ".git")))
    return { destination: "unfiled", reason: "the annexe clone is missing from the run" };
  if ([source.owner, source.repo, source.pr, source.date].some((value) => typeof value !== "string" || value === ""))
    return { destination: "unfiled", reason: "the orientation record carries no source attribution" };
  const attribution = `Filed by Minos from ${source.owner}/${source.repo}#${source.pr}, ${source.date}`;
  const location = `${basename(annexe)}/${ISSUE_LOG}`;
  const logPath = join(annexe, ISSUE_LOG);

  let env;
  try {
    env = gitEnvironment(credentialFile);
  } catch (error) {
    return { destination: "unfiled", reason: describe(error) };
  }

  let lastFailure = "";
  for (let attempt = 1; attempt <= PUSH_ATTEMPTS; attempt += 1) {
    try {
      const branch = git(annexe, env, "rev-parse", "--abbrev-ref", "HEAD").trim();
      // The clone is the run's own and nothing else writes to it, so the
      // freshest remote state is simply taken before appending: an entry is
      // appended to what the annexe says now, not to what it said at setup.
      git(annexe, env, "fetch", "--quiet", "origin", branch);
      git(annexe, env, "reset", "--quiet", "--hard", `origin/${branch}`);
      const contents = existsSync(logPath) ? readFileSync(logPath, "utf8") : "";
      const { text, written } = appendIssueLogEntries(contents, entries, attribution);
      if (written === 0) return { destination: "annexe", written: 0, location };
      writeFileSync(logPath, text);
      git(annexe, env, "add", "--", ISSUE_LOG);
      git(annexe, env, "-c", `user.name=${AUTHOR_NAME}`, "-c", `user.email=${AUTHOR_EMAIL}`,
        "commit", "--quiet", "--only", "-m", "issues: record review material for triage", "--", ISSUE_LOG);
      git(annexe, env, "push", "--quiet", "origin", `HEAD:${branch}`);
      return { destination: "annexe", written, location };
    } catch (error) {
      lastFailure = describe(error);
    }
  }
  return { destination: "unfiled", reason: `the annexe push failed: ${lastFailure}` };
}
