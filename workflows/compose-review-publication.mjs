#!/usr/bin/env node

// The publication composer: the one deterministic step between the run's
// validated verdicts and what leaves the run. It turns the adjudicated
// verdicts and the lead's classification decisions into the exact files
// the guarded forge review command posts, in the order they are posted,
// and the triage material the issue-log filing delivers, so no lead ever
// hand-assembles a payload or re-derives the routing.
//
// The pull request carries what blocks it and nothing else: a
// request-changes review holds the gating findings, and a clean result
// writes no review. Every other confirmed finding is advice, and goes to
// the configured filing destination on either outcome, where the project
// weighs it in its own time. So do the unverified observations reviewers
// made outside the change, marked as unverified, and the findings a
// verifier left unsettled, marked as open questions and never as defects. One defect leaves the run
// once: the same site and title raised by both groups merge mechanically,
// and findings the lead's decisions label as one defect merge on that
// judgement, whatever their sites and titles.
// A finding the lead classed as already raised, as speculative hardening or
// as a gap the description declares leaves the run on neither surface: the
// plan counts it and nothing more.
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
import { SEVERITY, alreadyRaised, declaredOutOfScope, findingKey, normalisedTitle, speculativeHardening, validateVerdictDecision, verdictDigest } from "./verdict-classification.mjs";

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

function refuse(reason) {
  process.stderr.write(`${reason}\n`);
  process.exit(1);
}

function nameSource(finding, source) {
  if (source === finding.source || (finding.alsoRaisedBy || []).includes(source)) return;
  finding.alsoRaisedBy = [...(finding.alsoRaisedBy || []), source];
}

// Deduplication across the two groups. The same defect raised by both is
// one finding: brief findings with the main group's site and title merge
// into the main finding, which gates if any member's disposition gates,
// carries the highest severity, names every brief that also raised it, and
// keeps the members' one `defect` label so the labelled merge below still
// sees it. The merge reads all of a main finding's twins together, so the
// order the brief verdict lists them in changes nothing. A merged finding
// that gates carries no never-gating class, whichever disposition held
// one; twins that all stay advisory keep the class they share. Twins that
// carry two labels, are classed differently while advisory, or disagree on
// whether the author already has the finding are a decision to correct, so
// the composer refuses them rather than publish or withhold the finding by
// one side's call.
if (brief) {
  // The main review may confirm more than one finding at a site and title.
  // Brief twins merge into the first by key, so the main listing order
  // picks nothing. The validated main decision has already given those
  // findings one answer on whether the author has the defect, so the first
  // speaks for them all.
  const mainBySiteAndTitle = new Map();
  for (const finding of main.findings)
    mainBySiteAndTitle.set(siteAndTitle(finding), [...(mainBySiteAndTitle.get(siteAndTitle(finding)) || []), finding]);
  for (const findings of mainBySiteAndTitle.values())
    findings.sort((a, b) => findingKey(a).localeCompare(findingKey(b)));
  const briefTwinsOf = new Map();
  brief.findings = brief.findings.filter((finding) => {
    const twin = mainBySiteAndTitle.get(siteAndTitle(finding))?.[0];
    if (!twin) return true;
    briefTwinsOf.set(twin, [...(briefTwinsOf.get(twin) || []), finding]);
    return false;
  });
  for (const [twin, briefTwins] of briefTwinsOf) {
    const at = `${twin.path}:${twin.line}`;
    const twinDisposition = main.dispositionOf(twin) || {};
    const briefDispositions = briefTwins.map((finding) => brief.dispositionOf(finding) || {});
    const members = [twinDisposition, ...briefDispositions];
    // Main's call is named first, then the brief's distinct calls in a
    // fixed order, so a refusal reads the same in any listing order.
    const calls = (read) => {
      const first = read(twinDisposition);
      const others = [...new Set(briefDispositions.map(read))].filter((value) => value !== first)
        .sort((a, b) => String(a).localeCompare(String(b)));
      return [first, ...others];
    };
    const labels = calls((disposition) => disposition.defect).filter((label) => typeof label === "string");
    if (labels.length > 1)
      refuse(`one finding at ${at} carries two defect labels across the decisions: ${labels.join(" and ")}`);
    const gating = members.some((disposition) => disposition.gating === true);
    const classes = calls((disposition) => disposition.class);
    if (!gating && classes.length > 1)
      refuse(`one finding at ${at} is classed differently across the decisions: ${classes.map((name) => name ?? "unclassed").join(" and ")}`);
    if (new Set(members.map(alreadyRaised)).size > 1)
      refuse(`one finding at ${at} is already raised in one decision and new in the other`);
    for (const finding of briefTwins) {
      if (SEVERITY[finding.severity] > SEVERITY[twin.severity]) twin.severity = finding.severity;
    }
    for (const source of briefTwins.map((finding) => finding.source).sort()) nameSource(twin, source);
    const merged = { ...twinDisposition, gating, ...(labels.length === 1 ? { defect: labels[0] } : {}) };
    if (gating) delete merged.class;
    main.dispositions.set(findingKey(twin), merged);
  }
}

