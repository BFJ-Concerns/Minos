// The one authority for what Minos publishes about its review. Every
// plain-Node producer of review material — the publication composer and
// the issue-log filing above all — renders through this module, so what a
// review body says, what a finding comment carries, how an issue-log entry
// reads and how unverified material is marked cannot drift between
// producers.
//
// Payload text is author-facing. Operator material — confidence values,
// probe questions and unit concerns, worker labels — belongs in the run
// record and never enters a payload here: the author reads what the
// finding is, how much it matters, and whether it blocks, not how the
// service convinced itself of it.
//
// Vocabulary is the forge status's own (internal/product): a blocking review
// says "changes need attention" and an approving one "changes approved", so
// the review body, the status description and the comment grammar read as
// one voice.

export const VERDICT_HEADLINES = Object.freeze({
  "request-changes": "**Minos: changes need attention.**",
  clean: "**Minos: changes approved.**",
});

const UNVERIFIED_NOTICE = "This was noticed outside the review's scope and has not been verified.";

function modelAttribution(model) {
  if (!model || typeof model !== "object") return null;
  if (typeof model.resolvedModel === "string" && model.resolvedModel !== "") {
    if (typeof model.pinnedModel === "string" && model.pinnedModel !== "" && model.pinnedModel !== model.resolvedModel)
      return `\`${model.resolvedModel}\` (pinned \`${model.pinnedModel}\`)`;
    return `\`${model.resolvedModel}\``;
  }
  if (typeof model.pinnedModel === "string" && model.pinnedModel !== "") return `\`${model.pinnedModel}\``;
  return null;
}

// One quiet provenance line (C42): both families on one line, or nothing
// when the run record carries no model evidence for either.
function provenance(finding) {
  const proposingModel = modelAttribution(finding.proposingModel);
  const verifyingModel = modelAttribution(finding.verifyingModel);
  if (!proposingModel || !verifyingModel) return "";
  return `Proposed by ${proposingModel}; verified by ${verifyingModel}.`;
}

// A finding comment's first line is its structural grammar: whether it
// blocks and how severe it is, before its title. The disposition is the
// lead's validated classification for this finding; an absent disposition
// renders as advisory, since only a recorded gating call may block.
export function findingComment(finding, disposition = null) {
  const gating = Boolean(disposition && disposition.gating === true);
  const label = gating ? "Blocking" : "Advisory";
  const crossReferences = Array.isArray(finding.crossReferences) && finding.crossReferences.length > 0
    ? `\n\nSee also on this line: ${finding.crossReferences.map((title) => `"${title}"`).join(", ")}.`
    : "";
  const alsoRaised = Array.isArray(finding.alsoRaisedBy) && finding.alsoRaisedBy.length > 0
    ? `\n\nAlso raised by: ${finding.alsoRaisedBy.join(", ")}.`
    : "";
  const attribution = provenance(finding);
  const comment = {
    path: finding.path,
    body:
      `**${label} · ${finding.severity}: ${finding.title}**\n\n` +
      `${finding.explanation}${alsoRaised}${crossReferences}${attribution === "" ? "" : `\n\n${attribution}`}`,
    line: finding.line,
  };
  // A finding about a range says so, so the forge boundary can cover the
  // lines it concerns rather than only the first of them.
  if (Number.isInteger(finding.endLine) && finding.endLine > finding.line)
    comment.end_line = finding.endLine;
  return comment;
}

function severityRollUp(findings) {
  const counts = new Map();
  for (const finding of findings) counts.set(finding.severity, (counts.get(finding.severity) || 0) + 1);
  return ["Critical", "High", "Medium", "Low"]
    .filter((severity) => counts.has(severity))
    .map((severity) => `${counts.get(severity)} ${severity}`)
    .join(", ");
}

function plural(count, noun) {
  return `${count} ${noun}${count === 1 ? "" : "s"}`;
}

function short(sha) {
  return typeof sha === "string" && sha.length >= 7 ? sha.slice(0, 7) : String(sha);
}

// Names the triage material that left the review for the issue log, in the
// author's own terms: how many advisory findings, unverified observations
// and brief misconfigurations. Empty when there is none.
function triageSummary(triage) {
  const counts = [
    [triage.advisory, "advisory finding"],
    [triage.observations, "unverified observation"],
    [triage.misconfigurations, "review-brief misconfiguration"],
  ].filter(([count]) => count > 0);
  if (counts.length === 0) return "";
  const parts = counts.map(([count, noun]) => plural(count, noun));
  const list = parts.length === 1 ? parts[0] : `${parts.slice(0, -1).join(", ")} and ${parts[parts.length - 1]}`;
  return `${list} ${counts.reduce((sum, [count]) => sum + count, 0) === 1 ? "is" : "are"} filed for triage, not on this review.`;
}

