// Delivers a run's non-gating material — verified advisory findings,
// safeguard requests where the repository files them, unverified observations from outside the change, findings a verifier
// could neither confirm nor refute, and configuration
// diagnostics — to the repository's configured filing destination: a file
// in a repository's default branch, an issue on a named repository, a
// comment on the pull request, or nowhere. One entry files once,
// deduplicated by a marker. A failed delivery stays in the run record and
// never falls back to another surface.

import { execFile, execFileSync } from "node:child_process";
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { promisify } from "node:util";

import { normalisedTitle } from "./verdict-classification.mjs";
import { issueLogEntry } from "./finding-presentation.mjs";

const PUSH_ATTEMPTS = 2;
const execFileAsync = promisify(execFile);

export const FILING_KINDS = ["file", "issue", "pull-request-comment", "none"];

// Entry kinds nobody confirmed.
const UNVERIFIED_KINDS = ["out-of-scope-observation", "unsettled-finding"];

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

// One entry as every destination kind writes it: the issue-log text the
// project's triage reads, followed by the marker that keeps it from filing
// twice.
export function markedFilingEntry(entry, attribution, reviewedRepository) {
  return `${issueLogEntry(entry, attribution)} ${filingMarker(entry, reviewedRepository)}`;
}

// The heading safeguard requests sit under, apart from the defects, wherever
// a destination gathers several entries in one text.
export const SAFEGUARD_REQUESTS_HEADING = "## Safeguard requests";

const isSafeguardRequest = (entry) => entry.kind === "safeguard-request";

// Appends `entries` as list items to a Markdown text, one blank line apart.
function appendItems(text, items) {
  if (items.length === 0) return text;
  return `${text.replace(/\n+$/, "")}\n${items.map((item) => `\n${item}\n`).join("")}`;
}

