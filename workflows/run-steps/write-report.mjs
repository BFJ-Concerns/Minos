import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";

import { runCommand } from "../run-process.mjs";

export async function writeReport(run, settled, ending, closingProblems = []) {
  const { owner, repository, number } = run.pullRequest;
  const { inspection, decisions, outstanding, publication } = settled;
  const problems = [
    ...closingProblems,
    settled.workingStatusProblem, settled.terminalStatusProblem, settled.terminalMarkerProblem,
    settled.reviewDispatchProblem, settled.briefDispatchProblem, publication?.filingRecordProblem,
    ...(publication?.filing.outcome === "unfiled" ? [publication.filing.reason] : []),
  ].filter(Boolean);
  try {
    const orientation = JSON.parse(readFileSync(run.orientation, "utf8"));
    for (const { source, reason } of orientation.misconfigurations ?? []) {
      problems.push(`${source?.repository ? `${source.repository}:` : ""}${source?.path ?? "Guidance"} — ${reason}`);
    }
  } catch (error) {
    problems.push(`Could not read orientation for the report. ${error.message}`);
  }
  const skipped = (settled.briefReview?.skipped ?? []).map(({ brief, reason }) => `- Skipped ${brief} — ${reason}`);
  const earlierFindings = run.judgementAnswers?.get("earlier-findings")?.findings ?? [];
  const timings = await timingLines(run, problems);

  const lines = [
    `# ${owner}/${repository}#${number}`, "",
    `Head \`${run.head}\` ended **${ending.outcome}**.`,
    ...(ending.step ? [`${ending.step} — ${ending.cause}`] : []),
    ...(settled.terminalCause ? [settled.terminalCause] : []),
    "", "## Review", "",
    ...(inspection ? [`- Push inspection chose **${inspection.outcome}**. ${inspection.reason}`] : []),
    ...(settled.carriedReview ? ["- Reused the predecessor's completed main review."] : []),
    ...(decisions?.main ? [`- Main classification — ${decisions.main.basis}`] : []),
    ...(decisions?.brief ? [`- Brief classification — ${decisions.brief.basis}`] : []),
    ...(outstanding ? [`- Earlier blocking findings still outstanding — ${outstanding.length}`] : []),
    ...earlierFindings.map(({ key, outcome, reason }) => `  - ${key} — ${outcome}. ${reason}`),
    ...(publication ? [`- Triage delivery — ${publication.filing.outcome}`] : []),
    ...skipped,
    "", "## Timings", "", ...timings,
    ...(problems.length ? ["", "## Anomalies", "", ...problems.map((problem) => `- ${problem}`)] : []),
  ];
  writeFileSync(join(run.runDir, "report.md"), `${lines.join("\n")}\n`);
}

async function timingLines(run, problems) {
  const path = join(run.runDir, "timing-record.json");
  try {
    await runCommand(run, join(dirname(run.env.MINOS_SETUP_WORKSPACE), "collect-timings"), [run.runDir, path]);
    const { commands, phases } = JSON.parse(readFileSync(path, "utf8"));
    return [
      ...commands.map(({ name, attempt, duration_ms }) => `- ${name}${attempt > 1 ? `@${attempt}` : ""} — ${seconds(duration_ms)}`),
      ...phases.filter(({ straggler }) => straggler).map(({ record, phase, longest_arm }) =>
        `- ${record} / ${phase} waited on ${longest_arm.label} (${seconds(longest_arm.ms)}).`),
      ...(commands.length ? [] : ["No stage spans were recorded."]),
    ];
  } catch (error) {
    problems.push(`Could not assemble the recorded timings. ${error.message}`);
    return ["Timings unavailable."];
  }
}

function seconds(ms) {
  return `${(ms / 1000).toFixed(1)} s`;
}