// The review body carries its own warrant: what was reviewed, what blocks
// and what left the review for triage, and what the author does next.
// `group` names the review this body opens — the main review, or a
// repository brief group — and `verdict` is the lead's validated decision
// for that group. Every finding on a review blocks; the triage counts name
// the rest. A brief group's body names the briefs it ran, since those are
// the author's own criteria; skipped concerns stay in the run record.
export function reviewBody({ verdict, reviewed, findings, group = "main", briefsRan = [], triage = null }) {
  const lines = [];
  if (group === "main") {
    lines.push(VERDICT_HEADLINES[verdict]);
  } else {
    lines.push("**Minos repository-brief review: changes need attention.**");
  }
  lines.push("");
  lines.push(`Reviewed head \`${short(reviewed.head)}\` against target \`${short(reviewed.target)}\`.`);
  if (briefsRan.length > 0)
    lines.push(`Briefs applied: ${briefsRan.map((brief) => `\`${brief}\``).join(", ")}.`);
  if (findings.length === 0) {
    lines.push("No blocking findings.");
  } else {
    const files = [...new Set(findings.map((finding) => finding.path))].sort();
    lines.push(
      `${plural(findings.length, "blocking finding")} (${severityRollUp(findings)}) ` +
        `in ${files.map((file) => `\`${file}\``).join(", ")}, each anchored inline where the diff allows; ` +
        `${findings.length === 1 ? "it" : "they"} must be resolved before this review approves.`,
    );
  }
  const summary = triage ? triageSummary(triage) : "";
  if (summary !== "") lines.push(summary);
  lines.push("");
  lines.push(`This review stands for head \`${short(reviewed.head)}\` only: push a new commit and Minos reviews the new head afresh.`);
  return lines.join("\n");
}

// Triage material — an advisory finding, an unverified observation or a
// review-brief misconfiguration — is one entry shape with three kinds. The
// two renderings below agree on what each kind says; only the surface
// differs.

// Prose folded into one list item: multi-line text stays inside the item,
// and the text ends as a sentence so what follows it reads as the next one.
function sentence(text) {
  const folded = String(text).trim().replace(/\n/g, "\n  ");
  return /[.!?]$/.test(folded) ? folded : `${folded}.`;
}

// One issue-log entry: a Markdown list item the project's triage reads cold,
// carrying the entry's kind, severity where it has one, title, site,
// explanation, provenance where the run recorded it, and its source.
export function issueLogEntry(entry, attribution) {
  if (entry.kind === "advisory-finding") {
    const attributionLine = provenance(entry);
    return (
      `- **Advisory · ${entry.severity}: ${entry.title}** (\`${entry.path}:${entry.line}\`) — ${sentence(entry.explanation)}` +
      `${attributionLine === "" ? "" : ` ${attributionLine}`} ${attribution}.`
    );
  }
  if (entry.kind === "out-of-scope-observation") {
    return (
      `- **Unverified observation: ${entry.title}** (\`${entry.path}:${entry.line}\`) — ${sentence(entry.explanation)} ` +
      `${UNVERIFIED_NOTICE} ${attribution}.`
    );
  }
  if (entry.kind === "review-brief-misconfiguration") {
    return `- **Review brief misconfiguration: ${entry.title}** (\`${entry.brief}\`) — ${sentence(entry.reason)} ${attribution}.`;
  }
  throw new Error(`unknown entry kind ${String(entry.kind)}`);
}

// The same entry as one pull-request comment, for a project with no issue
// log to file into: an advisory finding keeps the finding grammar with no
// blocking disposition; unverified material is plainly marked so it can
// never read as a finding.
export function triageComment(entry) {
  if (entry.kind === "advisory-finding") return findingComment(entry, null);
  if (entry.kind === "out-of-scope-observation") {
    return {
      path: entry.path,
      body: `**Unverified observation: ${entry.title}**\n\n${entry.explanation}\n\n${UNVERIFIED_NOTICE}`,
      line: entry.line,
    };
  }
  if (entry.kind === "review-brief-misconfiguration") {
    return {
      path: entry.brief,
      body: `**Review brief misconfiguration: ${entry.title}**\n\n${entry.reason}\n\n${UNVERIFIED_NOTICE}`,
      line: 1,
    };
  }
  throw new Error(`unknown entry kind ${String(entry.kind)}`);
}

export const TRIAGE_BODY =
  "Advisory findings and unverified observations from the review, for the author's judgement: none of these block.";
