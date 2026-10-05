// Delivers a clean run's confirmed non-gating material — verified advisory
// findings and brief configuration diagnostics — to the repository's
// configured filing destination: a file in a repository's default
// branch, an issue on a named repository, a comment on the pull request, or
// nowhere. One entry files once, deduplicated by a marker. A failed
// delivery stays in the run record and never falls back to another surface.

import { execFileSync } from "node:child_process";
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { request as httpRequest } from "node:http";
import { request as httpsRequest } from "node:https";
import { join } from "node:path";

import { normalisedTitle } from "./verdict-classification.mjs";
import { issueLogEntry } from "./finding-presentation.mjs";

const PUSH_ATTEMPTS = 2;

export const FILING_KINDS = ["file", "issue", "pull-request-comment", "none"];

// One entry is filed once: a repeat run over a later head that raises the
// same defect at the same site finds its marker already at the destination
// and skips it, so a destination never fills with one defect per push. The
// reviewed repository is part of the identity because one destination may
// serve several repositories, whose sites are only relative paths.
export function filingMarker(entry, reviewedRepository) {
  const title = normalisedTitle(entry);
  const identity = entry.kind === "review-brief-misconfiguration"
    ? [entry.kind, reviewedRepository, entry.brief, title]
    : entry.kind === "guidance-source-misconfiguration"
      ? [entry.kind, reviewedRepository, entry.sourceRepository || null, entry.path, entry.reason]
      : [entry.kind, reviewedRepository, entry.path, entry.line, title];
  return `<!-- minos:${Buffer.from(JSON.stringify(identity)).toString("base64url")} -->`;
}

// Appends the entries missing from a file's text; returns the new text and
// how many were added.
export function appendFilingEntries(contents, entries, attribution, reviewedRepository) {
  let text = contents.length === 0 ? "# Issues\n" : contents;
  if (!text.endsWith("\n")) text += "\n";
  let written = 0;
  for (const entry of entries) {
    const marker = filingMarker(entry, reviewedRepository);
    if (text.includes(marker)) continue;
    text += `\n${issueLogEntry(entry, attribution)} ${marker}\n`;
    written += 1;
  }
  return { text, written };
}

