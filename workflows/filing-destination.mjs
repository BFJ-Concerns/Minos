// Delivers a clean run's confirmed non-gating material — verified advisory
// findings and brief configuration diagnostics — to the repository's
// configured filing destination (C45): a file in a repository's default
// branch, an issue on a named repository, a comment on the pull request, or
// nowhere. One entry files once, deduplicated by a marker. A failed
// delivery stays in the run record and never falls back to another surface.

import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { request as httpRequest } from "node:http";
import { request as httpsRequest } from "node:https";
import { join } from "node:path";

import { issueLogEntry } from "./finding-presentation.mjs";

const PUSH_ATTEMPTS = 2;

export const FILING_KINDS = ["file", "issue", "pull-request-comment", "none"];

// One entry is filed once: a repeat run over a later head that raises the
// same defect at the same site finds its marker already at the destination
// and skips it, so a destination never fills with one defect per push.
export function filingMarker(entry) {
  const title = String(entry.title || "").trim().toLowerCase().replace(/\s+/g, " ");
  const identity = entry.kind === "review-brief-misconfiguration"
    ? [entry.kind, entry.brief, title]
    : [entry.kind, entry.path, entry.line, title];
  return `<!-- minos:${Buffer.from(JSON.stringify(identity)).toString("base64url")} -->`;
}

// Appends the entries missing from a file's text; returns the new text and
// how many were added.
export function appendFilingEntries(contents, entries, attribution) {
  let text = contents.length === 0 ? "# Issues\n" : contents;
  if (!text.endsWith("\n")) text += "\n";
  let written = 0;
  for (const entry of entries) {
    const marker = filingMarker(entry);
    if (text.includes(marker)) continue;
    text += `\n${issueLogEntry(entry, attribution)} ${marker}\n`;
    written += 1;
  }
  return { text, written };
}

export function validFilingDestination(destination) {
  if (!destination || typeof destination !== "object" || !FILING_KINDS.includes(destination.kind)) return false;
  for (const key of ["repository", "path"])
    if (destination[key] !== undefined && (typeof destination[key] !== "string" || destination[key] === "")) return false;
  if (destination.kind === "file" && !destination.path) return false;
  if (destination.kind === "issue" && !destination.repository) return false;
  return true;
}

function readToken(credentialFile) {
  const token = readFileSync(credentialFile, "utf8").split("\n")[0].trim();
  if (token === "") throw new Error("forge credential file is empty");
  return token;
}

function gitEnvironment(token) {
  const env = { ...process.env, GIT_TERMINAL_PROMPT: "0" };
  if (token) {
    env.GIT_CONFIG_COUNT = "1";
    env.GIT_CONFIG_KEY_0 = "http.extraHeader";
    env.GIT_CONFIG_VALUE_0 = `Authorization: token ${token}`;
  }
  return env;
}

function git(directory, env, ...args) {
  return execFileSync("git", ["-C", directory, ...args], { env, stdio: ["ignore", "pipe", "pipe"], encoding: "utf8", maxBuffer: Infinity });
}

function describe(error) {
  const stderr = error && typeof error.stderr === "string" ? error.stderr.trim() : "";
  return stderr !== "" ? stderr.split("\n").pop() : String(error && error.message ? error.message : error);
}

// The forge names a repository's clone URL and default branch; deriving
// either from a naming convention is what the configured destination exists
// to replace. One plain request, no connection reuse: the filing runs once
// per run and holds nothing open afterwards.
function lookupRepository(apiBase, repository, token) {
  if (!apiBase) return Promise.reject(new Error("MINOS_API_BASE is required to locate the filing repository"));
  const url = new URL(`${apiBase.replace(/\/$/, "")}/api/v1/repos/${repository}`);
  const headers = { Accept: "application/json" };
  if (token) headers.Authorization = `token ${token}`;
  const request = url.protocol === "https:" ? httpsRequest : httpRequest;
  return new Promise((resolve, reject) => {
    const outgoing = request(url, { method: "GET", headers, agent: false }, (response) => {
      const chunks = [];
      response.on("data", (chunk) => chunks.push(chunk));
      response.on("error", reject);
      response.on("end", () => {
        if (response.statusCode !== 200) {
          reject(new Error(`repository lookup for ${repository} returned HTTP ${response.statusCode}`));
          return;
        }
        let metadata;
        try {
          metadata = JSON.parse(Buffer.concat(chunks).toString("utf8"));
        } catch {
          reject(new Error(`repository metadata for ${repository} is not JSON`));
          return;
        }
        if (typeof metadata.clone_url !== "string" || metadata.clone_url === "" ||
            typeof metadata.default_branch !== "string" || metadata.default_branch === "") {
          reject(new Error(`repository metadata for ${repository} omitted its clone URL or default branch`));
          return;
        }
        resolve({ cloneURL: metadata.clone_url, branch: metadata.default_branch });
      });
    });
    outgoing.on("error", (error) => reject(new Error(`repository lookup for ${repository} failed: ${error.message}`)));
    outgoing.end();
  });
}

