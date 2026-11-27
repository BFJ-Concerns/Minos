import { CONTINUATION, INCOMPLETE, SUPERSEDED } from "../run-endings.mjs";
import { applied, movedHead, setStatus, showSummary } from "./forge-writes.mjs";
import { summaryFacts } from "./summary-facts.mjs";

// Keep operator diagnostics, which can include local paths and command
// output, in the run report. The author sees the operation that failed.
const incompleteConditions = {
  claim: "Minos could not claim the pull request for review.",
  markWorking: "Minos could not mark the pull request as in progress.",
  inspectPush: "Minos could not finish inspecting what changed.",
  review: "The specialist review did not complete.",
  reviewBriefs: "The repository brief review did not complete.",
  classify: "Minos could not finish classifying the findings.",
  settleEarlierFindings: "Minos could not determine which earlier blocking findings still stand.",
  publish: "Minos could not confirm publication of the findings.",
  conclude: "Minos could not confirm the final result on the pull request.",
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
        condition: incompleteConditions[ending.step] ?? "Minos stopped before recording a result.",
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
