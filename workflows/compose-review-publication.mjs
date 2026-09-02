#!/usr/bin/env node

// The publication composer: the one deterministic step between the run's
// validated verdicts and the forge. It turns the adjudicated verdicts and
// the lead's classification decisions into the exact files the guarded
// forge review command posts, in the order they are posted, so no lead ever
// hand-assembles a payload or re-derives the posting sequence (C6).
//
// Publication order is part of the contract (the forge marks a reviewer's
// latest state-bearing review as official): the main review lands first and
// carries the verdict; a repository-brief group follows as its own review,
// blocking only when its own decision gates and otherwise a plain comment
// review — never an approval, which would outrank the verdict; unverified
// observations come last as a comment review. One defect surfaces as one
// finding: findings the two groups raise at the same site are merged into
// the main review and cross-referenced by title, never posted twice.
//
// Usage:
//   compose-review-publication.mjs OUTPUT_DIR THRESHOLD MAIN_VERDICT MAIN_DECISION [BRIEF_VERDICT BRIEF_DECISION]
//
// Writes OUTPUT_DIR/publication-plan.json — an ordered list of posts, each
// naming its body file, comments file and the verdict argument for
// `minos forge review` — beside the files it names. A decision that does not
// validate against its verdict stops the composer with exit 1 and nothing
// written: nothing reaches the forge from an unvalidated decision.

import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import {
  OBSERVATIONS_BODY,
  findingComment,
  observationComment,
  reviewBody,
} from "./finding-presentation.mjs";
import { findingKey, validateVerdictDecision, verdictDigest } from "./verdict-classification.mjs";

const argv = process.argv.slice(2);
if (argv.length !== 4 && argv.length !== 6) {
  process.stderr.write(
    "usage: node workflows/compose-review-publication.mjs OUTPUT_DIR THRESHOLD MAIN_VERDICT MAIN_DECISION [BRIEF_VERDICT BRIEF_DECISION]\n",
  );
  process.exit(2);
}
const [outputDir, threshold, mainVerdictPath, mainDecisionPath, briefVerdictPath, briefDecisionPath] = argv;

function readJson(path, label) {
  try {
    return JSON.parse(readFileSync(path, "utf8"));
  } catch (error) {
    process.stderr.write(`${label} is missing or not valid JSON: ${path} (${error.message})\n`);
    process.exit(2);
  }
}

// A group is one review the run publishes: its complete verdict and the
// lead's validated decision over that verdict's confirmed findings.
function loadGroup(name, verdictPath, decisionPath) {
  const verdict = readJson(verdictPath, `${name} verdict`);
  const decision = readJson(decisionPath, `${name} decision`);
  const digest = verdictDigest({ review: verdict, threshold });
  if (digest.status !== "complete") {
    process.stderr.write(`${name} verdict cannot be digested: ${digest.reason}\n`);
    process.exit(1);
  }
  const validation = validateVerdictDecision(decision, digest);
  if (!validation.ok) {
    process.stderr.write(`${name} decision does not validate: ${validation.reason}\n`);
    process.exit(1);
  }
  // Dispositions are keyed exactly as the digest keys findings, so a
  // gating call can never fail to find its finding.
  const dispositions = new Map(decision.findings.map((disposition) => [disposition.key, disposition]));
  const dispositionOf = (finding) => dispositions.get(findingKey(finding)) || null;
  return {
    name,
    verdict,
    decision,
    dispositions,
    dispositionOf,
    findings: verdict.confirmedFindings.map((finding) => ({ ...finding })),
  };
}

const main = loadGroup("main", mainVerdictPath, mainDecisionPath);
const brief = briefVerdictPath ? loadGroup("brief", briefVerdictPath, briefDecisionPath) : null;

function normalisedTitle(finding) {
  return String(finding.title || "").trim().toLowerCase().replace(/\s+/g, " ");
}

// Keys are JSON-encoded tuples: a path may contain any character, so a
// delimiter-joined string could make two distinct sites read as one.
function site(finding) {
  return JSON.stringify([finding.path, finding.line]);
}

function siteAndTitle(finding) {
  return JSON.stringify([finding.path, finding.line, normalisedTitle(finding)]);
}

const SEVERITY_RANK = { Low: 1, Medium: 2, High: 3, Critical: 4 };