const groups = [main, brief].filter(Boolean);

// Restatements the lead judged. Dispositions carrying the same `defect`
// label — in one decision or across the two — describe one defect, which
// the composer publishes once at its highest-severity member (the main
// group's, then the first path, the lowest line, the title and the key, on
// ties, so the listing order never picks it), naming the
// other members' sites and the groups that also raised it. Members gate
// and are classed alike: the validator holds that within one decision, and
// the composer refuses a label split across the two, so the canonical
// member's own disposition is the defect's. The site-and-title merge above is the floor
// a decision without labels still gets; this pass is the same-defect
// judgement that merge cannot make. A member already raised merges into
// nothing: the author has the defect at that site, so it is counted and
// not said again, while the label's other members carry the defect's call
// at the sites the author has not been told of.
//
// Whether the author already has the defect is a fact about a site, so a
// label's findings at one line take one answer on it across the decisions,
// as the validator holds within one: an already-raised member set aside
// beside a gating one at the same line would post again what the author
// has.
const raisedAtLabelledSite = new Map();
for (const group of groups)
  for (const finding of group.findings) {
    const disposition = group.dispositionOf(finding);
    if (typeof disposition?.defect !== "string") continue;
    const id = JSON.stringify([finding.path, finding.line, disposition.defect]);
    const seen = raisedAtLabelledSite.get(id);
    if (seen !== undefined && seen !== alreadyRaised(disposition))
      refuse(`findings labelled ${disposition.defect} at ${finding.path}:${finding.line} are already raised in one decision and new in the other`);
    raisedAtLabelledSite.set(id, alreadyRaised(disposition));
  }
const byDefect = new Map();
for (const group of groups)
  for (const finding of group.findings) {
    const disposition = group.dispositionOf(finding);
    const label = disposition?.defect;
    if (typeof label !== "string" || alreadyRaised(disposition)) continue;
    byDefect.set(label, [...(byDefect.get(label) || []), { group, finding }]);
  }
for (const [label, members] of byDefect) {
  if (members.length < 2) continue;
  const gatings = new Set(members.map(({ group, finding }) => group.dispositionOf(finding)?.gating === true));
  if (gatings.size > 1) refuse(`findings labelled one defect gate differently across the decisions: ${label}`);
  const classes = new Set(members.map(({ group, finding }) => group.dispositionOf(finding)?.class));
  if (classes.size > 1) refuse(`findings labelled one defect are classed differently across the decisions: ${label}`);
  members.sort((a, b) =>
    SEVERITY[b.finding.severity] - SEVERITY[a.finding.severity]
    || groups.indexOf(a.group) - groups.indexOf(b.group)
    || a.finding.path.localeCompare(b.finding.path)
    || a.finding.line - b.finding.line
    || normalisedTitle(a.finding).localeCompare(normalisedTitle(b.finding))
    || findingKey(a.finding).localeCompare(findingKey(b.finding)));
  const [{ finding: canonical }, ...restatements] = members;
  for (const { group, finding } of restatements) {
    const at = `${finding.path}:${finding.line}`;
    if (at !== `${canonical.path}:${canonical.line}` && !(canonical.alsoAt || []).includes(at))
      canonical.alsoAt = [...(canonical.alsoAt || []), at];
    for (const source of [finding.source, ...(finding.alsoRaisedBy || [])]) nameSource(canonical, source);
    group.findings = group.findings.filter((other) => other !== finding);
  }
}

