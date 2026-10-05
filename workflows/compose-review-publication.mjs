#!/usr/bin/env node

// The publication composer: the one deterministic step between the run's
// validated verdicts and what leaves the run. It turns the adjudicated
// verdicts and the lead's classification decisions into the exact files
// the guarded forge review command posts, in the order they are posted,
// and the triage material the issue-log filing delivers, so no lead ever
// hand-assembles a payload or re-derives the routing.
//
// A request-changes review carries all confirmed findings, including advisory
// ones, so the author can address them in the same round. A clean result
// writes no reviews and routes confirmed findings to the configured filing
// destination instead.
// Unverified observations stay in the run record.
//
// Usage:
//   compose-review-publication.mjs OUTPUT_DIR ORIENTATION THRESHOLD MAIN_VERDICT MAIN_DECISION [BRIEF_VERDICT BRIEF_DECISION]
//
// Writes OUTPUT_DIR/publication-plan.json — an ordered list of posts, each
// naming its body file, comments file and the verdict argument for
// `minos forge review`, and the triage entries file — beside the files it
// names. ORIENTATION is setup's workspace record: each configured guidance
// source it could not read becomes a configuration diagnostic for the filing
// destination on any published outcome, never a finding. A decision that does not validate against its verdict stops the
// composer with exit 1 and nothing written: nothing leaves the run from an
// unvalidated decision.

import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import { findingComment, reviewBody } from "./finding-presentation.mjs";
import { SEVERITY, findingKey, normalisedTitle, validateVerdictDecision, verdictDigest } from "./verdict-classification.mjs";

const argv = process.argv.slice(2);
if (argv.length !== 5 && argv.length !== 7) {
  process.stderr.write(
    "usage: node workflows/compose-review-publication.mjs OUTPUT_DIR ORIENTATION THRESHOLD MAIN_VERDICT MAIN_DECISION [BRIEF_VERDICT BRIEF_DECISION]\n",
  );
  process.exit(2);
}
const [outputDir, orientationPath, threshold, mainVerdictPath, mainDecisionPath, briefVerdictPath, briefDecisionPath] = argv;

function readJson(path, label, failureExit = 2) {
  try {
    return JSON.parse(readFileSync(path, "utf8"));
  } catch (error) {
    process.stderr.write(`${label} is missing or not valid JSON: ${path} (${error.message})\n`);
    process.exit(failureExit);
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
    verdict,
    dispositions,
    dispositionOf,
    findings: verdict.confirmedFindings.map((finding) => ({ ...finding })),
  };
}

// Setup's orientation names the workspace path under `repository`; the
// reviewed repository's forge identity is its `source` owner and name.
function readOrientation(path) {
  const orientation = readJson(path, "orientation", 1);
  const misconfigurations = orientation?.misconfigurations ?? [];
  const malformed = !Array.isArray(misconfigurations) || misconfigurations.some((entry) =>
    entry?.kind === "guidance-source" && (typeof entry.source?.path !== "string" || typeof entry.reason !== "string"));
  const { owner, repo } = orientation?.source ?? {};
  if (malformed || (misconfigurations.length > 0 && (typeof owner !== "string" || typeof repo !== "string"))) {
    process.stderr.write(`orientation misconfigurations are malformed: ${path}\n`);
    process.exit(1);
  }
  return { repository: `${owner}/${repo}`, misconfigurations };
}
const orientation = readOrientation(orientationPath);
const guidanceMisconfigurations = orientation.misconfigurations;

const main = loadGroup("main", mainVerdictPath, mainDecisionPath);
const brief = briefVerdictPath ? loadGroup("brief", briefVerdictPath, briefDecisionPath) : null;

// Keys are JSON-encoded tuples: a path may contain any character, so a
// delimiter-joined string could make two distinct sites read as one.
function site(finding) {
  return JSON.stringify([finding.path, finding.line]);
}

function siteAndTitle(finding) {
  return JSON.stringify([finding.path, finding.line, normalisedTitle(finding)]);
}

