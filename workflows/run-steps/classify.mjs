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

import { readFileSync } from "node:fs";

import { earlierMinosFindings, reviewText } from "../earlier-minos-findings.mjs";
import { filedReviewBodies, readDefaultBranchFile } from "../filing-destination.mjs";
import { decisionFrom, stage as verdictClassification } from "../judgements/verdict-classification.mjs";
import { guidanceFromOrientation } from "../orientation-guidance.mjs";
import { pullRequestFromOrientation } from "../pull-request-description.mjs";
import { descriptionFromOrientation } from "../repository-description.mjs";
import { DECISION_KIND, findingKey, verdictDigest } from "../verdict-classification.mjs";
import { currentSnapshot } from "./forge-writes.mjs";

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

// Supply the advice the file or pull-request filing path already holds.
export async function filedEntries(run, destination, snapshot) {
  if (destination.kind === "none") return "This repository files nothing, so no entries are on file.";
  if (destination.kind === "pull-request-comment") {
    try {
      const text = filedReviewBodies(snapshot).map(reviewText).join("\n\n");
      return text || "Minos has filed no entries on this pull request.";
    } catch {
      return "The pull-request filing comments could not be read in this run.";
    }
  }
  if (destination.kind === "file") {
    const repository = destination.repository || `${run.pullRequest.owner}/${run.pullRequest.repository}`;
    try {
      const text = await readDefaultBranchFile(run, repository, destination.path);
      if (text === null) return `${destination.path} does not exist on the default branch of ${repository}.`;
      return text.trim() === "" ? `${destination.path} is empty.` : text;
    } catch {
      return `The filing file ${destination.path} on the default branch of ${repository} could not be read in this run.`;
    }
  }
  return `The filing destination (${destination.kind} on ${destination.repository}) could not be read in this run.`;
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

  const snapshot = await currentSnapshot(run);
  const threads = await run.forge("threads");
  const orientation = JSON.parse(readFileSync(run.orientation, "utf8"));
  const pullRequest = pullRequestFromOrientation(orientation);
  const repositoryDescription = descriptionFromOrientation(orientation);
  const input = {
    target: run.target,
    head: run.head,
    guidance: guidanceFromOrientation(orientation),
    ...(pullRequest === undefined ? {} : { pullRequest }),
    ...(repositoryDescription === undefined ? {} : { repositoryDescription }),
    threshold,
    findings,
    earlierFindings: earlierMinosFindings(snapshot, threads).map(({ path, line, head, finding }) => ({ path, line, head, finding })),
    filedEntries: await filedEntries(run, filingDestination(run.env), snapshot),
  };

  const answer = await run.judge(verdictClassification, input);
  return { decisions: Object.fromEntries(groups.map((group) => [group, decisionFrom(answer[group])])) };
}
