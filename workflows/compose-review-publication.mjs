#!/usr/bin/env node

// The publication composer: the one deterministic step between the run's
// validated verdicts and what leaves the run. It turns the adjudicated
// verdicts and the lead's classification decisions into the exact files
// the guarded forge review command posts, in the order they are posted,
// and the triage material the issue-log filing delivers, so no lead ever
// hand-assembles a payload or re-derives the routing (C6).
//
// The pull request carries what blocks it. Every finding on a review is a
// blocking finding, anchored where it applies; everything else the run
// learned — advisory findings the lead judged non-gating, unverified
// observations, review-brief misconfigurations — is triage material for the
// reviewed project's issue log, filed by file-triage.mjs after the reviews
// have posted, and delivered onto the pull request only where no log can
// take it. Publication order is part of the contract (the forge marks a
// reviewer's latest state-bearing review as official): the main review
// lands first and carries the verdict; a repository-brief group follows as
// its own blocking review only when its own decision gates. One defect
// surfaces once: findings the two groups raise at the same site are merged
// into the main review's, gating if either gates, and never posted twice.
//
// Usage:
//   compose-review-publication.mjs OUTPUT_DIR THRESHOLD MAIN_VERDICT MAIN_DECISION [BRIEF_VERDICT BRIEF_DECISION]
//
// Writes OUTPUT_DIR/publication-plan.json — an ordered list of posts, each
// naming its body file, comments file and the verdict argument for
// `minos forge review`, and the triage entries file — beside the files it
// names. A decision that does not validate against its verdict stops the
// composer with exit 1 and nothing written: nothing leaves the run from an
// unvalidated decision.

import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import { findingComment, reviewBody } from "./finding-presentation.mjs";
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

// A group is one verdict the run holds: the complete adjudicated verdict
// and the lead's validated decision over its confirmed findings.
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
// one finding: same site and same title merge into the main group's
// finding, which gates if either disposition gates, carries the higher
// severity, and names the brief that also raised it.
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

// A group's findings part ways here: the blocking ones ride its review,
// the rest are triage material.
for (const group of [main, brief].filter(Boolean)) {
  group.blocking = group.findings.filter((finding) => group.dispositionOf(finding)?.gating === true);
  group.advisory = group.findings.filter((finding) => group.dispositionOf(finding)?.gating !== true);
  // Distinct blocking findings on one line cross-reference each other by
  // title so the author reads them as neighbours rather than as a pile-up.
  const bySite = new Map();
  for (const finding of group.blocking) {
    const list = bySite.get(site(finding)) || [];
    list.push(finding);
    bySite.set(site(finding), list);
  }
  for (const finding of group.blocking) {
    const neighbours = bySite.get(site(finding)).filter((other) => other !== finding);
    if (neighbours.length > 0) finding.crossReferences = neighbours.map((other) => other.title);
  }
}

const reviewed = main.verdict.reviewed;
mkdirSync(outputDir, { recursive: true });
const plan = [];

function emitReview(fileStem, group, verdictArgument, body) {
  const bodyPath = join(outputDir, `${fileStem}.md`);
  const commentsPath = join(outputDir, `${fileStem}-comments.json`);
  const comments = group.blocking.map((finding) => findingComment(finding, group.dispositionOf(finding)));
  writeFileSync(bodyPath, `${body}\n`);
  writeFileSync(commentsPath, `${JSON.stringify(comments)}\n`);
  plan.push({ review: group.name, verdict: verdictArgument, body: bodyPath, comments: commentsPath });
}

// The triage material, in the order the log will carry it: advisory
// findings first, then unverified observations, then brief
// misconfigurations. The entry kind is the channel's, set after the
// spread: a brief misconfiguration carries its own `kind` (the skip kind)
// and must not masquerade as a channel.
const triageEntries = [];
for (const group of [main, brief].filter(Boolean))
  for (const finding of group.advisory) triageEntries.push({ ...finding, kind: "advisory-finding" });
for (const group of [main, brief].filter(Boolean))
  for (const observation of group.verdict.outOfScopeObservations || [])
    triageEntries.push({ ...observation, kind: "out-of-scope-observation" });
for (const group of [main, brief].filter(Boolean))
  for (const misconfiguration of group.verdict.misconfigurations || [])
    triageEntries.push({ ...misconfiguration, kind: "review-brief-misconfiguration" });
const triage = {
  advisory: triageEntries.filter((entry) => entry.kind === "advisory-finding").length,
  observations: triageEntries.filter((entry) => entry.kind === "out-of-scope-observation").length,
  misconfigurations: triageEntries.filter((entry) => entry.kind === "review-brief-misconfiguration").length,
};

const mainVerdict = main.blocking.length > 0 ? "request-changes" : "clean";
emitReview(
  "main-review",
  main,
  mainVerdict === "request-changes" ? "request-changes" : "approve",
  reviewBody({ verdict: mainVerdict, reviewed, findings: main.blocking, triage }),
);

if (brief && brief.blocking.length > 0) {
  emitReview(
    "brief-review",
    brief,
    "request-changes",
    reviewBody({
      verdict: "request-changes",
      reviewed,
      findings: brief.blocking,
      group: "brief",
      briefsRan: (brief.verdict.ran || []).map((entry) => entry.brief),
    }),
  );
}

const triagePath = join(outputDir, "triage-entries.json");
writeFileSync(triagePath, `${JSON.stringify(triageEntries)}\n`);

// The run's overall verdict: request-changes when any published review
// blocks, clean only when none does — the same rule the lifecycle's
// classification step states, carried here as data for the status write.
const planDocument = {
  kind: "minos-publication-plan-v1",
  verdict: plan.some((post) => post.verdict === "request-changes") ? "request-changes" : "clean",
  head: reviewed.head,
  target: reviewed.target,
  posts: plan,
  triage: { entries: triagePath, ...triage },
};
writeFileSync(join(outputDir, "publication-plan.json"), `${JSON.stringify(planDocument, null, 2)}\n`);
process.stdout.write(`${JSON.stringify(planDocument)}\n`);