// A fresh single-branch clone of the destination's default branch under the
// run directory: a separate write path, never a guidance clone.
function cloneForFiling(runDir, repository, target, env) {
  const directory = join(runDir, "filing", repository.replace("/", "--"));
  rmSync(directory, { recursive: true, force: true });
  mkdirSync(join(runDir, "filing"), { recursive: true });
  execFileSync("git", ["clone", "--quiet", "--single-branch", "--branch", target.branch, target.cloneURL, directory],
    { env, stdio: ["ignore", "pipe", "pipe"], encoding: "utf8" });
  return directory;
}

async function deliverToFile({ destination, entries, attribution, reviewedRepository, apiBase, token, runDir, identity }) {
  const repository = destination.repository || reviewedRepository;
  const location = `${repository}:${destination.path}`;
  let lastFailure = "";
  for (let attempt = 1; attempt <= PUSH_ATTEMPTS; attempt += 1) {
    try {
      const env = gitEnvironment(token);
      const target = await lookupRepository(apiBase, repository, token);
      const clone = cloneForFiling(runDir, repository, target, env);
      const filePath = join(clone, destination.path);
      const contents = existsSync(filePath) ? readFileSync(filePath, "utf8") : "";
      const { text, written } = appendFilingEntries(contents, entries, attribution);
      if (written === 0) return { kind: "file", outcome: "filed", written: 0, location };
      mkdirSync(join(filePath, ".."), { recursive: true });
      writeFileSync(filePath, text);
      git(clone, env, "add", "--", destination.path);
      git(clone, env, "-c", `user.name=${identity.name}`, "-c", `user.email=${identity.email}`,
        "commit", "--quiet", "--only", "-m", "issues: record review material for triage", "--", destination.path);
      git(clone, env, "push", "--quiet", "origin", `HEAD:${target.branch}`);
      return { kind: "file", outcome: "filed", written, location };
    } catch (error) {
      lastFailure = describe(error);
    }
  }
  return { kind: "file", outcome: "unfiled", reason: `filing to ${location} failed: ${lastFailure}` };
}

// Delivers `entries` to `destination`. `source` attributes the material to
// the run's pull request; `reviewedRepository` is the owner/name the file
// kind writes to when no repository is named; `identity` signs the commit.
// Every outcome carries the kind and one of: filed, nothing-to-file,
// discarded (the none kind), unfiled (with the reason).
export async function deliverFilingEntries({
  destination, entries, source, reviewedRepository, apiBase, credentialFile = null, runDir, identity,
}) {
  if (!validFilingDestination(destination)) throw new Error("filing destination is malformed");
  entries = entries.filter((entry) => entry.kind !== "out-of-scope-observation");
  const kind = destination.kind;
  if (entries.length === 0) return { kind, outcome: "nothing-to-file", written: 0 };
  if (kind === "none") return { kind, outcome: "discarded", written: 0 };
  if ([source && source.owner, source && source.repo, source && source.pr, source && source.date]
    .some((value) => typeof value !== "string" || value === ""))
    return { kind, outcome: "unfiled", reason: "the orientation record carries no source attribution" };
  const attribution = `Filed by ${identity.name} from ${source.owner}/${source.repo}#${source.pr}, ${source.date}`;
  let token = null;
  if (credentialFile) {
    try {
      token = readToken(credentialFile);
    } catch (error) {
      return { kind, outcome: "unfiled", reason: describe(error) };
    }
  }
  if (kind === "file")
    return deliverToFile({ destination, entries, attribution, reviewedRepository, apiBase, token, runDir, identity });
  return { kind, outcome: "unfiled", reason: `the ${kind} filing kind is not implemented in this build; the entries stay in the run record` };
}
