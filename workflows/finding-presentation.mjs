// The one authority for what Minos publishes about its review. Every
// plain-Node producer of review material — the publication composer and
// the issue-log filing above all — renders through this module, so what a
// review body says, what a finding comment carries, how an issue-log entry
// reads and which material may be published cannot drift between
// producers.
//
// Payload text is author-facing. Operator material — confidence values,
// probe questions and unit concerns, worker labels — belongs in the run
// record and never enters a payload here: the author reads what the
// finding is, how much it matters, and whether it blocks, not how the
// service convinced itself of it.
//
// A prose location is trustworthy only when it names this finding's anchor.
// Path syntax is repository-relative (a directory or filename extension), or
// the exact anchored path for extensionless root files. URL tokens are kept
// whole so neither their port nor a path inside them becomes a citation.
// Numeric diagnostic columns belong to the location token: stripping a line
// consumes them too, so a column never becomes a new line citation.
// This is lexical normalisation: it never reads files or guesses a new line.
function normaliseProse(text, finding) {
  return String(text).replace(
    /(?:[a-z][a-z0-9+.-]*:\/\/|\/\/)[^\s`"'()[\]{}<>]+|([^\s`"'()[\]{}<>:;,!?*]+):(\d+)(?:-(\d+))?(?::\d+)*/gi,
    (citation, path, line, endLine) => {
      if (path === undefined) return citation;
      const isPath = path === finding.path || path.includes("/") || /\.[a-z][a-z0-9]*$/i.test(path);
      if (!isPath) return citation;
      const ownAnchor = path === finding.path && Number(line) === finding.line;
      const ownRange = endLine === undefined ||
        (Number.isInteger(finding.endLine) && finding.endLine > finding.line && Number(endLine) === finding.endLine);
      return ownAnchor && ownRange ? citation : path;
    },
  );
}

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

// One quiet provenance line: both families on one line, or nothing
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
    ? `\n\nSee also on this line: ${finding.crossReferences.map((title) => `"${normaliseProse(title, finding)}"`).join(", ")}.`
    : "";
  const alsoRaised = Array.isArray(finding.alsoRaisedBy) && finding.alsoRaisedBy.length > 0
    ? `\n\nAlso raised by: ${finding.alsoRaisedBy.map((source) => normaliseProse(source, finding)).join(", ")}.`
    : "";
  // The other sites of a defect the decision merged here; literal sites,
  // never normalised to this comment's anchor.
  const alsoAt = Array.isArray(finding.alsoAt) && finding.alsoAt.length > 0
    ? `\n\nAlso at: ${finding.alsoAt.map((site) => `\`${site}\``).join(", ")}.`
    : "";
  const attribution = provenance(finding);
  const comment = {
    path: finding.path,
    body:
      `**${label} · ${finding.severity}: ${normaliseProse(finding.title, finding)}**\n\n` +
      `${normaliseProse(finding.explanation, finding)}${alsoRaised}${alsoAt}${crossReferences}${attribution === "" ? "" : `\n\n${attribution}`}`,
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

// The review names the blocking findings and counts the advisory findings
// carried alongside them. Brief names preserve the repository criteria.
export function reviewBody({ reviewed, findings, briefsRan = [], advisory = 0 }) {
  const lines = ["**Minos: changes need attention.**"];
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
  if (advisory > 0) lines.push(`${plural(advisory, "advisory finding")} included for the same review round.`);
  lines.push("");
  lines.push(`This review stands for head \`${short(reviewed.head)}\` only: push a new commit and Minos reviews the new head afresh.`);
  return lines.join("\n");
}

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
    const alsoAt = Array.isArray(entry.alsoAt) && entry.alsoAt.length > 0
      ? `, also at ${entry.alsoAt.map((site) => `\`${site}\``).join(", ")}`
      : "";
    return (
      `- **Advisory · ${entry.severity}: ${normaliseProse(entry.title, entry)}** (\`${entry.path}:${entry.line}\`${alsoAt}) — ${sentence(normaliseProse(entry.explanation, entry))}` +
      `${attributionLine === "" ? "" : ` ${attributionLine}`} ${attribution}.`
    );
  }
  if (entry.kind === "review-brief-misconfiguration") {
    return `- **Review brief misconfiguration: ${entry.title}** (\`${entry.brief}\`) — ${sentence(entry.reason)} ${attribution}.`;
  }
  if (entry.kind === "guidance-source-misconfiguration") {
    const source = `${entry.sourceRepository || entry.repository}:${entry.path}`;
    return `- **Guidance source misconfiguration: ${source}** — configured guidance for ${entry.repository} could not be read: ${sentence(entry.reason)} ${attribution}.`;
  }
  throw new Error(`unknown entry kind ${String(entry.kind)}`);
}