// Deduplication across the two groups. The same defect raised by both is
// one finding: same site and same title merge into the main group's
// finding, which gates if either disposition gates, carries the higher
// severity, and names the brief that also raised it.
if (brief) {
  const mainBySiteAndTitle = new Map(main.findings.map((finding) => [siteAndTitle(finding), finding]));
  brief.findings = brief.findings.filter((finding) => {
    const twin = mainBySiteAndTitle.get(siteAndTitle(finding));
    if (!twin) return true;
    if (SEVERITY[finding.severity] > SEVERITY[twin.severity]) twin.severity = finding.severity;
    twin.alsoRaisedBy = [...(twin.alsoRaisedBy || []), finding.source];
    if (brief.dispositionOf(finding)?.gating === true)
      main.dispositions.set(findingKey(twin), { ...main.dispositionOf(twin), gating: true });
    return false;
  });
}

const groups = [main, brief].filter(Boolean);
const findings = groups.flatMap((group) => group.findings);
const dispositionOf = (finding) => groups.find((group) => group.findings.includes(finding)).dispositionOf(finding);
const blocking = findings.filter((finding) => dispositionOf(finding)?.gating === true);
const verdict = blocking.length > 0 ? "request-changes" : "clean";

// Distinct findings at one site name their neighbours; duplicates were
// merged above before either publication surface is composed.
const bySite = new Map();
for (const finding of findings) {
  const neighbours = bySite.get(site(finding)) || [];
  neighbours.push(finding);
  bySite.set(site(finding), neighbours);
}
for (const finding of findings) {
  const neighbours = bySite.get(site(finding)).filter((other) => other !== finding);
  if (neighbours.length > 0) finding.crossReferences = neighbours.map((other) => other.title);
}

const reviewed = main.verdict.reviewed;
mkdirSync(outputDir, { recursive: true });
const posts = [];
if (verdict === "request-changes") {
  const body = join(outputDir, "main-review.md");
  const comments = join(outputDir, "main-review-comments.json");
  writeFileSync(body, `${reviewBody({
    reviewed, findings: blocking, advisory: findings.length - blocking.length,
    briefsRan: (brief?.verdict.ran || []).map((entry) => entry.brief),
  })}\n`);
  writeFileSync(comments, `${JSON.stringify(findings.map((finding) => findingComment(finding, dispositionOf(finding))))}\n`);
  posts.push({ review: "main", verdict, body, comments });
}

const triageEntries = verdict === "clean"
  ? findings.map((finding) => ({ ...finding, kind: "advisory-finding" }))
  : [];
for (const group of groups)
  for (const misconfiguration of group.verdict.misconfigurations || [])
    triageEntries.push({ ...misconfiguration, kind: "review-brief-misconfiguration" });
for (const misconfiguration of guidanceMisconfigurations)
  if (misconfiguration.kind === "guidance-source")
    triageEntries.push({
      kind: "guidance-source-misconfiguration",
      repository: orientation.repository,
      sourceRepository: misconfiguration.source.repository || null,
      path: misconfiguration.source.path,
      reason: misconfiguration.reason,
    });
const triagePath = join(outputDir, "triage-entries.json");
writeFileSync(triagePath, `${JSON.stringify(triageEntries)}\n`);
const planDocument = {
  kind: "minos-publication-plan-v1",
  verdict,
  head: reviewed.head,
  target: reviewed.target,
  posts,
  triage: {
    entries: triagePath,
    advisory: triageEntries.filter((entry) => entry.kind === "advisory-finding").length,
    misconfigurations: triageEntries.filter((entry) => entry.kind === "review-brief-misconfiguration").length,
    guidanceMisconfigurations: triageEntries.filter((entry) => entry.kind === "guidance-source-misconfiguration").length,
  },
};
writeFileSync(join(outputDir, "publication-plan.json"), `${JSON.stringify(planDocument, null, 2)}\n`);
process.stdout.write(`${JSON.stringify(planDocument)}\n`);