// Mirrors the loader's per-kind rules (internal/shell/config.go): the
// environment is the contract, so a value the loader would refuse is refused
// here too rather than normalised.
export function validFilingDestination(destination) {
  if (!destination || typeof destination !== "object" || !FILING_KINDS.includes(destination.kind)) return false;
  for (const key of Object.keys(destination))
    if (!["kind", "repository", "path"].includes(key)) return false;
  for (const key of ["repository", "path"])
    if (destination[key] !== undefined && (typeof destination[key] !== "string" || destination[key] === "")) return false;
  const repositoryNamed = destination.repository !== undefined;
  if (repositoryNamed && !/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(destination.repository)) return false;
  const pathNamed = destination.path !== undefined;
  if (pathNamed && (destination.path.startsWith("/") || destination.path.split("/").includes(".."))) return false;
  switch (destination.kind) {
    case "file": return pathNamed;
    case "issue": return repositoryNamed && !pathNamed;
    default: return !repositoryNamed && !pathNamed;
  }
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
// run directory (guidance/<owner>/<name>: nested, so names never collide): a
// separate write path, never a guidance clone. When the destination is the
// reviewed repository the clone carries the same pre-push guard as the
// workspace, so a push can never reach the pull-request branch.
function cloneForFiling(runDir, repository, target, env, protection) {
  const directory = join(runDir, "filing", ...repository.split("/"));
  rmSync(directory, { recursive: true, force: true });
  mkdirSync(join(directory, ".."), { recursive: true });
  execFileSync("git", ["clone", "--quiet", "--single-branch", "--branch", target.branch, target.cloneURL, directory],
    { env, stdio: ["ignore", "pipe", "pipe"], encoding: "utf8" });
  if (protection) {
    const commonDir = git(directory, env, "rev-parse", "--path-format=absolute", "--git-common-dir").trim();
    writeFileSync(join(commonDir, "minos-protected-ref"), `refs/heads/${protection.headBranch}\n`, { mode: 0o600 });
    mkdirSync(join(commonDir, "hooks"), { recursive: true });
    copyFileSync(protection.pushGuard, join(commonDir, "hooks", "pre-push"));
  }
  return directory;
}

async function deliverToFile({ destination, entries, attribution, reviewedRepository, apiBase, token, runDir, identity, protection }) {
  const repository = destination.repository || reviewedRepository;
  const location = `${repository}:${destination.path}`;
  let lastFailure = "";
  for (let attempt = 1; attempt <= PUSH_ATTEMPTS; attempt += 1) {
    try {
      const env = gitEnvironment(token);
      const target = await lookupRepository(apiBase, repository, token);
      const reviewed = repository === reviewedRepository;
      if (reviewed && protection && target.branch === protection.headBranch)
        return { kind: "file", outcome: "unfiled", reason: `filing to ${location} refused: the repository's default branch ${target.branch} is the pull-request branch, which Minos never writes` };
      const clone = cloneForFiling(runDir, repository, target, env, reviewed ? protection : null);
      const filePath = join(clone, destination.path);
      const contents = existsSync(filePath) ? readFileSync(filePath, "utf8") : "";
      const { text, written } = appendFilingEntries(contents, entries, attribution, reviewedRepository);
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

// The forge command owns both marker lookup and the durable write. Temporary
// payloads stay under the run directory, separate from any guidance clone.
async function deliverToIssue({ destination, entries, attribution, reviewedRepository, runDir }) {
  const location = destination.repository;
  let written = 0;
  let payloadDir;
  try {
    if (!process.env.MINOS_BIN) throw new Error("MINOS_BIN is required for issue filing");
    mkdirSync(join(runDir, "filing"), { recursive: true });
    payloadDir = mkdtempSync(join(runDir, "filing", "issue-"));
    const titleFile = join(payloadDir, "title");
    const bodyFile = join(payloadDir, "body");
    for (const entry of entries) {
      writeFileSync(titleFile, entry.title, { mode: 0o600 });
      writeFileSync(bodyFile, `${issueLogEntry(entry, attribution)} ${filingMarker(entry, reviewedRepository)}\n`, { mode: 0o600 });
      const result = JSON.parse(execFileSync(process.env.MINOS_BIN,
        ["forge", "file-issue", location, titleFile, bodyFile],
        { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] }));
      if (result.outcome !== "applied" || ![0, 1].includes(result.written))
        throw new Error(result.reason || "issue write was not confirmed");
      written += result.written;
    }
    return { kind: "issue", outcome: "filed", written, location };
  } catch (error) {
    return { kind: "issue", outcome: "unfiled", written, reason: `filing to ${location} failed: ${describe(error)}` };
  } finally {
    if (payloadDir) rmSync(payloadDir, { recursive: true, force: true });
  }
}

// Delivers `entries` to `destination`. `source` attributes the material to
// the run's pull request; `reviewedRepository` is the owner/name the file
// kind writes to when no repository is named; `identity` signs the commit;
// `protection` names the pull-request branch and the pre-push guard script
// that a clone of the reviewed repository must carry. Every outcome carries
// the kind and one of: filed, nothing-to-file, discarded (the none kind),
// unfiled (with the reason).
export async function deliverFilingEntries({
  destination, entries, source, reviewedRepository, apiBase, credentialFile = null, runDir, identity, protection = null,
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
  if (kind === "file") {
    if ((!destination.repository || destination.repository === reviewedRepository) && !protection)
      return { kind, outcome: "unfiled", reason: "filing to the reviewed repository needs the pull-request branch and the push guard (protection)" };
    return deliverToFile({ destination, entries, attribution, reviewedRepository, apiBase, token, runDir, identity, protection });
  }
  if (kind === "issue") return deliverToIssue({ destination, entries, attribution, reviewedRepository, runDir });
  return { kind, outcome: "unfiled", reason: `the ${kind} filing kind is not implemented in this build; the entries stay in the run record` };
}