// Adds the entries missing from a file's text; returns the new text and how
// many were added. Safeguard requests go at the end of the section under
// SAFEGUARD_REQUESTS_HEADING, which is opened at the end of the file the
// first time one is filed; every other entry goes before that section, so
// a defect never lands under it. The section ends at the next heading of
// its level or above, which a project may add after it.
export function appendFilingEntries(contents, entries, attribution, reviewedRepository) {
  let text = contents.length === 0 ? "# Issues\n" : contents;
  if (!text.endsWith("\n")) text += "\n";
  const markers = new Set();
  const fresh = entries.filter((entry) => {
    const marker = filingMarker(entry, reviewedRepository);
    if (text.includes(marker) || markers.has(marker)) return false;
    markers.add(marker);
    return true;
  });
  if (fresh.length === 0) return { text, written: 0 };
  const items = (wanted) => fresh.filter((entry) => isSafeguardRequest(entry) === wanted)
    .map((entry) => markedFilingEntry(entry, attribution, reviewedRepository));
  const defects = items(false);
  const requests = items(true);

  const lines = text.split("\n");
  const heading = lines.indexOf(SAFEGUARD_REQUESTS_HEADING);
  const offset = (index) => lines.slice(0, index).reduce((length, line) => length + line.length + 1, 0);
  let before = text;
  let section = "";
  let after = "";
  if (heading !== -1) {
    const end = lines.findIndex((line, index) => index > heading && /^#{1,2} /.test(line));
    before = text.slice(0, offset(heading));
    section = end === -1 ? text.slice(offset(heading)) : text.slice(offset(heading), offset(end));
    after = end === -1 ? "" : text.slice(offset(end));
  } else if (requests.length > 0) {
    section = `${SAFEGUARD_REQUESTS_HEADING}\n`;
  }
  const parts = [appendItems(before, defects)];
  if (section !== "") parts.push(appendItems(section, requests));
  if (after !== "") parts.push(after);
  return {
    text: parts.map((part, index) => (index < parts.length - 1 ? `${part.replace(/\n+$/, "")}\n` : part)).join("\n"),
    written: fresh.length,
  };
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

// The forge's adaptation answers for the forge: the run's minos binary is
// asked asynchronously, because a forge answering from this process's own
// loop (a test's fixture) must not be blocked while it is asked.
async function askForge(...args) {
  if (!process.env.MINOS_BIN) throw new Error("MINOS_BIN is required to reach the forge");
  const { stdout } = await execFileAsync(process.env.MINOS_BIN, ["forge", ...args], { encoding: "utf8", maxBuffer: 1 << 20 });
  return stdout;
}

const askForgeJSON = async (...args) => JSON.parse(await askForge(...args));

// The forge command's structured answer on a failed exit (its stdout), or
// null: the reason a reader acts on is worded here from that outcome, never
// passed through from the subprocess's error text.
function forgeOutcome(error) {
  try {
    const outcome = error?.result ?? JSON.parse(String(error?.stdout || ""));
    return outcome && typeof outcome === "object" ? outcome : null;
  } catch {
    return null;
  }
}

class EmptyCredential extends Error {}

// The HTTPS git credential the forge's adaptation derives from its own
// credential for the filing repository. No error built here quotes it.
async function cloneCredential(repository, forge) {
  if (!forge && !process.env.MINOS_BIN) throw new Error("MINOS_BIN is required to reach the forge");
  let credential;
  try {
    credential = await (forge ?? askForgeJSON)("clone-credential", ...repository.split("/"));
  } catch (error) {
    const outcome = forgeOutcome(error);
    if (outcome && outcome.refusal === "empty-credential") throw new EmptyCredential("forge credential file is empty");
    throw new Error("the forge supplied no clone credential");
  }
  if (!credential || typeof credential.username !== "string" || credential.username === "" ||
      typeof credential.password !== "string" || credential.password === "")
    throw new Error("the forge's clone credential is not a credential object");
  return credential;
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
// to replace.
async function lookupRepository(repository, forge = askForgeJSON) {
  const [owner, name] = repository.split("/");
  let metadata;
  try {
    metadata = await forge("repository-metadata", owner, name);
  } catch (error) {
    const outcome = forgeOutcome(error);
    if (outcome && Number.isInteger(outcome.lookup_status))
      throw new Error(`repository lookup for ${repository} returned HTTP ${outcome.lookup_status}`);
    throw new Error(`repository lookup for ${repository} failed: the forge could not be asked`);
  }
  if (!metadata || typeof metadata.clone_url !== "string" || metadata.clone_url === "" ||
      typeof metadata.default_branch !== "string" || metadata.default_branch === "")
    throw new Error(`repository metadata for ${repository} omitted its clone URL or default branch`);
  return { cloneURL: metadata.clone_url, branch: metadata.default_branch };
}

function gitAuthEnvironment(directory, env, credential) {
  const config = join(directory, "gitconfig");
  const authorisation = Buffer.from(`${credential.username}:${credential.password}`).toString("base64");
  writeFileSync(config, `[http]\n\textraHeader = Authorization: Basic ${authorisation}\n`, { mode: 0o600 });
  return {
    ...env, GIT_TERMINAL_PROMPT: "0",
    GIT_CONFIG_COUNT: "1", GIT_CONFIG_KEY_0: "include.path", GIT_CONFIG_VALUE_0: config,
  };
}

// Read the same branch as the file delivery uses, including when the
// reviewed pull request targets a different branch or comes from a fork.
export async function readDefaultBranchFile(run, repository, path) {
  const target = await lookupRepository(repository, run.forge);
  const credential = await cloneCredential(repository, run.forge);
  const directory = mkdtempSync(join(run.runDir, "filing-read-"));
  try {
    const env = gitAuthEnvironment(directory, run.env, credential);
    const clone = join(directory, "repository");
    await execFileAsync("git", ["clone", "--quiet", "--depth=1", "--single-branch", "--no-checkout",
      "--branch", target.branch, "--", target.cloneURL, clone], { env });
    if (!git(clone, env, "--literal-pathspecs", "ls-tree", "-z", "HEAD", "--", path)) return null;
    return git(clone, env, "cat-file", "blob", `HEAD:${path}`);
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
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

async function deliverToFile({ destination, entries, attribution, reviewedRepository, runDir, identity, protection }) {
  const repository = destination.repository || reviewedRepository;
  const location = `${repository}:${destination.path}`;
  let lastFailure = "";
  let credential;
  try {
    credential = await cloneCredential(repository);
  } catch (error) {
    if (error instanceof EmptyCredential) return { kind: "file", outcome: "unfiled", reason: error.message };
    return { kind: "file", outcome: "unfiled", reason: `filing to ${location} failed: ${error.message}` };
  }
  let authDirectory;
  try {
    mkdirSync(runDir, { recursive: true });
    authDirectory = mkdtempSync(join(runDir, "filing-auth-"));
    const env = gitAuthEnvironment(authDirectory, process.env, credential);
    for (let attempt = 1; attempt <= PUSH_ATTEMPTS; attempt += 1) {
      try {
        const target = await lookupRepository(repository);
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
  } catch (error) {
    return { kind: "file", outcome: "unfiled", reason: `filing to ${location} failed: ${describe(error)}` };
  } finally {
    if (authDirectory) rmSync(authDirectory, { recursive: true, force: true });
  }
}

// A safeguard request's issue says what it is in its title, so a reader of
// the issue list tells it from a defect without opening it.
export function issueTitle(entry) {
  return entry.kind === "safeguard-request" ? `Safeguard request: ${entry.title}` : entry.title;
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
      writeFileSync(titleFile, issueTitle(entry), { mode: 0o600 });
      writeFileSync(bodyFile, `${markedFilingEntry(entry, attribution, reviewedRepository)}\n`, { mode: 0o600 });
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

export function filedReviewBodies(snapshot) {
  if (!snapshot || typeof snapshot.authenticated_user !== "string" || snapshot.authenticated_user === "" ||
      !Array.isArray(snapshot.reviews) || snapshot.reviews.some((review) =>
        !review || typeof review.user !== "string" || typeof review.body !== "string"))
    throw new Error("forge snapshot omitted the service identity or readable reviews");
  return snapshot.reviews.filter((review) =>
    review.user === snapshot.authenticated_user && /<!-- minos:[A-Za-z0-9_-]+ -->/.test(review.body))
    .map((review) => review.body);
}

// Reviews are this destination's marker store. Read every service review,
// including older heads, so the same entry is filed once across pushes.
async function deliverToPullRequestComment({ entries, attribution, reviewedRepository, runDir }) {
  const kind = "pull-request-comment";
  let payloadDir;
  try {
    const binary = process.env.MINOS_BIN;
    if (!binary) throw new Error("MINOS_BIN is required for pull-request-comment filing");
    const head = process.env.MINOS_HEAD_SHA;
    const target = process.env.MINOS_TARGET_SHA;
    for (const [name, value] of [["MINOS_HEAD_SHA", head], ["MINOS_TARGET_SHA", target]]) {
      if (typeof value !== "string" || !/^(?:[a-fA-F0-9]{40}|[a-fA-F0-9]{64})$/.test(value))
        throw new Error(`${name} must contain a full commit SHA for pull-request-comment filing`);
    }
    // The snapshot carries every conversation comment, so its size has no
    // bound of its own.
    const command = (...args) => JSON.parse(execFileSync(binary, ["forge", ...args],
      { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"], maxBuffer: Infinity }));
    const bodies = filedReviewBodies(command("snapshot"));
    const markers = new Set();
    const remaining = entries.filter((entry) => {
      const marker = filingMarker(entry, reviewedRepository);
      if (markers.has(marker) || bodies.some((body) => body.includes(marker))) return false;
      markers.add(marker);
      return true;
    });
    if (remaining.length === 0) return { kind, outcome: "nothing-to-file", written: 0 };
    mkdirSync(join(runDir, "filing"), { recursive: true });
    payloadDir = mkdtempSync(join(runDir, "filing", "pull-request-comment-"));
    const bodyFile = join(payloadDir, "body.md");
    const listed = (wanted) => remaining.filter((entry) => isSafeguardRequest(entry) === wanted)
      .map((entry) => `${markedFilingEntry(entry, attribution, reviewedRepository)}\n`).join("\n");
    const defects = listed(false);
    const requests = listed(true);
    writeFileSync(bodyFile, [defects, requests === "" ? "" : `${SAFEGUARD_REQUESTS_HEADING}\n\n${requests}`]
      .filter((part) => part !== "").join("\n"), { mode: 0o600 });
    const result = command("review", head, target, "comment", bodyFile);
    if (!result || result.outcome !== "applied") throw new Error(result?.reason || "comment review write was not confirmed");
    return { kind, outcome: "filed", written: remaining.length };
  } catch (error) {
    return { kind, outcome: "unfiled", reason: `pull-request-comment filing failed: ${describe(error)}` };
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
  destination, entries, source, reviewedRepository, runDir, identity, protection = null,
}) {
  if (!validFilingDestination(destination)) throw new Error("filing destination is malformed");
  const kind = destination.kind;
  // Unverified material, an out-of-scope observation or an unsettled
  // finding, never reaches the pull request: for a pull-request-comment
  // destination it stays in the run record.
  if (kind === "pull-request-comment")
    entries = entries.filter((entry) => !UNVERIFIED_KINDS.includes(entry.kind));
  if (entries.length === 0) return { kind, outcome: "nothing-to-file", written: 0 };
  if (kind === "none") return { kind, outcome: "discarded", written: 0 };
  if ([source && source.owner, source && source.repo, source && source.pr, source && source.date]
    .some((value) => typeof value !== "string" || value === ""))
    return { kind, outcome: "unfiled", reason: "the orientation record carries no source attribution" };
  const attribution = `Filed by ${identity.name} from ${source.owner}/${source.repo}#${source.pr}, ${source.date}`;
  if (kind === "file") {
    if ((!destination.repository || destination.repository === reviewedRepository) && !protection)
      return { kind, outcome: "unfiled", reason: "filing to the reviewed repository needs the pull-request branch and the push guard (protection)" };
    return deliverToFile({ destination, entries, attribution, reviewedRepository, runDir, identity, protection });
  }
  if (kind === "issue") return deliverToIssue({ destination, entries, attribution, reviewedRepository, runDir });
  if (kind === "pull-request-comment") return deliverToPullRequestComment({ entries, attribution, reviewedRepository, runDir });
}
