import { CONTINUATION, INCOMPLETE, SUPERSEDED } from "../run-endings.mjs";
import { applied, movedHead, setStatus, showSummary } from "./forge-writes.mjs";
import { summaryFacts } from "./summary-facts.mjs";

// Keep operator diagnostics, which can include local paths and command
// output, in the run report. The author sees the operation that failed.
// Each cause is worded as what happened to the review of the head, never
// as what the service could not do.
const incompleteConditions = {
  claim: "the pull request could not be claimed for review.",
  markWorking: "the pull request could not be marked as in progress.",
  inspectPush: "the inspection of what changed did not finish.",
  review: "the specialist review did not complete.",
  reviewBriefs: "the repository brief review did not complete.",
  classify: "the classification of the findings did not finish.",
  settleEarlierFindings: "which earlier blocking findings still stand could not be determined.",
  publish: "the publication of the findings could not be confirmed.",
  conclude: "the final result could not be confirmed on the pull request.",
};

async function removeFromCurrentHead(run) {
  const snapshot = await run.forge("snapshot");
  await applied(run, "marker", [snapshot.head_sha, snapshot.target_sha, "in-flight", "remove"]);
}

export async function withdraw(run, settled, ending, problems) {
  if (ending.outcome === CONTINUATION) return;
  if (ending.outcome === SUPERSEDED) return removeFromCurrentHead(run);
  try {
    if (ending.outcome === INCOMPLETE) {
      const summaryProblem = await showSummary(run, INCOMPLETE, {
        ...summaryFacts(settled),
        condition: incompleteConditions[ending.step] ?? "the review stopped before recording a result.",
      });
      if (summaryProblem) problems?.push(summaryProblem);
      const statusProblem = await setStatus(run, "incomplete");
      if (statusProblem) problems?.push(statusProblem);
    }
    await applied(run, "marker", [run.head, run.target, "in-flight", "remove"]);
  } catch (error) {
    if (!movedHead(error)) throw error;
    await removeFromCurrentHead(run);
  }
}
