// The one authority for what Minos publishes onto a pull request about
// its findings. Every plain-Node producer of forge review payloads — the
// run-record adjudicator above all — renders through this
// module, so what a clean review says, what a blocking review says, and
// what a finding comment carries cannot drift between producers.
//
// Payload text is author-facing. Operator material — confidence values,
// probe questions and unit concerns, worker labels — belongs in the run
// record and never enters a payload here: the author reads what the
// finding is and how much it matters, not how the service convinced
// itself of it.

export const REVIEW_BODIES = Object.freeze({
  findings: "Confirmed findings in the reviewed code.",
  clean: "No confirmed findings in the reviewed code.",
  brief: "Repository review brief findings.",
});

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

export function findingComment(finding) {
  const proposingModel = modelAttribution(finding.proposingModel);
  const verifyingModel = modelAttribution(finding.verifyingModel);
  const attribution = proposingModel && verifyingModel
    ? `\n\nProposed by: ${proposingModel}.\nVerified by: ${verifyingModel}.`
    : "";
  const comment = {
    path: finding.path,
    body: `**${finding.title}**\n\n${finding.explanation}\n\nSeverity: ${finding.severity}.${attribution}`,
    line: finding.line,
  };
  // A finding about a range says so, so the forge boundary can cover the
  // lines it concerns rather than only the first of them.
  if (Number.isInteger(finding.endLine) && finding.endLine > finding.line)
    comment.end_line = finding.endLine;
  return comment;
}