// Deduplication across the two groups. The same defect raised by both is
// one finding: same site and same title merge into the main review's
// finding, which gates if either disposition gates, carries the higher
// severity, and names the brief that also raised it. Distinct findings on
// one site are cross-referenced by title so the author reads them as
// neighbours rather than as a pile-up.
if (brief) {
  const mainBySiteAndTitle = new Map(main.findings.map((finding) => [siteAndTitle(finding), finding]));
  brief.findings = brief.findings.filter((finding) => {
    const twin = mainBySiteAndTitle.get(siteAndTitle(finding));
    if (!twin) return true;
    if (SEVERITY_RANK[finding.severity] > SEVERITY_RANK[twin.severity]) twin.severity = finding.severity;
    twin.alsoRaisedBy = [...(twin.alsoRaisedBy || []), finding.source];
    if (brief.dispositionOf(finding)?.gating === true)
      main.dispositions.set(findingKey(twin), { ...main.dispositionOf(twin), gating: true });
    return false;
  });
}
for (const group of [main, brief].filter(Boolean)) {
  const bySite = new Map();
  for (const finding of group.findings) {
    const list = bySite.get(site(finding)) || [];
    list.push(finding);
    bySite.set(site(finding), list);
  }
  for (const finding of group.findings) {
    const neighbours = bySite.get(site(finding)).filter((other) => other !== finding);
    if (neighbours.length > 0) finding.crossReferences = neighbours.map((other) => other.title);
  }
}

// The verdict a group's review carries. The main review is the verdict the
// classification earned. A brief group blocks only on its own gating
// decision; a merged finding that gated has already moved to the main
// review, so a brief group left with no gating finding of its own is a
// comment review.
function groupVerdict(group) {
  const gates = group.findings.some((finding) => group.dispositionOf(finding)?.gating === true);
  if (group.name === "main") return gates ? "request-changes" : "clean";
  return gates ? "request-changes" : "advisory";
}

const reviewed = main.verdict.reviewed;
mkdirSync(outputDir, { recursive: true });
const plan = [];

function emitReview(fileStem, group, verdictArgument, body) {
  const bodyPath = join(outputDir, `${fileStem}.md`);
  const commentsPath = join(outputDir, `${fileStem}-comments.json`);
  const comments = group.findings.map((finding) => findingComment(finding, group.dispositionOf(finding)));
  writeFileSync(bodyPath, `${body}\n`);
  writeFileSync(commentsPath, `${JSON.stringify(comments)}\n`);
  plan.push({ review: group.name, verdict: verdictArgument, body: bodyPath, comments: commentsPath });
}

const mainVerdict = groupVerdict(main);
emitReview(
  "main-review",
  main,
  mainVerdict === "request-changes" ? "request-changes" : "approve",
  reviewBody({ verdict: mainVerdict, reviewed, findings: main.findings, dispositionOf: main.dispositionOf }),
);

if (brief && brief.findings.length > 0) {
  const briefVerdict = groupVerdict(brief);
  emitReview(
    "brief-review",
    brief,
    briefVerdict === "request-changes" ? "request-changes" : "comment",
    reviewBody({
      verdict: briefVerdict === "request-changes" ? "request-changes" : "clean",
      reviewed,
      findings: brief.findings,
      dispositionOf: brief.dispositionOf,
      group: "brief",
      briefsRan: (brief.verdict.ran || []).map((entry) => entry.brief),
    }),
  );
}

const entries = [];
for (const group of [main, brief].filter(Boolean)) {
  // The entry kind is the channel's, set after the spread: a brief
  // misconfiguration carries its own `kind` (the skip kind) and must not
  // masquerade as a channel.
  for (const observation of group.verdict.outOfScopeObservations || [])
    entries.push({ ...observation, kind: "out-of-scope-observation" });
  for (const misconfiguration of group.verdict.misconfigurations || [])
    entries.push({ ...misconfiguration, kind: "review-brief-misconfiguration" });
}
if (entries.length > 0) {
  const bodyPath = join(outputDir, "observations.md");
  const commentsPath = join(outputDir, "observations-comments.json");
  writeFileSync(bodyPath, `${OBSERVATIONS_BODY}\n`);
  writeFileSync(commentsPath, `${JSON.stringify(entries.map(observationComment))}\n`);
  plan.push({ review: "observations", verdict: "comment", body: bodyPath, comments: commentsPath });
}

// The run's overall verdict: request-changes when any published review
// blocks, clean only when none does — the same rule the lifecycle's
// classification step states, carried here as data for the status write.
const planDocument = {
  kind: "minos-publication-plan-v1",
  verdict: plan.some((post) => post.verdict === "request-changes") ? "request-changes" : "clean",
  head: reviewed.head,
  target: reviewed.target,
  posts: plan,
};
writeFileSync(join(outputDir, "publication-plan.json"), `${JSON.stringify(planDocument, null, 2)}\n`);
process.stdout.write(`${JSON.stringify(planDocument)}\n`);
