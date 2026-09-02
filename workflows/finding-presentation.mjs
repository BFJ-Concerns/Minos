// The one authority for what Minos publishes onto a pull request about
// its review. Every plain-Node producer of forge review payloads — the
// publication composer above all — renders through this module, so what a
// review body says, what a finding comment carries, and how an unverified
// observation is marked cannot drift between producers.
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
  return `\n\nProposed by ${proposingModel}; verified by ${verifyingModel}.`;
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
  const comment = {
    path: finding.path,
    body:
      `**${label} · ${finding.severity}: ${finding.title}**\n\n` +
      `${finding.explanation}${alsoRaised}${crossReferences}${provenance(finding)}`,
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

// The review body carries its own warrant: what was reviewed, what the
// review found and how much of it blocks, and what the author does next.
// `group` names the review this body opens — the main review, or a
// repository brief group — and `verdict` is the lead's validated decision
// for that group. A brief group's body names the briefs it ran, since those
// are the author's own criteria; skipped concerns stay in the run record.
// `dispositionOf` returns the lead's validated disposition for a finding
// (null when none), keyed however the caller's decision keys them.
export function reviewBody({ verdict, reviewed, findings, dispositionOf, group = "main", briefsRan = [] }) {
  const blocking = findings.filter((finding) => dispositionOf(finding)?.gating === true);
  const advisory = findings.filter((finding) => dispositionOf(finding)?.gating !== true);
  const lines = [];
  if (group === "main") {
    lines.push(VERDICT_HEADLINES[verdict]);
  } else {
    lines.push(
      verdict === "request-changes"
        ? "**Minos repository-brief review: changes need attention.**"
        : "**Minos repository-brief review: advisory findings.**",
    );
  }
  lines.push("");
  lines.push(`Reviewed head \`${short(reviewed.head)}\` against target \`${short(reviewed.target)}\`.`);
  if (briefsRan.length > 0)
    lines.push(`Briefs applied: ${briefsRan.map((brief) => `\`${brief}\``).join(", ")}.`);
  if (findings.length === 0) {
    lines.push("No confirmed findings.");
  } else {
    const files = [...new Set(findings.map((finding) => finding.path))].sort();
    lines.push(
      `${plural(findings.length, "confirmed finding")} (${severityRollUp(findings)}) ` +
        `in ${files.map((file) => `\`${file}\``).join(", ")}, each anchored inline where the diff allows.`,
    );
    if (blocking.length > 0)
      lines.push(`${plural(blocking.length, "blocking finding")} must be resolved before this review approves.`);
    if (advisory.length > 0)
      lines.push(`${plural(advisory.length, "advisory finding")} ${advisory.length === 1 ? "is" : "are"} for your judgement and ${advisory.length === 1 ? "does" : "do"} not block.`);
  }
  lines.push("");
  lines.push(`This review stands for head \`${short(reviewed.head)}\` only: push a new commit and Minos reviews the new head afresh.`);
  return lines.join("\n");
}

// Unverified side material — out-of-scope observations and review-brief
// misconfigurations — renders as one comment review, each entry plainly
// marked unverified so it can never read as a finding.
export function observationComment(entry) {
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

export const OBSERVATIONS_BODY = "Unverified observations from the review, for the author's judgement.";
