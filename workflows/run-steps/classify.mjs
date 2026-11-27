// Classifies the confirmed findings: which gate, which take a never-gating
// class, which are advisory. That is a judgement, so this step gathers what
// the verdict-classification stage is given (each verdict's confirmed
// findings with their digest facts, everything Minos posted on this pull
// request before, and what the filing destination already holds), asks the
// stage once for both verdicts, and settles its answer as decisions the
// stage's definition has already held to verdict-classification.mjs's
// validation.
//
// Takes `review` and `briefReview`. Does nothing when no review ran.
// Settles `decisions`: `{ main, brief }`, each a validated verdict
// decision, `brief` absent when the brief stage returned no verdict.

import { execFileSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";

import { earlierMinosFindings } from "../earlier-minos-findings.mjs";
import { decisionFrom, stage as verdictClassification } from "../judgements/verdict-classification.mjs";
import { guidanceFromOrientation } from "../orientation-guidance.mjs";
import { pullRequestFromOrientation } from "../pull-request-description.mjs";
import { DECISION_KIND, findingKey, verdictDigest } from "../verdict-classification.mjs";

// One verdict's confirmed findings as the stage is shown them: the digest's
// facts with the reviewer's and the verifier's words beside them.
export function groupFindings(verdict, threshold) {
  const digest = verdictDigest({ review: verdict, threshold });
  if (digest.status !== "complete") throw new Error(`the verdict cannot be classified: ${digest.reason}`);
  const confirmed = new Map(verdict.confirmedFindings.map((finding) => [findingKey(finding), finding]));
  return digest.findings.map(({ key, severity, atOrAboveThreshold, path, line, title }) => {
    const finding = confirmed.get(key);
    return {
      key, severity, atOrAboveThreshold, path, line, title,
      explanation: String(finding.explanation ?? ""),
      verification: String(finding.verification ?? ""),
      source: String(finding.source ?? ""),
    };
  });
}

// What the filing destination already holds, where this run can read it
// without another clone: a file in the reviewed repository, as the target
// carries it, or a file in a repository setup already cloned as a guidance
// source, as that clone holds it. Anywhere else is said to be unread, and
// the stage classifies without it.
export function filedEntries(run, destination, orientation) {
  if (destination.kind === "none") return "This repository files nothing, so no entries are on file.";
  if (destination.kind === "file" && !destination.repository) {
    try {
      return entriesText(destination.path, execFileSync("git", ["-C", run.workspace, "show", `${run.target}:${destination.path}`], {
        encoding: "utf8",
        stdio: ["ignore", "pipe", "ignore"],
        maxBuffer: Infinity,
      }));
    } catch {
      return `${destination.path} does not exist on the target branch, so no entries are on file.`;
    }
  }
  const clone = destination.kind === "file" && guidanceClone(orientation, destination.repository);
  if (clone) {
    const location = join(clone, destination.path);
    return existsSync(location)
      ? entriesText(destination.path, readFileSync(location, "utf8"))
      : `${destination.path} does not exist in ${destination.repository}, so no entries are on file.`;
  }
  const where = destination.repository ? `${destination.kind} on ${destination.repository}` : destination.kind;
  return `The filing destination (${where}) could not be read in this run.`;
}

function entriesText(path, text) {
  return text.trim() === "" ? `${path} is empty.` : text;
}

// The working tree setup cloned a secondary repository into, found from a
// guidance document it read there: the document's location less its path.
function guidanceClone(orientation, repository) {
  const entry = (orientation.guidance || []).find((guidance) =>
    guidance.source && guidance.source.repository === repository &&
    typeof guidance.location === "string" && guidance.location.endsWith(`/${guidance.source.path}`));
  return entry ? entry.location.slice(0, -(entry.source.path.length + 1)) : null;
}

function filingDestination(env) {
  try {
    return JSON.parse(env.MINOS_FILING_DESTINATION);
  } catch {
    throw new Error("MINOS_FILING_DESTINATION is missing or not valid JSON");
  }
}

// The decision a group with no confirmed findings takes. Nothing is left
// to judge in it: with no finding there is nothing to gate, and the
// verdict is clean by arithmetic.
function emptyDecision() {
  return { kind: DECISION_KIND, verdict: "clean", basis: "The review confirmed no findings.", findings: [] };
}

export async function classify(run, settled) {
  if (settled.review === undefined) return undefined;
  const threshold = run.env.MINOS_REVIEW_THRESHOLD;
  const findings = {
    main: groupFindings(settled.review, threshold),
    ...(settled.briefReview === undefined ? {} : { brief: groupFindings(settled.briefReview, threshold) }),
  };
  const groups = Object.keys(findings);
  if (groups.every((group) => findings[group].length === 0))
    return { decisions: Object.fromEntries(groups.map((group) => [group, emptyDecision()])) };

  const [snapshot, threads] = [await run.forge("snapshot"), await run.forge("threads")];
  const orientation = JSON.parse(readFileSync(run.orientation, "utf8"));
  const pullRequest = pullRequestFromOrientation(orientation);
  const input = {
    target: run.target,
    head: run.head,
    guidance: guidanceFromOrientation(orientation),
    ...(pullRequest === undefined ? {} : { pullRequest }),
    threshold,
    findings,
    earlierFindings: earlierMinosFindings(snapshot, threads).map(({ path, line, head, finding }) => ({ path, line, head, finding })),
    filedEntries: filedEntries(run, filingDestination(run.env), orientation),
  };

  const answer = await run.judge(verdictClassification, input);
  return { decisions: Object.fromEntries(groups.map((group) => [group, decisionFrom(answer[group])])) };
}
