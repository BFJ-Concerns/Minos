import { writeFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import { runCommand } from "../run-process.mjs";
import { DEFAULT_THRESHOLD } from "../verdict-classification.mjs";
import { applied, currentSnapshot, movedHead } from "./forge-writes.mjs";

function helper(name) {
  return fileURLToPath(new URL(`../${name}`, import.meta.url));
}

function recordDecision(run, name, decision) {
  const path = join(run.runDir, name);
  writeFileSync(path, `${JSON.stringify(decision)}\n`);
  return path;
}

export async function publish(run, { review, briefReview, reviewFile, briefReviewFile, decisions }) {
  if (!review) return;
  const output = join(run.runDir, "publication");
  const args = [
    output, run.orientation, run.env.MINOS_REVIEW_THRESHOLD || DEFAULT_THRESHOLD,
    reviewFile, recordDecision(run, "verdict-decision.json", decisions.main),
  ];
  if (briefReview) args.push(briefReviewFile, recordDecision(run, "verdict-decision-brief.json", decisions.brief));
  const plan = JSON.parse(await runCommand(run, process.execPath, [helper("compose-review-publication.mjs"), ...args]));
  for (const post of plan.posts) {
    await applied(run, "review", [run.head, run.target, post.verdict, post.body, post.comments]);
  }

  let filing;
  try {
    const { entries, ...counts } = plan.triage;
    if (Object.values(counts).some((count) => count > 0)) {
      const { kind } = JSON.parse(run.env.MINOS_FILING_DESTINATION);
      if (kind === "file" || kind === "issue") await currentSnapshot(run);
    }
    const result = await runCommand(run, process.execPath, [helper("file-triage.mjs"), entries, run.orientation]);
    filing = JSON.parse(result);
  } catch (error) {
    if (movedHead(error)) throw error;
    filing = { outcome: "unfiled", reason: error.message };
  }
  let filingRecordProblem;
  try {
    writeFileSync(join(run.runDir, "triage-result.json"), `${JSON.stringify(filing)}\n`);
  } catch (error) {
    filingRecordProblem = `Could not record triage delivery. ${error.message}`;
  }
  return { publication: { plan, filing, filingRecordProblem } };
}
