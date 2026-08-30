// The one authority for what Minos publishes onto a pull request about
// its findings. Every plain-Node producer of forge review payloads — the
// run-record adjudicator, the fix-wave planner — renders through this
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
  unresolved: "Confirmed code findings remain unresolved.",
});

export function findingComment(finding) {
  return {
    path: finding.path,
    body: `**${finding.title}**\n\n${finding.explanation}\n\nSeverity: ${finding.severity}.`,
    line: finding.line,
  };
}