const dispositionOf = (finding) => groups.find((group) => group.findings.includes(finding)).dispositionOf(finding);
// What the author already has or has declared is not said again, and a
// safeguard nothing in the change calls for is not handed on as work: these
// findings were confirmed, so they are counted, and they reach no review
// and no filing.
const confirmed = groups.flatMap((group) => group.findings);
const surfacesNowhere = (disposition) =>
  alreadyRaised(disposition) || speculativeHardening(disposition) || declaredOutOfScope(disposition);
const findings = confirmed.filter((finding) => !surfacesNowhere(dispositionOf(finding)));
const blocking = findings.filter((finding) => dispositionOf(finding)?.gating === true);
const advisory = findings.filter((finding) => dispositionOf(finding)?.gating !== true);
const verdict = blocking.length > 0 ? "request-changes" : "clean";

// Distinct blocking findings at one site name their neighbours, so a
// comment only ever points at another comment on the pull request;
// duplicates were merged above. A merged defect is present at every site
// it was raised at, so a finding anchored where one of its restatements
// was names it too.
const sitesOf = (finding) => [site(finding), ...(finding.alsoAt || []).map((at) => {
  const separator = at.lastIndexOf(":");
  return JSON.stringify([at.slice(0, separator), Number(at.slice(separator + 1))]);
})];
const bySite = new Map();
for (const finding of blocking)
  for (const key of sitesOf(finding)) bySite.set(key, [...(bySite.get(key) || []), finding]);
for (const finding of blocking) {
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
    reviewed, findings: blocking,
    briefsRan: (brief?.verdict.ran || []).map((entry) => entry.brief),
  })}\n`);
  writeFileSync(comments, `${JSON.stringify(blocking.map((finding) => findingComment(finding, dispositionOf(finding))))}\n`);
  posts.push({ review: "main", verdict, body, comments });
}

// An observation is filed unless a confirmed finding of this run already
// stands at its site: there the verified account is the one to keep.
const confirmedSites = new Set(confirmed.map(site));
const observations = groups
  .flatMap((group) => group.verdict.outOfScopeObservations || [])
  .filter((observation) => !confirmedSites.has(site(observation)));
// An unsettled finding is filed as unverified material is, on the same
// terms as an observation.
const unsettled = groups
  .flatMap((group) => group.verdict.unsettledFindings || [])
  .filter((finding) => !confirmedSites.has(site(finding)));
const triageEntries = [
  ...advisory.map((finding) => ({ ...finding, kind: "advisory-finding" })),
  ...unsettled.map(({ title, severity, path, line, explanation, outsideFact, wouldSettle }) =>
    ({ kind: "unsettled-finding", title, severity, path, line, explanation, outsideFact, wouldSettle })),
  ...observations.map(({ title, path, line, explanation }) => ({ kind: "out-of-scope-observation", title, path, line, explanation })),
];
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
  alreadyRaised: confirmed.filter((finding) => alreadyRaised(dispositionOf(finding))).length,
  speculativeHardening: confirmed.filter((finding) => speculativeHardening(dispositionOf(finding))).length,
  declaredOutOfScope: confirmed.filter((finding) => declaredOutOfScope(dispositionOf(finding))).length,
  triage: {
    entries: triagePath,
    advisory: triageEntries.filter((entry) => entry.kind === "advisory-finding").length,
    unsettled: triageEntries.filter((entry) => entry.kind === "unsettled-finding").length,
    observations: triageEntries.filter((entry) => entry.kind === "out-of-scope-observation").length,
    misconfigurations: triageEntries.filter((entry) => entry.kind === "review-brief-misconfiguration").length,
    guidanceMisconfigurations: triageEntries.filter((entry) => entry.kind === "guidance-source-misconfiguration").length,
  },
};
writeFileSync(join(outputDir, "publication-plan.json"), `${JSON.stringify(planDocument, null, 2)}\n`);
process.stdout.write(`${JSON.stringify(planDocument)}\n`);
